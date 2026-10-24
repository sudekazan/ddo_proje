package scheduling

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sudekazan/ddo_proje/backend/internal/audit"
	"github.com/sudekazan/ddo_proje/backend/internal/auth"
	"github.com/sudekazan/ddo_proje/backend/internal/httpx"
	"github.com/sudekazan/ddo_proje/backend/internal/platform"
)

type Appointment struct {
	ID            int64      `json:"id"`
	PatientID     int64      `json:"patient_id"`
	PatientName   string     `json:"patient_name"`
	DentistID     int64      `json:"dentist_id"`
	DentistName   string     `json:"dentist_name"`
	AssistantID   int64      `json:"assistant_id"`
	AssistantName string     `json:"assistant_name"`
	UnitID        int64      `json:"unit_id"`
	UnitName      string     `json:"unit_name"`
	TreatmentCode string     `json:"treatment_code"`
	TreatmentName string     `json:"treatment_name"`
	Tooth         *int       `json:"tooth"`
	PlanItemID    *int64     `json:"plan_item_id"`
	SessionNo     *int       `json:"session_no"`
	Start         time.Time  `json:"start"`
	End           time.Time  `json:"end"`
	Status        string     `json:"status"`
	CancelReason  *string    `json:"cancel_reason"`
	CreatedAt     time.Time  `json:"created_at"`
	CompletedAt   *time.Time `json:"completed_at"`
}

const apptSelect = `
	SELECT a.id, a.patient_id, p.first_name || ' ' || p.last_name, a.dentist_id, d.full_name,
	       a.assistant_id, s.full_name, a.unit_id, u.name, a.treatment_code, t.name, a.tooth,
	       a.plan_item_id, a.session_no, lower(a.during), upper(a.during), a.status, a.cancel_reason,
	       a.created_at, a.completed_at
	FROM "tn-appointments" a
	JOIN "tn-patients" p ON p.id = a.patient_id
	JOIN "tn-staff" d ON d.id = a.dentist_id
	JOIN "tn-staff" s ON s.id = a.assistant_id
	JOIN "tn-units" u ON u.id = a.unit_id
	JOIN "tn-treatment_types" t ON t.code = a.treatment_code`

func scanAppointment(row pgx.Row) (Appointment, error) {
	var a Appointment
	err := row.Scan(&a.ID, &a.PatientID, &a.PatientName, &a.DentistID, &a.DentistName, &a.AssistantID,
		&a.AssistantName, &a.UnitID, &a.UnitName, &a.TreatmentCode, &a.TreatmentName, &a.Tooth,
		&a.PlanItemID, &a.SessionNo, &a.Start, &a.End, &a.Status, &a.CancelReason, &a.CreatedAt, &a.CompletedAt)
	return a, err
}

type querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func getAppointment(ctx context.Context, q querier, id int64) (Appointment, error) {
	a, err := scanAppointment(q.QueryRow(ctx, apptSelect+` WHERE a.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return a, httpx.NotFound("randevu bulunamadı")
	}
	return a, err
}

// Get returns an appointment visible to actor.
func (e *Engine) Get(ctx context.Context, actor auth.Principal, id int64) (Appointment, error) {
	a, err := getAppointment(ctx, e.Pool, id)
	if err != nil {
		return a, err
	}
	if !actor.CanSeePatient(a.PatientID) {
		return Appointment{}, httpx.Forbidden()
	}
	return a, nil
}

type ListFilter struct {
	From      *time.Time
	To        *time.Time
	DentistID int64
	PatientID int64
	Status    string
	Desc      bool
	Cursor    string
	Limit     int
}

// List uses keyset pagination on (start, id).
func (e *Engine) List(ctx context.Context, f ListFilter) (httpx.Page[Appointment], error) {
	var page httpx.Page[Appointment]
	where := []string{"true"}
	args := []any{}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(cond, "?", "$"+strconv.Itoa(len(args))))
	}
	if f.From != nil {
		add("lower(a.during) >= ?", *f.From)
	}
	if f.To != nil {
		add("lower(a.during) < ?", *f.To)
	}
	if f.DentistID != 0 {
		add("a.dentist_id = ?", f.DentistID)
	}
	if f.PatientID != 0 {
		add("a.patient_id = ?", f.PatientID)
	}
	if f.Status != "" {
		add("a.status = ?", f.Status)
	}
	op, dir := ">", "ASC"
	if f.Desc {
		op, dir = "<", "DESC"
	}
	if f.Cursor != "" {
		ts, id, ok := strings.Cut(f.Cursor, "_")
		nanos, err1 := strconv.ParseInt(ts, 10, 64)
		cid, err2 := strconv.ParseInt(id, 10, 64)
		if !ok || err1 != nil || err2 != nil {
			return page, httpx.BadRequest("cursor geçersiz")
		}
		args = append(args, time.Unix(0, nanos), cid)
		where = append(where, fmt.Sprintf("(lower(a.during), a.id) %s ($%d, $%d)", op, len(args)-1, len(args)))
	}
	args = append(args, f.Limit+1)
	rows, err := e.Pool.Query(ctx, apptSelect+" WHERE "+strings.Join(where, " AND ")+
		fmt.Sprintf(" ORDER BY lower(a.during) %s, a.id %s LIMIT $%d", dir, dir, len(args)), args...)
	if err != nil {
		return page, err
	}
	items, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Appointment, error) { return scanAppointment(r) })
	if err != nil {
		return page, err
	}
	page.Items = items
	if len(items) > f.Limit {
		page.Items = items[:f.Limit]
		last := page.Items[f.Limit-1]
		page.NextCursor = fmt.Sprintf("%d_%d", last.Start.UnixNano(), last.ID)
	}
	return page, nil
}

type CancelResult struct {
	Appointment Appointment  `json:"appointment"`
	Promoted    *Appointment `json:"waitlist_appointment"`
}

// Cancel frees the slot and, in the same transaction, offers it to the first matching
// waitlist entry (FOR UPDATE SKIP LOCKED so concurrent cancellations never pick the same entry).
func (e *Engine) Cancel(ctx context.Context, actor auth.Principal, id int64, reason string) (CancelResult, error) {
	var res CancelResult
	err := platform.InTx(ctx, e.Pool, func(tx pgx.Tx) error {
		res = CancelResult{}
		var patientID int64
		var status string
		err := tx.QueryRow(ctx, `SELECT patient_id, status FROM "tn-appointments" WHERE id = $1 FOR UPDATE`, id).Scan(&patientID, &status)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound("randevu bulunamadı")
		}
		if err != nil {
			return err
		}
		if !actor.CanSeePatient(patientID) {
			return httpx.Forbidden()
		}
		if status != "booked" {
			return httpx.Conflict("NOT_CANCELLABLE", "yalnızca planlanmış randevular iptal edilebilir (durum: "+status+")")
		}
		before, err := getAppointment(ctx, tx, id)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE "tn-appointments" SET status = 'cancelled', cancel_reason = $2 WHERE id = $1`, id, reason); err != nil {
			return err
		}
		if res.Appointment, err = getAppointment(ctx, tx, id); err != nil {
			return err
		}
		if err := notify(ctx, tx, patientID, &id, "cancel",
			fmt.Sprintf("%s tarihli randevunuz iptal edildi.", before.Start.In(e.Loc).Format("02.01.2006 15:04"))); err != nil {
			return err
		}
		if err := audit.Write(ctx, tx, audit.Entry{Actor: actor.UserID, Action: "appointment.cancel",
			Entity: "appointment", EntityID: id, Old: before, New: res.Appointment}); err != nil {
			return err
		}
		if before.Start.Before(e.now()) {
			return nil
		}
		res.Promoted, err = e.promoteWaitlist(ctx, tx, actor.UserID, before)
		return err
	})
	return res, err
}

type waitCandidate struct {
	ID            int64
	PatientID     int64
	TreatmentCode string
	Tooth         *int
}

func (e *Engine) promoteWaitlist(ctx context.Context, tx pgx.Tx, actorID int64, freed Appointment) (*Appointment, error) {
	rows, err := tx.Query(ctx, `
		SELECT w.id, w.patient_id, w.treatment_code, w.tooth
		FROM "tn-waitlist" w JOIN "tn-treatment_types" t ON t.code = w.treatment_code
		WHERE w.status = 'waiting'
		  AND (w.dentist_id IS NULL OR w.dentist_id = $1)
		  AND $2::timestamptz + make_interval(mins => t.duration_min) <= $3::timestamptz
		  AND w.avail_window @> tstzrange($2::timestamptz, $2::timestamptz + make_interval(mins => t.duration_min))
		ORDER BY w.created_at, w.id
		LIMIT 5
		FOR UPDATE OF w SKIP LOCKED`, freed.DentistID, freed.Start, freed.End)
	if err != nil {
		return nil, err
	}
	cands, err := pgx.CollectRows(rows, pgx.RowToStructByPos[waitCandidate])
	if err != nil {
		return nil, err
	}
	for _, c := range cands {
		sp, err := tx.Begin(ctx) // savepoint: a failed candidate must not abort the cancellation
		if err != nil {
			return nil, err
		}
		unit, assistant := freed.UnitID, freed.AssistantID
		id, _, err := e.BookTx(ctx, sp, actorID, BookRequest{
			PatientID: c.PatientID, DentistID: freed.DentistID, TreatmentCode: c.TreatmentCode,
			Start: freed.Start, UnitID: &unit, AssistantID: &assistant, Tooth: c.Tooth,
		})
		if err != nil {
			if rbErr := sp.Rollback(ctx); rbErr != nil {
				return nil, rbErr
			}
			continue
		}
		if err := sp.Commit(ctx); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE "tn-waitlist" SET status = 'booked', appointment_id = $2 WHERE id = $1`, c.ID, id); err != nil {
			return nil, err
		}
		appt, err := getAppointment(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if err := notify(ctx, tx, c.PatientID, &id, "waitlist",
			fmt.Sprintf("Bekleme listesindeki talebiniz için %s tarihine randevunuz oluşturuldu.", appt.Start.In(e.Loc).Format("02.01.2006 15:04"))); err != nil {
			return nil, err
		}
		if err := audit.Write(ctx, tx, audit.Entry{Actor: actorID, Action: "waitlist.promote",
			Entity: "appointment", EntityID: id, New: map[string]any{"waitlist_id": c.ID, "appointment": appt}}); err != nil {
			return nil, err
		}
		return &appt, nil
	}
	return nil, nil
}

type WaitlistEntry struct {
	ID            int64     `json:"id"`
	PatientID     int64     `json:"patient_id"`
	PatientName   string    `json:"patient_name"`
	TreatmentCode string    `json:"treatment_code"`
	DentistID     *int64    `json:"dentist_id"`
	Tooth         *int      `json:"tooth"`
	From          time.Time `json:"from"`
	To            time.Time `json:"to"`
	Status        string    `json:"status"`
	AppointmentID *int64    `json:"appointment_id"`
	CreatedAt     time.Time `json:"created_at"`
}

type WaitlistRequest struct {
	PatientID     int64     `json:"patient_id"`
	TreatmentCode string    `json:"treatment_code"`
	DentistID     *int64    `json:"dentist_id"`
	Tooth         *int      `json:"tooth"`
	From          time.Time `json:"from"`
	To            time.Time `json:"to"`
}

func (r WaitlistRequest) Validate() error {
	var v httpx.V
	v.Check(r.PatientID > 0, "patient_id", "zorunlu")
	v.Check(r.TreatmentCode != "", "treatment_code", "zorunlu")
	v.Check(!r.From.IsZero(), "from", "zorunlu")
	v.Check(r.To.After(r.From), "to", "from'dan sonra olmalı")
	return v.Err()
}

const waitlistSelect = `SELECT w.id, w.patient_id, p.first_name || ' ' || p.last_name, w.treatment_code, w.dentist_id, w.tooth,
	lower(w.avail_window), upper(w.avail_window), w.status, w.appointment_id, w.created_at
	FROM "tn-waitlist" w JOIN "tn-patients" p ON p.id = w.patient_id`

func (e *Engine) AddWaitlist(ctx context.Context, actor auth.Principal, req WaitlistRequest) (WaitlistEntry, error) {
	var out WaitlistEntry
	if !actor.CanSeePatient(req.PatientID) {
		return out, httpx.Forbidden()
	}
	err := platform.InTx(ctx, e.Pool, func(tx pgx.Tx) error {
		tt, err := loadTreatment(ctx, tx, req.TreatmentCode)
		if err != nil {
			return err
		}
		if tt.RequiresTooth && req.Tooth == nil {
			return httpx.Unprocessable("TOOTH_REQUIRED", tt.Name+" için diş numarası zorunlu")
		}
		var id int64
		if err := tx.QueryRow(ctx, `INSERT INTO "tn-waitlist" (patient_id, treatment_code, dentist_id, tooth, avail_window)
			VALUES ($1, $2, $3, $4, tstzrange($5, $6)) RETURNING id`,
			req.PatientID, req.TreatmentCode, req.DentistID, req.Tooth, req.From, req.To).Scan(&id); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, waitlistSelect+` WHERE w.id = $1`, id)
		if err != nil {
			return err
		}
		if out, err = pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[WaitlistEntry]); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{Actor: actor.UserID, Action: "waitlist.add", Entity: "waitlist", EntityID: id, New: out})
	})
	return out, err
}

func (e *Engine) ListWaitlist(ctx context.Context, status string) ([]WaitlistEntry, error) {
	rows, err := e.Pool.Query(ctx, waitlistSelect+` WHERE ($1 = '' OR w.status = $1) ORDER BY w.created_at, w.id LIMIT 200`, status)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[WaitlistEntry])
}

func (e *Engine) CancelWaitlist(ctx context.Context, actor auth.Principal, id int64) error {
	return platform.InTx(ctx, e.Pool, func(tx pgx.Tx) error {
		var patientID int64
		var status string
		err := tx.QueryRow(ctx, `SELECT patient_id, status FROM "tn-waitlist" WHERE id = $1 FOR UPDATE`, id).Scan(&patientID, &status)
		if err != nil {
			return err
		}
		if !actor.CanSeePatient(patientID) {
			return httpx.Forbidden()
		}
		if status != "waiting" {
			return httpx.Conflict("NOT_CANCELLABLE", "kayıt bekleme durumunda değil")
		}
		if _, err := tx.Exec(ctx, `UPDATE "tn-waitlist" SET status = 'cancelled' WHERE id = $1`, id); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{Actor: actor.UserID, Action: "waitlist.cancel", Entity: "waitlist", EntityID: id})
	})
}
