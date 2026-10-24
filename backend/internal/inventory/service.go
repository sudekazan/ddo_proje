package inventory

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sudekazan/ddo_proje/backend/internal/audit"
	"github.com/sudekazan/ddo_proje/backend/internal/auth"
	"github.com/sudekazan/ddo_proje/backend/internal/httpx"
	"github.com/sudekazan/ddo_proje/backend/internal/platform"
)

type Service struct {
	Pool *pgxpool.Pool
	Loc  *time.Location
}

type recipeLine struct {
	SupplyID int64
	SKU      string
	Name     string
	QtyMilli int64
}

// Consume deducts the recipe of a treatment from stock, lot by lot in FEFO order, skipping
// expired lots. Lots are locked with FOR UPDATE in a fixed order (supply, expiry, id), so
// concurrent completions queue up instead of over-drawing; the CHECK (qty_milli >= 0)
// constraint is the final guard. Fails with 409 INSUFFICIENT_STOCK without partial effects.
func Consume(ctx context.Context, tx pgx.Tx, apptID int64, code string, today time.Time, actorID int64) error {
	rows, err := tx.Query(ctx, `SELECT r.supply_id, s.sku, s.name, r.qty_milli FROM "tn-recipes" r
		JOIN "tn-supplies" s ON s.id = r.supply_id WHERE r.treatment_code = $1 ORDER BY r.supply_id`, code)
	if err != nil {
		return err
	}
	lines, err := pgx.CollectRows(rows, pgx.RowToStructByPos[recipeLine])
	if err != nil {
		return err
	}
	for _, line := range lines {
		rows, err := tx.Query(ctx, `SELECT id, qty_milli FROM "tn-supply_lots"
			WHERE supply_id = $1 AND qty_milli > 0 AND expires_on >= $2::date
			ORDER BY expires_on, id FOR UPDATE`, line.SupplyID, today.Format(time.DateOnly))
		if err != nil {
			return err
		}
		lots, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Lot])
		if err != nil {
			return err
		}
		takes, ok := AllocateFEFO(lots, line.QtyMilli)
		if !ok {
			return httpx.Conflict("INSUFFICIENT_STOCK", fmt.Sprintf("%s (%s) stoğu yetersiz", line.Name, line.SKU))
		}
		for _, t := range takes {
			if _, err := tx.Exec(ctx, `UPDATE "tn-supply_lots" SET qty_milli = qty_milli - $2 WHERE id = $1`, t.LotID, t.QtyMilli); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO "tn-stock_movements" (supply_id, lot_id, appointment_id, kind, qty_milli, created_by)
				VALUES ($1, $2, $3, 'consume', $4, NULLIF($5, 0))`, line.SupplyID, t.LotID, apptID, -t.QtyMilli, actorID); err != nil {
				return err
			}
		}
		if err := refreshAlert(ctx, tx, line.SupplyID); err != nil {
			return err
		}
	}
	return nil
}

// Return puts back exactly what an appointment consumed, into the same lots.
func Return(ctx context.Context, tx pgx.Tx, apptID int64, actorID int64) error {
	rows, err := tx.Query(ctx, `SELECT m.id, m.supply_id, m.lot_id, -m.qty_milli FROM "tn-stock_movements" m
		WHERE m.appointment_id = $1 AND m.kind = 'consume'
		  AND NOT EXISTS (SELECT 1 FROM "tn-stock_movements" r WHERE r.reverses_id = m.id)
		ORDER BY m.supply_id, m.lot_id`, apptID)
	if err != nil {
		return err
	}
	type mv struct{ ID, SupplyID, LotID, Qty int64 }
	moves, err := pgx.CollectRows(rows, pgx.RowToStructByPos[mv])
	if err != nil {
		return err
	}
	for _, m := range moves {
		if _, err := tx.Exec(ctx, `UPDATE "tn-supply_lots" SET qty_milli = qty_milli + $2 WHERE id = $1`, m.LotID, m.Qty); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO "tn-stock_movements" (supply_id, lot_id, appointment_id, kind, qty_milli, reverses_id, created_by)
			VALUES ($1, $2, $3, 'return', $4, $5, NULLIF($6, 0))`, m.SupplyID, m.LotID, apptID, m.Qty, m.ID, actorID); err != nil {
			return err
		}
		if err := refreshAlert(ctx, tx, m.SupplyID); err != nil {
			return err
		}
	}
	return nil
}

// refreshAlert opens a minimum stock alert when usable stock falls below the minimum and
// resolves it when stock is replenished.
func refreshAlert(ctx context.Context, tx pgx.Tx, supplyID int64) error {
	var total, minimum int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT sum(qty_milli) FROM "tn-supply_lots" WHERE supply_id = s.id AND expires_on >= current_date), 0),
		s.min_stock_milli FROM "tn-supplies" s WHERE s.id = $1`, supplyID).Scan(&total, &minimum); err != nil {
		return err
	}
	if total < minimum {
		_, err := tx.Exec(ctx, `INSERT INTO "tn-stock_alerts" (supply_id, qty_milli) VALUES ($1, $2)
			ON CONFLICT (supply_id) WHERE resolved_at IS NULL DO UPDATE SET qty_milli = EXCLUDED.qty_milli`, supplyID, total)
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE "tn-stock_alerts" SET resolved_at = now() WHERE supply_id = $1 AND resolved_at IS NULL`, supplyID)
	return err
}

type Supply struct {
	ID            int64      `json:"id"`
	SKU           string     `json:"sku"`
	Name          string     `json:"name"`
	Unit          string     `json:"unit"`
	MinStockMilli int64      `json:"min_stock_milli"`
	StockMilli    int64      `json:"stock_milli"`
	ExpiredMilli  int64      `json:"expired_milli"`
	NextExpiry    *time.Time `json:"next_expiry"`
	BelowMinimum  bool       `json:"below_minimum"`
}

func (s *Service) listSupplies(w http.ResponseWriter, r *http.Request) error {
	rows, err := s.Pool.Query(r.Context(), `
		SELECT s.id, s.sku, s.name, s.unit, s.min_stock_milli,
		       COALESCE(sum(l.qty_milli) FILTER (WHERE l.expires_on >= current_date), 0),
		       COALESCE(sum(l.qty_milli) FILTER (WHERE l.expires_on < current_date), 0),
		       min(l.expires_on) FILTER (WHERE l.expires_on >= current_date AND l.qty_milli > 0),
		       COALESCE(sum(l.qty_milli) FILTER (WHERE l.expires_on >= current_date), 0) < s.min_stock_milli
		FROM "tn-supplies" s LEFT JOIN "tn-supply_lots" l ON l.supply_id = s.id
		GROUP BY s.id ORDER BY s.name`)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Supply])
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
	return nil
}

type LotView struct {
	ID         int64     `json:"id"`
	LotNo      string    `json:"lot_no"`
	ExpiresOn  time.Time `json:"expires_on"`
	QtyMilli   int64     `json:"qty_milli"`
	ReceivedAt time.Time `json:"received_at"`
	Expired    bool      `json:"expired"`
}

func (s *Service) listLots(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.Int64Param(chi.URLParam(r, "supplyID"), "supplyID")
	if err != nil {
		return err
	}
	rows, err := s.Pool.Query(r.Context(), `SELECT id, lot_no, expires_on, qty_milli, received_at, expires_on < current_date
		FROM "tn-supply_lots" WHERE supply_id = $1 ORDER BY expires_on, id`, id)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[LotView])
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
	return nil
}

type createSupplyRequest struct {
	SKU           string `json:"sku"`
	Name          string `json:"name"`
	Unit          string `json:"unit"`
	MinStockMilli int64  `json:"min_stock_milli"`
}

func (r createSupplyRequest) Validate() error {
	var v httpx.V
	v.Check(r.SKU != "", "sku", "zorunlu")
	v.Check(r.Name != "", "name", "zorunlu")
	v.Check(r.Unit == "g" || r.Unit == "ml" || r.Unit == "adet", "unit", "g, ml veya adet olmalı")
	v.Check(r.MinStockMilli >= 0, "min_stock_milli", "negatif olamaz")
	return v.Err()
}

func (s *Service) createSupply(w http.ResponseWriter, r *http.Request) error {
	var req createSupplyRequest
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	actor := auth.MustFromContext(r.Context())
	var id int64
	err := platform.InTx(r.Context(), s.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(r.Context(), `INSERT INTO "tn-supplies" (sku, name, unit, min_stock_milli) VALUES ($1, $2, $3, $4) RETURNING id`,
			req.SKU, req.Name, req.Unit, req.MinStockMilli).Scan(&id); err != nil {
			return err
		}
		return audit.Write(r.Context(), tx, audit.Entry{Actor: actor.UserID, Action: "supply.create", Entity: "supply", EntityID: id, New: req})
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"id": id})
	return nil
}

type receiveRequest struct {
	LotNo     string `json:"lot_no"`
	ExpiresOn string `json:"expires_on"`
	QtyMilli  int64  `json:"qty_milli"`
}

func (r receiveRequest) Validate() error {
	var v httpx.V
	v.Check(r.LotNo != "", "lot_no", "zorunlu")
	_, err := time.Parse(time.DateOnly, r.ExpiresOn)
	v.Check(err == nil, "expires_on", "YYYY-AA-GG olmalı")
	v.Check(r.QtyMilli > 0, "qty_milli", "pozitif olmalı")
	return v.Err()
}

// receive books a delivery as a new lot (or tops up the same lot number).
func (s *Service) receive(w http.ResponseWriter, r *http.Request) error {
	supplyID, err := httpx.Int64Param(chi.URLParam(r, "supplyID"), "supplyID")
	if err != nil {
		return err
	}
	var req receiveRequest
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	actor := auth.MustFromContext(r.Context())
	var lotID int64
	err = platform.InTx(r.Context(), s.Pool, func(tx pgx.Tx) error {
		ctx := r.Context()
		var expires time.Time
		err := tx.QueryRow(ctx, `INSERT INTO "tn-supply_lots" (supply_id, lot_no, expires_on, qty_milli) VALUES ($1, $2, $3, $4)
			ON CONFLICT (supply_id, lot_no) DO UPDATE SET qty_milli = "tn-supply_lots".qty_milli + EXCLUDED.qty_milli
			RETURNING id, expires_on`, supplyID, req.LotNo, req.ExpiresOn, req.QtyMilli).Scan(&lotID, &expires)
		if err != nil {
			return err
		}
		if expires.Format(time.DateOnly) != req.ExpiresOn {
			return httpx.Conflict("LOT_EXPIRY_MISMATCH", "aynı lot numarası farklı SKT ile kayıtlı")
		}
		if _, err := tx.Exec(ctx, `INSERT INTO "tn-stock_movements" (supply_id, lot_id, kind, qty_milli, created_by)
			VALUES ($1, $2, 'receive', $3, $4)`, supplyID, lotID, req.QtyMilli, actor.UserID); err != nil {
			return err
		}
		if err := refreshAlert(ctx, tx, supplyID); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{Actor: actor.UserID, Action: "stock.receive", Entity: "supply", EntityID: supplyID, New: req})
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"lot_id": lotID})
	return nil
}

type RecipeLine struct {
	SupplyID int64  `json:"supply_id"`
	SKU      string `json:"sku"`
	Name     string `json:"name"`
	Unit     string `json:"unit"`
	QtyMilli int64  `json:"qty_milli"`
}

func (s *Service) getRecipe(w http.ResponseWriter, r *http.Request) error {
	rows, err := s.Pool.Query(r.Context(), `SELECT r.supply_id, s.sku, s.name, s.unit, r.qty_milli FROM "tn-recipes" r
		JOIN "tn-supplies" s ON s.id = r.supply_id WHERE r.treatment_code = $1 ORDER BY s.name`, chi.URLParam(r, "code"))
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[RecipeLine])
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"treatment_code": chi.URLParam(r, "code"), "items": items})
	return nil
}

type recipeRequest struct {
	Items []struct {
		SupplyID int64 `json:"supply_id"`
		QtyMilli int64 `json:"qty_milli"`
	} `json:"items"`
}

func (r recipeRequest) Validate() error {
	var v httpx.V
	for i, it := range r.Items {
		v.Check(it.SupplyID > 0 && it.QtyMilli > 0, fmt.Sprintf("items[%d]", i), "supply_id ve pozitif qty_milli zorunlu")
	}
	return v.Err()
}

// putRecipe replaces the consumable recipe of a treatment type.
func (s *Service) putRecipe(w http.ResponseWriter, r *http.Request) error {
	code := chi.URLParam(r, "code")
	var req recipeRequest
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	actor := auth.MustFromContext(r.Context())
	err := platform.InTx(r.Context(), s.Pool, func(tx pgx.Tx) error {
		ctx := r.Context()
		if err := tx.QueryRow(ctx, `SELECT code FROM "tn-treatment_types" WHERE code = $1 FOR UPDATE`, code).Scan(&code); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFound("tedavi türü bulunamadı")
			}
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM "tn-recipes" WHERE treatment_code = $1`, code); err != nil {
			return err
		}
		for _, it := range req.Items {
			if _, err := tx.Exec(ctx, `INSERT INTO "tn-recipes" (treatment_code, supply_id, qty_milli) VALUES ($1, $2, $3)`,
				code, it.SupplyID, it.QtyMilli); err != nil {
				return err
			}
		}
		return audit.Write(ctx, tx, audit.Entry{Actor: actor.UserID, Action: "recipe.replace", Entity: "treatment_type", New: map[string]any{"code": code, "items": req.Items}})
	})
	if err != nil {
		return err
	}
	return s.getRecipe(w, r)
}

type Alert struct {
	ID            int64     `json:"id"`
	SupplyID      int64     `json:"supply_id"`
	SKU           string    `json:"sku"`
	Name          string    `json:"name"`
	QtyMilli      int64     `json:"qty_milli"`
	MinStockMilli int64     `json:"min_stock_milli"`
	CreatedAt     time.Time `json:"created_at"`
}

func (s *Service) alerts(w http.ResponseWriter, r *http.Request) error {
	rows, err := s.Pool.Query(r.Context(), `SELECT a.id, a.supply_id, s.sku, s.name, a.qty_milli, s.min_stock_milli, a.created_at
		FROM "tn-stock_alerts" a JOIN "tn-supplies" s ON s.id = a.supply_id WHERE a.resolved_at IS NULL ORDER BY a.created_at DESC`)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Alert])
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
	return nil
}

type Movement struct {
	ID            int64     `json:"id"`
	SKU           string    `json:"sku"`
	LotNo         string    `json:"lot_no"`
	Kind          string    `json:"kind"`
	QtyMilli      int64     `json:"qty_milli"`
	AppointmentID *int64    `json:"appointment_id"`
	CreatedAt     time.Time `json:"created_at"`
}

func (s *Service) movements(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	var apptID int64
	if v := q.Get("appointment_id"); v != "" {
		var err error
		if apptID, err = httpx.Int64Param(v, "appointment_id"); err != nil {
			return err
		}
	}
	rows, err := s.Pool.Query(r.Context(), `SELECT m.id, s.sku, l.lot_no, m.kind, m.qty_milli, m.appointment_id, m.created_at
		FROM "tn-stock_movements" m JOIN "tn-supplies" s ON s.id = m.supply_id JOIN "tn-supply_lots" l ON l.id = m.lot_id
		WHERE ($1 = 0 OR m.appointment_id = $1) ORDER BY m.id DESC LIMIT $2`, apptID, httpx.Limit(r, 100, 500))
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Movement])
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
	return nil
}

func (s *Service) Routes(r chi.Router) {
	view := auth.Require(auth.RoleAssistant, auth.RoleDentist, auth.RoleAccounting)
	manage := auth.Require(auth.RoleAssistant, auth.RoleAccounting)
	r.With(view).Get("/inventory/supplies", httpx.HandlerFunc(s.listSupplies).ServeHTTP)
	r.With(manage).Post("/inventory/supplies", httpx.HandlerFunc(s.createSupply).ServeHTTP)
	r.With(view).Get("/inventory/supplies/{supplyID}/lots", httpx.HandlerFunc(s.listLots).ServeHTTP)
	r.With(manage).Post("/inventory/supplies/{supplyID}/lots", httpx.HandlerFunc(s.receive).ServeHTTP)
	r.With(view).Get("/inventory/recipes/{code}", httpx.HandlerFunc(s.getRecipe).ServeHTTP)
	r.With(manage).Put("/inventory/recipes/{code}", httpx.HandlerFunc(s.putRecipe).ServeHTTP)
	r.With(view).Get("/inventory/alerts", httpx.HandlerFunc(s.alerts).ServeHTTP)
	r.With(view).Get("/inventory/movements", httpx.HandlerFunc(s.movements).ServeHTTP)
}
