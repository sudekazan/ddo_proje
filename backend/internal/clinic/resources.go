package clinic

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/sudekazan/ddo_proje/backend/internal/audit"
	"github.com/sudekazan/ddo_proje/backend/internal/auth"
	"github.com/sudekazan/ddo_proje/backend/internal/httpx"
	"github.com/sudekazan/ddo_proje/backend/internal/platform"
)

type StaffMember struct {
	ID       int64  `json:"id"`
	FullName string `json:"full_name"`
	Kind     string `json:"kind"`
	Active   bool   `json:"active"`
}

type Unit struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Active bool   `json:"active"`
}

type Leave struct {
	ID      int64     `json:"id"`
	StaffID int64     `json:"staff_id"`
	Start   time.Time `json:"start"`
	End     time.Time `json:"end"`
	Reason  string    `json:"reason"`
}

type TreatmentType struct {
	Code          string `json:"code"`
	Name          string `json:"name"`
	DurationMin   int    `json:"duration_min"`
	Sessions      int    `json:"sessions"`
	MinGapDays    int    `json:"min_gap_days"`
	RequiresTooth bool   `json:"requires_tooth"`
}

type Institution struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	DiscountBP int    `json:"discount_bp"`
}

func (h *Handler) listStaff(w http.ResponseWriter, r *http.Request) error {
	kind := r.URL.Query().Get("kind")
	rows, err := h.Pool.Query(r.Context(), `SELECT id, full_name, kind, active FROM "tn-staff"
		WHERE ($1 = '' OR kind = $1) ORDER BY kind, id`, kind)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[StaffMember])
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
	return nil
}

func (h *Handler) listUnits(w http.ResponseWriter, r *http.Request) error {
	rows, err := h.Pool.Query(r.Context(), `SELECT id, name, active FROM "tn-units" ORDER BY id`)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Unit])
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
	return nil
}

type unitRequest struct {
	Name   *string `json:"name"`
	Active *bool   `json:"active"`
}

func (h *Handler) createUnit(w http.ResponseWriter, r *http.Request) error {
	var req unitRequest
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	if req.Name == nil || strings.TrimSpace(*req.Name) == "" {
		return (&httpx.V{}).ErrField("name", "zorunlu")
	}
	actor := auth.MustFromContext(r.Context())
	var u Unit
	err := platform.InTx(r.Context(), h.Pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(r.Context(), `INSERT INTO "tn-units" (name) VALUES ($1) RETURNING id, name, active`,
			strings.TrimSpace(*req.Name)).Scan(&u.ID, &u.Name, &u.Active)
		if err != nil {
			return err
		}
		return audit.Write(r.Context(), tx, audit.Entry{Actor: actor.UserID, Action: "unit.create", Entity: "unit", EntityID: u.ID, New: u})
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusCreated, u)
	return nil
}

func (h *Handler) updateUnit(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.Int64Param(chi.URLParam(r, "unitID"), "unitID")
	if err != nil {
		return err
	}
	var req unitRequest
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	actor := auth.MustFromContext(r.Context())
	var before, after Unit
	err = platform.InTx(r.Context(), h.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(r.Context(), `SELECT id, name, active FROM "tn-units" WHERE id = $1 FOR UPDATE`, id).
			Scan(&before.ID, &before.Name, &before.Active); err != nil {
			return err
		}
		if err := tx.QueryRow(r.Context(), `UPDATE "tn-units" SET name = COALESCE($2, name), active = COALESCE($3, active)
			WHERE id = $1 RETURNING id, name, active`, id, req.Name, req.Active).Scan(&after.ID, &after.Name, &after.Active); err != nil {
			return err
		}
		return audit.Write(r.Context(), tx, audit.Entry{Actor: actor.UserID, Action: "unit.update", Entity: "unit", EntityID: id, Old: before, New: after})
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, after)
	return nil
}

func (h *Handler) listLeaves(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.Int64Param(chi.URLParam(r, "staffID"), "staffID")
	if err != nil {
		return err
	}
	rows, err := h.Pool.Query(r.Context(), `SELECT id, staff_id, lower(during), upper(during), reason
		FROM "tn-staff_leaves" WHERE staff_id = $1 AND upper(during) > now() - interval '30 days' ORDER BY lower(during)`, id)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Leave])
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
	return nil
}

type leaveRequest struct {
	Start  time.Time `json:"start"`
	End    time.Time `json:"end"`
	Reason string    `json:"reason"`
}

func (r leaveRequest) Validate() error {
	var v httpx.V
	v.Check(!r.Start.IsZero(), "start", "zorunlu (RFC3339)")
	v.Check(r.End.After(r.Start), "end", "başlangıçtan sonra olmalı")
	v.Check(r.End.Sub(r.Start) <= 90*24*time.Hour, "end", "izin en fazla 90 gün olabilir")
	return v.Err()
}

// createLeave records staff leave. Existing bookings in the interval are reported back so
// reception can reschedule them; new bookings in the interval are rejected by the engine.
func (h *Handler) createLeave(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.Int64Param(chi.URLParam(r, "staffID"), "staffID")
	if err != nil {
		return err
	}
	var req leaveRequest
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	actor := auth.MustFromContext(r.Context())
	var leave Leave
	var affected []int64
	err = platform.InTx(r.Context(), h.Pool, func(tx pgx.Tx) error {
		// Lock the staff row: the booking engine takes the same lock, so a booking cannot
		// slip in between this check and the insert.
		if _, err := tx.Exec(r.Context(), `SELECT 1 FROM "tn-staff" WHERE id = $1 FOR UPDATE`, id); err != nil {
			return err
		}
		err := tx.QueryRow(r.Context(), `INSERT INTO "tn-staff_leaves" (staff_id, during, reason)
			VALUES ($1, tstzrange($2, $3), $4) RETURNING id, staff_id, lower(during), upper(during), reason`,
			id, req.Start, req.End, req.Reason).Scan(&leave.ID, &leave.StaffID, &leave.Start, &leave.End, &leave.Reason)
		if err != nil {
			return err
		}
		rows, err := tx.Query(r.Context(), `SELECT id FROM "tn-appointments"
			WHERE (dentist_id = $1 OR assistant_id = $1) AND status = 'booked' AND during && tstzrange($2, $3) ORDER BY id`,
			id, req.Start, req.End)
		if err != nil {
			return err
		}
		if affected, err = pgx.CollectRows(rows, pgx.RowTo[int64]); err != nil {
			return err
		}
		return audit.Write(r.Context(), tx, audit.Entry{Actor: actor.UserID, Action: "staff_leave.create", Entity: "staff", EntityID: id, New: leave})
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"leave": leave, "conflicting_appointment_ids": affected})
	return nil
}

func (h *Handler) listTreatmentTypes(w http.ResponseWriter, r *http.Request) error {
	rows, err := h.Pool.Query(r.Context(), `SELECT code, name, duration_min, sessions, min_gap_days, requires_tooth
		FROM "tn-treatment_types" ORDER BY code`)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[TreatmentType])
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
	return nil
}

func (h *Handler) listInstitutions(w http.ResponseWriter, r *http.Request) error {
	rows, err := h.Pool.Query(r.Context(), `SELECT id, name, discount_bp FROM "tn-institutions" ORDER BY name`)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Institution])
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
	return nil
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) error {
	p := auth.MustFromContext(r.Context())
	var email, name string
	if err := h.Pool.QueryRow(r.Context(), `SELECT email, full_name FROM "tn-dk-users" WHERE id = $1`, p.UserID).Scan(&email, &name); err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, auth.UserInfo{Principal: p, Email: email, FullName: name})
	return nil
}

// Routes mounts the clinic endpoints; r is already authenticated.
func (h *Handler) Routes(r chi.Router) {
	staff := auth.Require(auth.Staff...)
	reception := auth.Require(auth.RoleReception)
	r.Get("/me", httpx.HandlerFunc(h.me).ServeHTTP)
	r.Get("/treatment-types", httpx.HandlerFunc(h.listTreatmentTypes).ServeHTTP)

	r.With(staff).Get("/patients", httpx.HandlerFunc(h.listPatients).ServeHTTP)
	r.With(reception).Post("/patients", httpx.HandlerFunc(h.createPatient).ServeHTTP)
	r.Get("/patients/{patientID}", httpx.HandlerFunc(h.getPatient).ServeHTTP)
	r.With(reception).Patch("/patients/{patientID}", httpx.HandlerFunc(h.updatePatient).ServeHTTP)

	r.With(staff).Get("/staff", httpx.HandlerFunc(h.listStaff).ServeHTTP)
	r.With(staff).Get("/staff/{staffID}/leaves", httpx.HandlerFunc(h.listLeaves).ServeHTTP)
	r.With(reception).Post("/staff/{staffID}/leaves", httpx.HandlerFunc(h.createLeave).ServeHTTP)
	r.With(staff).Get("/units", httpx.HandlerFunc(h.listUnits).ServeHTTP)
	r.With(reception).Post("/units", httpx.HandlerFunc(h.createUnit).ServeHTTP)
	r.With(reception).Patch("/units/{unitID}", httpx.HandlerFunc(h.updateUnit).ServeHTTP)
	r.With(staff).Get("/institutions", httpx.HandlerFunc(h.listInstitutions).ServeHTTP)
}
