// Package workflow orchestrates the operations that span several modules in one transaction:
// completing an appointment (tooth chart + stock + plan progress + ledger), reverting a wrong
// completion, and cancelling a treatment plan with refund.
package workflow

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
	"github.com/sudekazan/ddo_proje/backend/internal/billing"
	"github.com/sudekazan/ddo_proje/backend/internal/dental"
	"github.com/sudekazan/ddo_proje/backend/internal/httpx"
	"github.com/sudekazan/ddo_proje/backend/internal/inventory"
	"github.com/sudekazan/ddo_proje/backend/internal/platform"
)

type Service struct {
	Pool *pgxpool.Pool
	Loc  *time.Location
	Now  func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

type appt struct {
	ID            int64
	PatientID     int64
	DentistID     int64
	TreatmentCode string
	Tooth         *int
	PlanItemID    *int64
	SessionNo     *int
	Start         time.Time
	Status        string
}

func lockAppointment(ctx context.Context, tx pgx.Tx, id int64) (appt, error) {
	var a appt
	err := tx.QueryRow(ctx, `SELECT id, patient_id, dentist_id, treatment_code, tooth, plan_item_id, session_no, lower(during), status
		FROM "tn-appointments" WHERE id = $1 FOR UPDATE`, id).
		Scan(&a.ID, &a.PatientID, &a.DentistID, &a.TreatmentCode, &a.Tooth, &a.PlanItemID, &a.SessionNo, &a.Start, &a.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, httpx.NotFound("randevu bulunamadı")
	}
	return a, err
}

type item struct {
	PlanID        int64
	SessionsTotal int
	SessionsDone  int
	NetKurus      int64
	DependsOn     *int64
}

func lockItem(ctx context.Context, tx pgx.Tx, id int64) (item, error) {
	var it item
	err := tx.QueryRow(ctx, `SELECT plan_id, sessions_total, sessions_done, net_kurus, depends_on FROM "tn-plan_items" WHERE id = $1 FOR UPDATE`, id).
		Scan(&it.PlanID, &it.SessionsTotal, &it.SessionsDone, &it.NetKurus, &it.DependsOn)
	return it, err
}

type CompleteResult struct {
	AppointmentID int64  `json:"appointment_id"`
	Status        string `json:"status"`
	ChargedKurus  int64  `json:"charged_kurus"`
	BalanceAfter  int64  `json:"balance_after"`
	ToothState    string `json:"tooth_state,omitempty"`
}

// Complete marks an appointment done. In one transaction it validates the dependency order,
// applies the procedure to the tooth, consumes stock (FEFO), advances the plan item and posts
// the progress charge (session share of the item, or list price for unplanned work).
func (s *Service) Complete(ctx context.Context, actor auth.Principal, id int64, note string) (CompleteResult, error) {
	var res CompleteResult
	err := platform.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		res = CompleteResult{AppointmentID: id}
		a, err := lockAppointment(ctx, tx, id)
		if err != nil {
			return err
		}
		if a.Status != "booked" {
			return httpx.Conflict("NOT_COMPLETABLE", "yalnızca planlanmış randevular tamamlanabilir (durum: "+a.Status+")")
		}
		if actor.Role == auth.RoleDentist && actor.StaffID != a.DentistID {
			return httpx.E(http.StatusForbidden, "FORBIDDEN", "yalnızca randevunun hekimi tamamlayabilir")
		}
		today := s.now().In(s.Loc)

		final := true
		var it item
		if a.PlanItemID != nil {
			if it, err = lockItem(ctx, tx, *a.PlanItemID); err != nil {
				return err
			}
			if it.DependsOn != nil {
				var depStatus, depCode string
				if err := tx.QueryRow(ctx, `SELECT status, treatment_code FROM "tn-plan_items" WHERE id = $1`, *it.DependsOn).Scan(&depStatus, &depCode); err != nil {
					return err
				}
				if depStatus != "done" && depStatus != "cancelled" {
					return httpx.Conflict("DEPENDENCY_NOT_MET", "önce bağlı olunan "+depCode+" kalemi tamamlanmalı")
				}
			}
			if *a.SessionNo != it.SessionsDone+1 {
				return httpx.Conflict("SESSION_ORDER", fmt.Sprintf("seanslar sırayla tamamlanmalı; sıradaki seans %d", it.SessionsDone+1))
			}
			final = *a.SessionNo == it.SessionsTotal
		}

		if a.Tooth != nil {
			if err := dental.ApplyProcedure(ctx, tx, a.PatientID, *a.Tooth, a.TreatmentCode, a.ID, a.DentistID, s.now(), final, note); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `SELECT state FROM "tn-tooth_status" WHERE patient_id = $1 AND tooth = $2`, a.PatientID, *a.Tooth).Scan(&res.ToothState); err != nil {
				return err
			}
		}
		if err := inventory.Consume(ctx, tx, a.ID, a.TreatmentCode, today, actor.UserID); err != nil {
			return err
		}

		entry := billing.Entry{PatientID: a.PatientID, Kind: "charge", AppointmentID: &a.ID, CreatedBy: actor.UserID}
		if a.PlanItemID != nil {
			entry.Amount = billing.SessionShare(it.NetKurus, it.SessionsTotal, *a.SessionNo)
			entry.PlanID, entry.PlanItemID = &it.PlanID, a.PlanItemID
			entry.Description = fmt.Sprintf("%s — seans %d/%d", a.TreatmentCode, *a.SessionNo, it.SessionsTotal)
			if _, err := tx.Exec(ctx, `UPDATE "tn-plan_items" SET sessions_done = sessions_done + 1, billed_kurus = billed_kurus + $2,
				status = CASE WHEN sessions_done + 1 = sessions_total THEN 'done' ELSE 'in_progress' END WHERE id = $1`,
				*a.PlanItemID, entry.Amount); err != nil {
				return err
			}
			if err := refreshPlanStatus(ctx, tx, it.PlanID); err != nil {
				return err
			}
		} else {
			price, err := billing.PriceAt(ctx, tx, a.TreatmentCode, a.Start.In(s.Loc))
			if err != nil {
				return err
			}
			bp, err := billing.PatientDiscountBP(ctx, tx, a.PatientID)
			if err != nil {
				return err
			}
			entry.Amount = price - billing.Discount(price, bp)
			entry.Description = a.TreatmentCode + " (plan dışı)"
		}
		if entry.Amount > 0 {
			if _, err := billing.Post(ctx, tx, entry); err != nil {
				return err
			}
			res.ChargedKurus = entry.Amount
		}
		if _, err := tx.Exec(ctx, `UPDATE "tn-appointments" SET status = 'completed', completed_at = now() WHERE id = $1`, a.ID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT balance_kurus FROM "tn-patient_accounts" WHERE patient_id = $1), 0)`, a.PatientID).Scan(&res.BalanceAfter); err != nil {
			return err
		}
		res.Status = "completed"
		return audit.Write(ctx, tx, audit.Entry{Actor: actor.UserID, Action: "appointment.complete", Entity: "appointment",
			EntityID: a.ID, Old: map[string]any{"status": "booked"}, New: res})
	})
	return res, err
}

// Revert undoes a wrong completion: stock goes back to the same lots, the tooth returns to its
// previous state, the charge is reversed and the appointment is booked again.
func (s *Service) Revert(ctx context.Context, actor auth.Principal, id int64, reason string) (CompleteResult, error) {
	var res CompleteResult
	err := platform.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		res = CompleteResult{AppointmentID: id}
		a, err := lockAppointment(ctx, tx, id)
		if err != nil {
			return err
		}
		if a.Status != "completed" {
			return httpx.Conflict("NOT_COMPLETED", "yalnızca tamamlanmış randevu geri alınabilir")
		}
		if actor.Role == auth.RoleDentist && actor.StaffID != a.DentistID {
			return httpx.E(http.StatusForbidden, "FORBIDDEN", "yalnızca randevunun hekimi geri alabilir")
		}
		var it item
		if a.PlanItemID != nil {
			if it, err = lockItem(ctx, tx, *a.PlanItemID); err != nil {
				return err
			}
			if *a.SessionNo != it.SessionsDone {
				return httpx.Conflict("SESSION_ORDER", "yalnızca son tamamlanan seans geri alınabilir")
			}
		}
		if err := dental.RevertProcedure(ctx, tx, a.ID); err != nil {
			return err
		}
		if err := inventory.Return(ctx, tx, a.ID, actor.UserID); err != nil {
			return err
		}
		var chargeID, amount int64
		err = tx.QueryRow(ctx, `SELECT l.id, l.amount_kurus FROM "tn-ledger_entries" l WHERE l.appointment_id = $1 AND l.kind = 'charge'
			AND NOT EXISTS (SELECT 1 FROM "tn-ledger_entries" r WHERE r.reverses_id = l.id) ORDER BY l.id DESC LIMIT 1`, a.ID).Scan(&chargeID, &amount)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return err
		default:
			e := billing.Entry{PatientID: a.PatientID, Kind: "charge_reversal", Amount: -amount, AppointmentID: &a.ID,
				PlanItemID: a.PlanItemID, ReversesID: &chargeID, Description: "Tamamlama geri alındı: " + reason, CreatedBy: actor.UserID}
			if a.PlanItemID != nil {
				e.PlanID = &it.PlanID
			}
			if _, err := billing.Post(ctx, tx, e); err != nil {
				return err
			}
			res.ChargedKurus = -amount
		}
		if a.PlanItemID != nil {
			if _, err := tx.Exec(ctx, `UPDATE "tn-plan_items" SET sessions_done = sessions_done - 1, billed_kurus = billed_kurus - $2,
				status = CASE WHEN sessions_done - 1 = 0 THEN 'pending' ELSE 'in_progress' END WHERE id = $1`, *a.PlanItemID, amount); err != nil {
				return err
			}
			if err := refreshPlanStatus(ctx, tx, it.PlanID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE "tn-appointments" SET status = 'booked', completed_at = NULL WHERE id = $1`, a.ID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT balance_kurus FROM "tn-patient_accounts" WHERE patient_id = $1), 0)`, a.PatientID).Scan(&res.BalanceAfter); err != nil {
			return err
		}
		res.Status = "booked"
		return audit.Write(ctx, tx, audit.Entry{Actor: actor.UserID, Action: "appointment.revert", Entity: "appointment",
			EntityID: a.ID, Old: map[string]any{"status": "completed"}, New: map[string]any{"status": "booked", "reason": reason}})
	})
	return res, err
}

func refreshPlanStatus(ctx context.Context, tx pgx.Tx, planID int64) error {
	_, err := tx.Exec(ctx, `
		UPDATE "tn-treatment_plans" tp SET status = CASE
			WHEN NOT EXISTS (SELECT 1 FROM "tn-plan_items" pi WHERE pi.plan_id = tp.id AND pi.version = tp.current_version
			                 AND pi.status NOT IN ('done','cancelled')) THEN 'completed'
			WHEN EXISTS (SELECT 1 FROM "tn-plan_items" pi WHERE pi.plan_id = tp.id AND pi.version = tp.current_version
			             AND pi.sessions_done > 0) THEN 'in_progress'
			ELSE 'approved' END
		WHERE tp.id = $1 AND tp.status IN ('approved','in_progress','completed')`, planID)
	return err
}

type CancelPlanResult struct {
	PlanID                int64   `json:"plan_id"`
	PaidKurus             int64   `json:"paid_kurus"`
	BilledKurus           int64   `json:"billed_kurus"`
	RefundKurus           int64   `json:"refund_kurus"`
	CancelledAppointments []int64 `json:"cancelled_appointments"`
	BalanceAfter          int64   `json:"balance_after"`
}

// CancelPlan stops a plan: future sessions and open installments are cancelled, pending items
// are closed, and whatever was paid beyond the work already billed is refunded.
func (s *Service) CancelPlan(ctx context.Context, actor auth.Principal, planID int64, reason string) (CancelPlanResult, error) {
	var res CancelPlanResult
	err := platform.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		res = CancelPlanResult{PlanID: planID, CancelledAppointments: []int64{}}
		var patientID int64
		var status string
		err := tx.QueryRow(ctx, `SELECT patient_id, status FROM "tn-treatment_plans" WHERE id = $1 FOR UPDATE`, planID).Scan(&patientID, &status)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound("tedavi planı bulunamadı")
		}
		if err != nil {
			return err
		}
		if status == "cancelled" || status == "completed" {
			return httpx.Conflict("PLAN_CLOSED", "plan zaten "+status)
		}
		rows, err := tx.Query(ctx, `UPDATE "tn-appointments" a SET status = 'cancelled', cancel_reason = 'Plan iptali'
			FROM "tn-plan_items" pi WHERE pi.id = a.plan_item_id AND pi.plan_id = $1 AND a.status = 'booked' RETURNING a.id`, planID)
		if err != nil {
			return err
		}
		if res.CancelledAppointments, err = pgx.CollectRows(rows, pgx.RowTo[int64]); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE "tn-plan_items" SET status = 'cancelled' WHERE plan_id = $1 AND status IN ('pending','in_progress')`, planID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE "tn-installments" SET status = 'cancelled' WHERE plan_id = $1 AND status = 'open'`, planID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT sum(amount_kurus) FROM "tn-payments" WHERE plan_id = $1), 0)
			- COALESCE((SELECT sum(amount_kurus) FROM "tn-refunds" WHERE plan_id = $1), 0),
			COALESCE((SELECT sum(billed_kurus) FROM "tn-plan_items" WHERE plan_id = $1), 0)`, planID).Scan(&res.PaidKurus, &res.BilledKurus); err != nil {
			return err
		}
		res.RefundKurus = billing.RefundOnCancel(res.PaidKurus, res.BilledKurus)
		if res.RefundKurus > 0 {
			var refundID int64
			if err := tx.QueryRow(ctx, `INSERT INTO "tn-refunds" (patient_id, plan_id, amount_kurus, created_by) VALUES ($1, $2, $3, $4) RETURNING id`,
				patientID, planID, res.RefundKurus, actor.UserID).Scan(&refundID); err != nil {
				return err
			}
			if _, err := billing.Post(ctx, tx, billing.Entry{PatientID: patientID, Kind: "refund", Amount: res.RefundKurus,
				PlanID: &planID, RefundID: &refundID, Description: "Plan iptali iadesi", CreatedBy: actor.UserID}); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE "tn-treatment_plans" SET status = 'cancelled', cancelled_at = now() WHERE id = $1`, planID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT balance_kurus FROM "tn-patient_accounts" WHERE patient_id = $1), 0)`, patientID).Scan(&res.BalanceAfter); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{Actor: actor.UserID, Action: "plan.cancel", Entity: "plan", EntityID: planID,
			Old: map[string]any{"status": status}, New: map[string]any{"status": "cancelled", "reason": reason, "result": res}})
	})
	return res, err
}

type reasonRequest struct {
	Reason string `json:"reason"`
	Note   string `json:"note"`
}

func (s *Service) complete(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.Int64Param(chi.URLParam(r, "id"), "id")
	if err != nil {
		return err
	}
	var req reasonRequest
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	res, err := s.Complete(r.Context(), auth.MustFromContext(r.Context()), id, req.Note)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, res)
	return nil
}

func (s *Service) revert(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.Int64Param(chi.URLParam(r, "id"), "id")
	if err != nil {
		return err
	}
	var req reasonRequest
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	if req.Reason == "" {
		return (&httpx.V{}).ErrField("reason", "geri alma gerekçesi zorunlu")
	}
	res, err := s.Revert(r.Context(), auth.MustFromContext(r.Context()), id, req.Reason)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, res)
	return nil
}

func (s *Service) cancelPlan(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.Int64Param(chi.URLParam(r, "planID"), "planID")
	if err != nil {
		return err
	}
	var req reasonRequest
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	res, err := s.CancelPlan(r.Context(), auth.MustFromContext(r.Context()), id, req.Reason)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, res)
	return nil
}

func (s *Service) Routes(r chi.Router) {
	r.With(auth.Require(auth.RoleDentist, auth.RoleAssistant)).Post("/appointments/{id}/complete", httpx.HandlerFunc(s.complete).ServeHTTP)
	r.With(auth.Require(auth.RoleDentist)).Post("/appointments/{id}/revert", httpx.HandlerFunc(s.revert).ServeHTTP)
	r.With(auth.Require(auth.RoleDentist, auth.RoleAccounting)).Post("/plans/{planID}/cancel", httpx.HandlerFunc(s.cancelPlan).ServeHTTP)
}
