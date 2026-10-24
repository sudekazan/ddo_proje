package billing

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
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

type querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// PriceAt returns the list price valid on date.
func PriceAt(ctx context.Context, q querier, code string, date time.Time) (int64, error) {
	var price int64
	err := q.QueryRow(ctx, `SELECT price_kurus FROM "tn-price_list" WHERE treatment_code = $1 AND valid_during @> $2::date`,
		code, date.Format(time.DateOnly)).Scan(&price)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, httpx.Unprocessable("NO_PRICE", fmt.Sprintf("%s için %s tarihinde geçerli fiyat yok", code, date.Format("02.01.2006")))
	}
	return price, err
}

// PatientDiscountBP returns the contracted institution discount of a patient (0 if none).
func PatientDiscountBP(ctx context.Context, q querier, patientID int64) (int, error) {
	var bp int
	err := q.QueryRow(ctx, `SELECT COALESCE(i.discount_bp, 0) FROM "tn-patients" p
		LEFT JOIN "tn-institutions" i ON i.id = p.institution_id WHERE p.id = $1`, patientID).Scan(&bp)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, httpx.NotFound("hasta bulunamadı")
	}
	return bp, err
}

type Price struct {
	ID            int64      `json:"id"`
	TreatmentCode string     `json:"treatment_code"`
	TreatmentName string     `json:"treatment_name"`
	PriceKurus    int64      `json:"price_kurus"`
	ValidFrom     time.Time  `json:"valid_from"`
	ValidTo       *time.Time `json:"valid_to"`
}

func (s *Service) listPrices(w http.ResponseWriter, r *http.Request) error {
	date := time.Now().In(s.Loc)
	if v := r.URL.Query().Get("date"); v != "" {
		d, err := time.ParseInLocation(time.DateOnly, v, s.Loc)
		if err != nil {
			return httpx.BadRequest("date YYYY-AA-GG olmalı")
		}
		date = d
	}
	all := r.URL.Query().Get("history") == "true"
	rows, err := s.Pool.Query(r.Context(), `
		SELECT p.id, p.treatment_code, t.name, p.price_kurus, lower(p.valid_during), upper(p.valid_during)
		FROM "tn-price_list" p JOIN "tn-treatment_types" t ON t.code = p.treatment_code
		WHERE $2 OR p.valid_during @> $1::date
		ORDER BY p.treatment_code, lower(p.valid_during)`, date.Format(time.DateOnly), all)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Price])
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"date": date.Format(time.DateOnly), "items": items})
	return nil
}

type newPriceRequest struct {
	TreatmentCode string `json:"treatment_code"`
	PriceKurus    int64  `json:"price_kurus"`
	ValidFrom     string `json:"valid_from"`
}

func (r newPriceRequest) Validate() error {
	var v httpx.V
	v.Check(r.TreatmentCode != "", "treatment_code", "zorunlu")
	v.Check(r.PriceKurus > 0, "price_kurus", "pozitif olmalı")
	_, err := time.Parse(time.DateOnly, r.ValidFrom)
	v.Check(err == nil, "valid_from", "YYYY-AA-GG olmalı")
	return v.Err()
}

// createPrice closes the open-ended price at valid_from and starts a new one there.
// Plans keep the price of their own price_date, so a future price never changes them.
func (s *Service) createPrice(w http.ResponseWriter, r *http.Request) error {
	var req newPriceRequest
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	actor := auth.MustFromContext(r.Context())
	var id int64
	err := platform.InTx(r.Context(), s.Pool, func(tx pgx.Tx) error {
		ctx := r.Context()
		if _, err := tx.Exec(ctx, `SELECT 1 FROM "tn-treatment_types" WHERE code = $1 FOR UPDATE`, req.TreatmentCode); err != nil {
			return err
		}
		var openID int64
		var openFrom time.Time
		err := tx.QueryRow(ctx, `SELECT id, lower(valid_during) FROM "tn-price_list"
			WHERE treatment_code = $1 AND upper_inf(valid_during)`, req.TreatmentCode).Scan(&openID, &openFrom)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return err
		case openFrom.Format(time.DateOnly) >= req.ValidFrom:
			return httpx.Conflict("PRICE_OVERLAP", "yeni fiyat, geçerli fiyatın başlangıcından sonra başlamalı")
		default:
			if _, err := tx.Exec(ctx, `UPDATE "tn-price_list" SET valid_during = daterange(lower(valid_during), $2::date) WHERE id = $1`,
				openID, req.ValidFrom); err != nil {
				return err
			}
		}
		if err := tx.QueryRow(ctx, `INSERT INTO "tn-price_list" (treatment_code, price_kurus, valid_during)
			VALUES ($1, $2, daterange($3::date, NULL)) RETURNING id`, req.TreatmentCode, req.PriceKurus, req.ValidFrom).Scan(&id); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{Actor: actor.UserID, Action: "price.create", Entity: "price", EntityID: id, New: req})
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"id": id})
	return nil
}

type PaymentRequest struct {
	PatientID   int64  `json:"patient_id"`
	PlanID      *int64 `json:"plan_id"`
	AmountKurus int64  `json:"amount_kurus"`
	Method      string `json:"method"`
}

func (r PaymentRequest) Validate() error {
	var v httpx.V
	v.Check(r.PatientID > 0, "patient_id", "zorunlu")
	v.Check(r.AmountKurus > 0 && r.AmountKurus <= 100_000_000_00, "amount_kurus", "pozitif ve makul bir tutar olmalı")
	v.Check(r.Method == "cash" || r.Method == "card" || r.Method == "transfer", "method", "cash, card veya transfer olmalı")
	return v.Err()
}

type Payment struct {
	ID            int64     `json:"id"`
	PatientID     int64     `json:"patient_id"`
	PlanID        *int64    `json:"plan_id"`
	AmountKurus   int64     `json:"amount_kurus"`
	Method        string    `json:"method"`
	ReceivedAt    time.Time `json:"received_at"`
	LedgerEntryID int64     `json:"ledger_entry_id"`
	BalanceAfter  int64     `json:"balance_after"`
	Allocations   []Alloc   `json:"installment_allocations"`
}

type Alloc struct {
	InstallmentID int64 `json:"installment_id"`
	Seq           int   `json:"seq"`
	AmountKurus   int64 `json:"amount_kurus"`
}

// Pay records a payment, allocates it to the plan's open installments in order (peşinat first)
// and posts it to the ledger — all in one transaction.
func (s *Service) Pay(ctx context.Context, actor auth.Principal, req PaymentRequest) (Payment, error) {
	var p Payment
	err := platform.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		p = Payment{PatientID: req.PatientID, PlanID: req.PlanID, AmountKurus: req.AmountKurus, Method: req.Method}
		if req.PlanID != nil {
			var owner int64
			var status string
			err := tx.QueryRow(ctx, `SELECT patient_id, status FROM "tn-treatment_plans" WHERE id = $1 FOR UPDATE`, *req.PlanID).Scan(&owner, &status)
			if errors.Is(err, pgx.ErrNoRows) || (err == nil && owner != req.PatientID) {
				return httpx.Unprocessable("INVALID_PLAN", "plan bu hastaya ait değil")
			}
			if err != nil {
				return err
			}
			if status == "cancelled" {
				return httpx.Conflict("PLAN_CANCELLED", "iptal edilmiş plana ödeme alınamaz")
			}
		}
		if err := tx.QueryRow(ctx, `INSERT INTO "tn-payments" (patient_id, plan_id, amount_kurus, method, created_by)
			VALUES ($1, $2, $3, $4, $5) RETURNING id, received_at`, req.PatientID, req.PlanID, req.AmountKurus, req.Method, actor.UserID).
			Scan(&p.ID, &p.ReceivedAt); err != nil {
			return err
		}
		if req.PlanID != nil {
			allocs, err := allocate(ctx, tx, *req.PlanID, req.AmountKurus)
			if err != nil {
				return err
			}
			p.Allocations = allocs
		}
		id, err := Post(ctx, tx, Entry{PatientID: req.PatientID, Kind: "payment", Amount: -req.AmountKurus,
			PlanID: req.PlanID, PaymentID: &p.ID, Description: "Ödeme (" + req.Method + ")", CreatedBy: actor.UserID})
		if err != nil {
			return err
		}
		p.LedgerEntryID = id
		if err := tx.QueryRow(ctx, `SELECT balance_after FROM "tn-ledger_entries" WHERE id = $1`, id).Scan(&p.BalanceAfter); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{Actor: actor.UserID, Action: "payment.create", Entity: "payment", EntityID: p.ID, New: p})
	})
	return p, err
}

func allocate(ctx context.Context, tx pgx.Tx, planID, amount int64) ([]Alloc, error) {
	rows, err := tx.Query(ctx, `SELECT id, seq, amount_kurus - paid_kurus FROM "tn-installments"
		WHERE plan_id = $1 AND status = 'open' ORDER BY seq FOR UPDATE`, planID)
	if err != nil {
		return nil, err
	}
	type open struct {
		ID   int64
		Seq  int
		Owed int64
	}
	opens, err := pgx.CollectRows(rows, pgx.RowToStructByPos[open])
	if err != nil {
		return nil, err
	}
	allocs := []Alloc{}
	for _, o := range opens {
		if amount == 0 {
			break
		}
		take := min(o.Owed, amount)
		amount -= take
		if _, err := tx.Exec(ctx, `UPDATE "tn-installments" SET paid_kurus = paid_kurus + $2,
			status = CASE WHEN paid_kurus + $2 = amount_kurus THEN 'paid' ELSE status END WHERE id = $1`, o.ID, take); err != nil {
			return nil, err
		}
		allocs = append(allocs, Alloc{InstallmentID: o.ID, Seq: o.Seq, AmountKurus: take})
	}
	return allocs, nil
}

type InstallmentPlanRequest struct {
	DownPaymentKurus int64  `json:"down_payment_kurus"`
	Count            int    `json:"count"`
	FirstDueDate     string `json:"first_due_date"`
}

func (r InstallmentPlanRequest) Validate() error {
	var v httpx.V
	v.Check(r.DownPaymentKurus >= 0, "down_payment_kurus", "negatif olamaz")
	v.Check(r.Count >= 1 && r.Count <= 36, "count", "1-36 arası olmalı")
	_, err := time.Parse(time.DateOnly, r.FirstDueDate)
	v.Check(err == nil, "first_due_date", "YYYY-AA-GG olmalı")
	return v.Err()
}

type Installment struct {
	ID          int64     `json:"id"`
	Seq         int       `json:"seq"`
	DueDate     time.Time `json:"due_date"`
	AmountKurus int64     `json:"amount_kurus"`
	PaidKurus   int64     `json:"paid_kurus"`
	Status      string    `json:"status"`
}

// CreateInstallments splits the plan total (minus what was already paid towards it) into a
// down payment (seq 0, due today) and Count monthly installments that sum exactly.
func (s *Service) CreateInstallments(ctx context.Context, actor auth.Principal, planID int64, req InstallmentPlanRequest) ([]Installment, error) {
	var out []Installment
	err := platform.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		var status string
		var net int64
		err := tx.QueryRow(ctx, `SELECT tp.status, pv.net_kurus FROM "tn-treatment_plans" tp
			JOIN "tn-plan_versions" pv ON pv.plan_id = tp.id AND pv.version = tp.current_version
			WHERE tp.id = $1 FOR UPDATE OF tp`, planID).Scan(&status, &net)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound("plan bulunamadı")
		}
		if err != nil {
			return err
		}
		if status != "approved" && status != "in_progress" {
			return httpx.Conflict("PLAN_NOT_APPROVED", "taksit planı yalnızca onaylı planlar için oluşturulabilir")
		}
		var existing int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM "tn-installments" WHERE plan_id = $1 AND status <> 'cancelled'`, planID).Scan(&existing); err != nil {
			return err
		}
		if existing > 0 {
			return httpx.Conflict("INSTALLMENTS_EXIST", "bu plan için zaten taksit planı var")
		}
		var paid int64
		if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(amount_kurus), 0) FROM "tn-payments" WHERE plan_id = $1`, planID).Scan(&paid); err != nil {
			return err
		}
		remaining := net - paid
		if req.DownPaymentKurus > remaining {
			return httpx.Unprocessable("DOWN_PAYMENT_TOO_LARGE", "peşinat kalan tutardan büyük olamaz")
		}
		first, _ := time.ParseInLocation(time.DateOnly, req.FirstDueDate, s.Loc)
		seq := 0
		insert := func(due time.Time, amount int64) error {
			if amount <= 0 {
				return nil
			}
			_, err := tx.Exec(ctx, `INSERT INTO "tn-installments" (plan_id, seq, due_date, amount_kurus) VALUES ($1, $2, $3, $4)`,
				planID, seq, due.Format(time.DateOnly), amount)
			return err
		}
		if err := insert(time.Now().In(s.Loc), req.DownPaymentKurus); err != nil {
			return err
		}
		for i, part := range Split(remaining-req.DownPaymentKurus, req.Count) {
			seq = i + 1
			if err := insert(first.AddDate(0, i, 0), part); err != nil {
				return err
			}
		}
		if out, err = listInstallments(ctx, tx, planID); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{Actor: actor.UserID, Action: "installments.create", Entity: "plan", EntityID: planID, New: out})
	})
	return out, err
}

func listInstallments(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, planID int64) ([]Installment, error) {
	rows, err := q.Query(ctx, `SELECT id, seq, due_date, amount_kurus, paid_kurus, status FROM "tn-installments"
		WHERE plan_id = $1 ORDER BY seq`, planID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Installment])
}

type Account struct {
	PatientID        int64         `json:"patient_id"`
	BalanceKurus     int64         `json:"balance_kurus"`
	ChargedKurus     int64         `json:"charged_kurus"`
	PaidKurus        int64         `json:"paid_kurus"`
	RefundedKurus    int64         `json:"refunded_kurus"`
	OpenInstallments []Installment `json:"open_installments"`
}

func (s *Service) account(w http.ResponseWriter, r *http.Request) error {
	id, err := patientParam(r)
	if err != nil {
		return err
	}
	a := Account{PatientID: id}
	err = s.Pool.QueryRow(r.Context(), `
		SELECT COALESCE((SELECT balance_kurus FROM "tn-patient_accounts" WHERE patient_id = $1), 0),
		       COALESCE(sum(amount_kurus) FILTER (WHERE kind IN ('charge','charge_reversal')), 0),
		       COALESCE(-sum(amount_kurus) FILTER (WHERE kind = 'payment'), 0),
		       COALESCE(sum(amount_kurus) FILTER (WHERE kind = 'refund'), 0)
		FROM "tn-ledger_entries" WHERE patient_id = $1`, id).Scan(&a.BalanceKurus, &a.ChargedKurus, &a.PaidKurus, &a.RefundedKurus)
	if err != nil {
		return err
	}
	rows, err := s.Pool.Query(r.Context(), `SELECT i.id, i.seq, i.due_date, i.amount_kurus, i.paid_kurus, i.status
		FROM "tn-installments" i JOIN "tn-treatment_plans" tp ON tp.id = i.plan_id
		WHERE tp.patient_id = $1 AND i.status = 'open' ORDER BY i.due_date, i.id`, id)
	if err != nil {
		return err
	}
	if a.OpenInstallments, err = pgx.CollectRows(rows, pgx.RowToStructByPos[Installment]); err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, a)
	return nil
}

// ledger lists entries newest first with keyset pagination on id.
func (s *Service) ledger(w http.ResponseWriter, r *http.Request) error {
	id, err := patientParam(r)
	if err != nil {
		return err
	}
	limit := httpx.Limit(r, 50, 200)
	before := int64(1 << 62)
	if c := r.URL.Query().Get("cursor"); c != "" {
		if before, err = strconv.ParseInt(c, 10, 64); err != nil {
			return httpx.BadRequest("cursor geçersiz")
		}
	}
	rows, err := s.Pool.Query(r.Context(), `SELECT id, kind, amount_kurus, balance_after, plan_id, appointment_id, payment_id, description, created_at
		FROM "tn-ledger_entries" WHERE patient_id = $1 AND id < $2 ORDER BY id DESC LIMIT $3`, id, before, limit+1)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[LedgerEntry])
	if err != nil {
		return err
	}
	page := httpx.Page[LedgerEntry]{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		page.NextCursor = strconv.FormatInt(page.Items[limit-1].ID, 10)
	}
	httpx.JSON(w, http.StatusOK, page)
	return nil
}

func patientParam(r *http.Request) (int64, error) {
	id, err := httpx.Int64Param(chi.URLParam(r, "patientID"), "patientID")
	if err != nil {
		return 0, err
	}
	if !auth.MustFromContext(r.Context()).CanSeePatient(id) {
		return 0, httpx.Forbidden()
	}
	return id, nil
}

func (s *Service) pay(w http.ResponseWriter, r *http.Request) error {
	var req PaymentRequest
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	p, err := s.Pay(r.Context(), auth.MustFromContext(r.Context()), req)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusCreated, p)
	return nil
}

func (s *Service) createInstallments(w http.ResponseWriter, r *http.Request) error {
	planID, err := httpx.Int64Param(chi.URLParam(r, "planID"), "planID")
	if err != nil {
		return err
	}
	var req InstallmentPlanRequest
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	out, err := s.CreateInstallments(r.Context(), auth.MustFromContext(r.Context()), planID, req)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"items": out})
	return nil
}

func (s *Service) getInstallments(w http.ResponseWriter, r *http.Request) error {
	planID, err := httpx.Int64Param(chi.URLParam(r, "planID"), "planID")
	if err != nil {
		return err
	}
	var owner int64
	if err := s.Pool.QueryRow(r.Context(), `SELECT patient_id FROM "tn-treatment_plans" WHERE id = $1`, planID).Scan(&owner); err != nil {
		return err
	}
	if !auth.MustFromContext(r.Context()).CanSeePatient(owner) {
		return httpx.Forbidden()
	}
	out, err := listInstallments(r.Context(), s.Pool, planID)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
	return nil
}

func (s *Service) consistency(w http.ResponseWriter, r *http.Request) error {
	c, err := CheckConsistency(r.Context(), s.Pool)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, c)
	return nil
}

func (s *Service) Routes(r chi.Router) {
	accounting := auth.Require(auth.RoleAccounting)
	finance := auth.Require(auth.RoleAccounting, auth.RoleReception, auth.RolePatient)
	r.With(auth.Require(auth.Staff...)).Get("/prices", httpx.HandlerFunc(s.listPrices).ServeHTTP)
	r.With(accounting).Post("/prices", httpx.HandlerFunc(s.createPrice).ServeHTTP)
	r.With(accounting).Post("/payments", httpx.HandlerFunc(s.pay).ServeHTTP)
	r.With(finance).Get("/patients/{patientID}/account", httpx.HandlerFunc(s.account).ServeHTTP)
	r.With(finance).Get("/patients/{patientID}/ledger", httpx.HandlerFunc(s.ledger).ServeHTTP)
	r.With(accounting).Post("/plans/{planID}/installments", httpx.HandlerFunc(s.createInstallments).ServeHTTP)
	r.With(finance).Get("/plans/{planID}/installments", httpx.HandlerFunc(s.getInstallments).ServeHTTP)
	r.With(accounting).Get("/billing/consistency", httpx.HandlerFunc(s.consistency).ServeHTTP)
}
