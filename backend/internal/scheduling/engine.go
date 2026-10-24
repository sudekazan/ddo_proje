// Package scheduling is the conflict-free appointment engine: it books dentist + unit +
// assistant together, honours treatment durations, staff leave and minimum gaps between
// sessions, and promotes the waitlist when an appointment is cancelled.
//
// Correctness under concurrency rests on two layers:
//  1. the dentist row is locked (SELECT … FOR UPDATE) so bookings for one dentist are serialised;
//  2. EXCLUDE USING gist constraints on (resource, during) make overlapping active
//     appointments impossible at the database level, whatever the application does.
package scheduling

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sudekazan/ddo_proje/backend/internal/audit"
	"github.com/sudekazan/ddo_proje/backend/internal/auth"
	"github.com/sudekazan/ddo_proje/backend/internal/dental"
	"github.com/sudekazan/ddo_proje/backend/internal/httpx"
	"github.com/sudekazan/ddo_proje/backend/internal/platform"
)

const (
	OpenHour  = 9
	CloseHour = 18
	SlotStep  = 15 * time.Minute
)

type Engine struct {
	Pool *pgxpool.Pool
	Loc  *time.Location
	Now  func() time.Time
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

type BookRequest struct {
	PatientID     int64     `json:"patient_id"`
	DentistID     int64     `json:"dentist_id"`
	TreatmentCode string    `json:"treatment_code"`
	Start         time.Time `json:"start"`
	UnitID        *int64    `json:"unit_id"`
	AssistantID   *int64    `json:"assistant_id"`
	Tooth         *int      `json:"tooth"`
	PlanItemID    *int64    `json:"plan_item_id"`
}

func (r BookRequest) Validate() error {
	var v httpx.V
	v.Check(r.PatientID > 0, "patient_id", "zorunlu")
	v.Check(r.DentistID > 0, "dentist_id", "zorunlu")
	v.Check(r.TreatmentCode != "" || r.PlanItemID != nil, "treatment_code", "zorunlu")
	v.Check(!r.Start.IsZero(), "start", "zorunlu (RFC3339)")
	v.Check(r.Tooth == nil || dental.ValidFDI(*r.Tooth), "tooth", "geçerli bir FDI diş numarası olmalı (11-48, 51-85)")
	return v.Err()
}

type treatment struct {
	Code          string
	Name          string
	DurationMin   int
	Sessions      int
	MinGapDays    int
	RequiresTooth bool
}

func loadTreatment(ctx context.Context, tx pgx.Tx, code string) (treatment, error) {
	var t treatment
	err := tx.QueryRow(ctx, `SELECT code, name, duration_min, sessions, min_gap_days, requires_tooth
		FROM "tn-treatment_types" WHERE code = $1`, code).
		Scan(&t.Code, &t.Name, &t.DurationMin, &t.Sessions, &t.MinGapDays, &t.RequiresTooth)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, httpx.Unprocessable("UNKNOWN_TREATMENT", "tanımsız tedavi türü: "+code)
	}
	return t, err
}

// CheckWorkingHours: Monday–Saturday, 09:00–18:00 clinic time, starts on a 15 minute grid.
func CheckWorkingHours(start, end time.Time, loc *time.Location) error {
	ls := start.In(loc)
	if ls.Second() != 0 || ls.Nanosecond() != 0 || ls.Minute()%15 != 0 {
		return httpx.Unprocessable("INVALID_START", "randevu başlangıcı 15 dakikalık dilimlere denk gelmeli")
	}
	if ls.Weekday() == time.Sunday {
		return httpx.Unprocessable("OUTSIDE_WORKING_HOURS", "klinik pazar günü kapalı")
	}
	open := time.Date(ls.Year(), ls.Month(), ls.Day(), OpenHour, 0, 0, 0, loc)
	closing := time.Date(ls.Year(), ls.Month(), ls.Day(), CloseHour, 0, 0, 0, loc)
	if start.Before(open) || end.After(closing) {
		return httpx.Unprocessable("OUTSIDE_WORKING_HOURS", "randevu 09:00-18:00 arasında olmalı")
	}
	return nil
}

// DaysBetween counts calendar days in loc between two instants.
func DaysBetween(a, b time.Time, loc *time.Location) int {
	ay, am, ad := a.In(loc).Date()
	by, bm, bd := b.In(loc).Date()
	da := time.Date(ay, am, ad, 0, 0, 0, 0, time.UTC)
	db := time.Date(by, bm, bd, 0, 0, 0, 0, time.UTC)
	return int(db.Sub(da).Hours() / 24)
}

func onLeave(ctx context.Context, tx pgx.Tx, staffID int64, start, end time.Time) (bool, error) {
	var exists bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM "tn-staff_leaves"
		WHERE staff_id = $1 AND during && tstzrange($2, $3))`, staffID, start, end).Scan(&exists)
	return exists, err
}

// AutoAssigned remembers which resources the engine picked, so a lost race on them can be retried.
type AutoAssigned struct{ unit, assistant bool }

// Book creates an appointment. Auto-assigned unit/assistant races are retried transparently.
func (e *Engine) Book(ctx context.Context, actor auth.Principal, req BookRequest) (Appointment, error) {
	if actor.Role == auth.RolePatient && actor.PatientID != req.PatientID {
		return Appointment{}, httpx.Forbidden()
	}
	if actor.Role == auth.RolePatient && req.Start.Before(e.now()) {
		return Appointment{}, httpx.Unprocessable("START_IN_PAST", "geçmiş bir zamana randevu alınamaz")
	}
	var appt Appointment
	for attempt := 0; ; attempt++ {
		var auto AutoAssigned
		err := platform.InTx(ctx, e.Pool, func(tx pgx.Tx) error {
			id, a, err := e.BookTx(ctx, tx, actor.UserID, req)
			auto = a
			if err != nil {
				return err
			}
			if appt, err = getAppointment(ctx, tx, id); err != nil {
				return err
			}
			if err := notify(ctx, tx, appt.PatientID, &appt.ID, "booking",
				fmt.Sprintf("%s tarihli %s randevunuz oluşturuldu.", appt.Start.In(e.Loc).Format("02.01.2006 15:04"), appt.TreatmentName)); err != nil {
				return err
			}
			return audit.Write(ctx, tx, audit.Entry{Actor: actor.UserID, Action: "appointment.book",
				Entity: "appointment", EntityID: id, New: appt})
		})
		lostRace := (auto.unit && platform.ConstraintViolated(err, "appt_no_unit_overlap")) ||
			(auto.assistant && platform.ConstraintViolated(err, "appt_no_assistant_overlap"))
		if lostRace && attempt < 3 {
			continue
		}
		if err != nil {
			e.recordConflict(ctx, actor.UserID, req, err)
		}
		return appt, err
	}
}

// BookTx performs the booking inside tx. It is also used for waitlist promotion, inside a savepoint.
func (e *Engine) BookTx(ctx context.Context, tx pgx.Tx, actorID int64, req BookRequest) (int64, AutoAssigned, error) {
	var auto AutoAssigned
	var sessionNo *int
	if req.PlanItemID != nil {
		item, err := lockPlanItem(ctx, tx, *req.PlanItemID)
		if err != nil {
			return 0, auto, err
		}
		if req.TreatmentCode == "" {
			req.TreatmentCode = item.TreatmentCode
		}
		if req.Tooth == nil {
			req.Tooth = item.Tooth
		}
		if item.TreatmentCode != req.TreatmentCode || !sameTooth(item.Tooth, req.Tooth) {
			return 0, auto, httpx.Unprocessable("PLAN_ITEM_MISMATCH", "randevu tedavi/diş bilgisi plan kalemiyle uyuşmuyor")
		}
		if item.PatientID != req.PatientID {
			return 0, auto, httpx.Unprocessable("PLAN_ITEM_MISMATCH", "plan kalemi başka bir hastaya ait")
		}
		n, err := e.checkPlanSession(ctx, tx, item, req.Start)
		if err != nil {
			return 0, auto, err
		}
		sessionNo = &n
	}

	tt, err := loadTreatment(ctx, tx, req.TreatmentCode)
	if err != nil {
		return 0, auto, err
	}
	end := req.Start.Add(time.Duration(tt.DurationMin) * time.Minute)
	if err := CheckWorkingHours(req.Start, end, e.Loc); err != nil {
		return 0, auto, err
	}
	if tt.RequiresTooth && req.Tooth == nil {
		return 0, auto, httpx.Unprocessable("TOOTH_REQUIRED", tt.Name+" için diş numarası zorunlu")
	}
	if !tt.RequiresTooth {
		req.Tooth = nil
	} else {
		// Reject clinically impossible bookings early (e.g. dolgu on an extracted tooth).
		state := dental.Healthy
		err := tx.QueryRow(ctx, `SELECT state FROM "tn-tooth_status" WHERE patient_id = $1 AND tooth = $2`, req.PatientID, *req.Tooth).Scan(&state)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return 0, auto, err
		}
		if _, err := dental.Apply(tt.Code, *req.Tooth, state); err != nil {
			return 0, auto, err
		}
	}

	// Serialise bookings per dentist.
	var kind string
	var active bool
	err = tx.QueryRow(ctx, `SELECT kind, active FROM "tn-staff" WHERE id = $1 FOR UPDATE`, req.DentistID).Scan(&kind, &active)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (kind != "dentist" || !active)) {
		return 0, auto, httpx.Unprocessable("INVALID_DENTIST", "geçerli ve aktif bir hekim seçilmeli")
	}
	if err != nil {
		return 0, auto, err
	}
	if leave, err := onLeave(ctx, tx, req.DentistID, req.Start, end); err != nil {
		return 0, auto, err
	} else if leave {
		return 0, auto, httpx.Conflict("DENTIST_ON_LEAVE", "hekim bu tarihte izinli")
	}
	// With the dentist row locked this check is race free; the exclusion constraint stays as backstop.
	var dentistBusy bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM "tn-appointments" WHERE dentist_id = $1
		AND status IN ('booked','completed') AND during && tstzrange($2, $3))`, req.DentistID, req.Start, end).Scan(&dentistBusy); err != nil {
		return 0, auto, err
	}
	if dentistBusy {
		return 0, auto, httpx.Conflict("DENTIST_BUSY", "hekimin bu saatte başka bir randevusu var")
	}

	unitID, err := e.pickUnit(ctx, tx, req.UnitID, req.Start, end)
	if err != nil {
		return 0, auto, err
	}
	auto.unit = req.UnitID == nil
	assistantID, err := e.pickAssistant(ctx, tx, req.AssistantID, req.Start, end)
	if err != nil {
		return 0, auto, err
	}
	auto.assistant = req.AssistantID == nil

	var createdBy *int64
	if actorID != 0 {
		createdBy = &actorID
	}
	var id int64
	err = tx.QueryRow(ctx, `
		INSERT INTO "tn-appointments" (patient_id, dentist_id, assistant_id, unit_id, treatment_code, tooth,
		                               plan_item_id, session_no, during, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, tstzrange($9, $10, '[)'), $11) RETURNING id`,
		req.PatientID, req.DentistID, assistantID, unitID, tt.Code, req.Tooth,
		req.PlanItemID, sessionNo, req.Start, end, createdBy).Scan(&id)
	return id, auto, err
}

func sameTooth(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func (e *Engine) pickUnit(ctx context.Context, tx pgx.Tx, want *int64, start, end time.Time) (int64, error) {
	if want != nil {
		var active bool
		err := tx.QueryRow(ctx, `SELECT active FROM "tn-units" WHERE id = $1`, *want).Scan(&active)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && !active) {
			return 0, httpx.Unprocessable("INVALID_UNIT", "geçerli ve aktif bir ünite seçilmeli")
		}
		return *want, err
	}
	var id int64
	err := tx.QueryRow(ctx, `
		SELECT u.id FROM "tn-units" u
		WHERE u.active AND NOT EXISTS (
			SELECT 1 FROM "tn-appointments" a
			WHERE a.unit_id = u.id AND a.status IN ('booked','completed') AND a.during && tstzrange($1, $2))
		ORDER BY u.id LIMIT 1`, start, end).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, httpx.Conflict("NO_UNIT_AVAILABLE", "bu saatte boş ünite yok")
	}
	return id, err
}

func (e *Engine) pickAssistant(ctx context.Context, tx pgx.Tx, want *int64, start, end time.Time) (int64, error) {
	if want != nil {
		var kind string
		var active bool
		err := tx.QueryRow(ctx, `SELECT kind, active FROM "tn-staff" WHERE id = $1`, *want).Scan(&kind, &active)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && (kind != "assistant" || !active)) {
			return 0, httpx.Unprocessable("INVALID_ASSISTANT", "geçerli ve aktif bir asistan seçilmeli")
		}
		if err != nil {
			return 0, err
		}
		if leave, err := onLeave(ctx, tx, *want, start, end); err != nil {
			return 0, err
		} else if leave {
			return 0, httpx.Conflict("ASSISTANT_ON_LEAVE", "asistan bu tarihte izinli")
		}
		return *want, nil
	}
	var id int64
	err := tx.QueryRow(ctx, `
		SELECT s.id FROM "tn-staff" s
		WHERE s.kind = 'assistant' AND s.active
		  AND NOT EXISTS (SELECT 1 FROM "tn-appointments" a
		                  WHERE a.assistant_id = s.id AND a.status IN ('booked','completed') AND a.during && tstzrange($1, $2))
		  AND NOT EXISTS (SELECT 1 FROM "tn-staff_leaves" l WHERE l.staff_id = s.id AND l.during && tstzrange($1, $2))
		ORDER BY s.id LIMIT 1`, start, end).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, httpx.Conflict("NO_ASSISTANT_AVAILABLE", "bu saatte boş asistan yok")
	}
	return id, err
}

type planItem struct {
	ID            int64
	PlanID        int64
	PatientID     int64
	TreatmentCode string
	Tooth         *int
	SessionsTotal int
	Status        string
	DependsOn     *int64
	MinGapDays    int
}

func lockPlanItem(ctx context.Context, tx pgx.Tx, id int64) (planItem, error) {
	var it planItem
	var planStatus string
	var version, current int
	err := tx.QueryRow(ctx, `
		SELECT pi.id, pi.plan_id, tp.patient_id, pi.treatment_code, pi.tooth, pi.sessions_total, pi.status,
		       pi.depends_on, t.min_gap_days, tp.status, pi.version, tp.current_version
		FROM "tn-plan_items" pi
		JOIN "tn-treatment_plans" tp ON tp.id = pi.plan_id
		JOIN "tn-treatment_types" t ON t.code = pi.treatment_code
		WHERE pi.id = $1 FOR UPDATE OF pi`, id).
		Scan(&it.ID, &it.PlanID, &it.PatientID, &it.TreatmentCode, &it.Tooth, &it.SessionsTotal, &it.Status,
			&it.DependsOn, &it.MinGapDays, &planStatus, &version, &current)
	if errors.Is(err, pgx.ErrNoRows) {
		return it, httpx.NotFound("plan kalemi bulunamadı")
	}
	if err != nil {
		return it, err
	}
	switch {
	case planStatus != "approved" && planStatus != "in_progress":
		return it, httpx.Conflict("PLAN_NOT_APPROVED", "tedavi planı hasta tarafından onaylanmadan seans planlanamaz")
	case version != current:
		return it, httpx.Conflict("PLAN_ITEM_OUTDATED", "plan kalemi güncel plan sürümüne ait değil")
	case it.Status == "done" || it.Status == "cancelled":
		return it, httpx.Conflict("PLAN_ITEM_CLOSED", "plan kalemi tamamlanmış veya iptal edilmiş")
	}
	return it, nil
}

type session struct {
	No    int
	Start time.Time
	End   time.Time
}

// checkPlanSession picks the first free session number and enforces the minimum gap to the
// neighbouring sessions and the dependency order (e.g. kanal tedavisi bitmeden kuron olmaz).
func (e *Engine) checkPlanSession(ctx context.Context, tx pgx.Tx, it planItem, start time.Time) (int, error) {
	rows, err := tx.Query(ctx, `SELECT session_no, lower(during), upper(during) FROM "tn-appointments"
		WHERE plan_item_id = $1 AND status IN ('booked','completed') ORDER BY session_no`, it.ID)
	if err != nil {
		return 0, err
	}
	sessions, err := pgx.CollectRows(rows, pgx.RowToStructByPos[session])
	if err != nil {
		return 0, err
	}
	no, err := NextSession(sessions, it.SessionsTotal, it.MinGapDays, start, e.Loc)
	if err != nil {
		return 0, err
	}
	if it.DependsOn != nil {
		if err := checkDependency(ctx, tx, *it.DependsOn, start); err != nil {
			return 0, err
		}
	}
	return no, nil
}

// NextSession returns the session number for a booking at start, given the already booked
// sessions (sorted by number). Sessions must be at least gapDays calendar days apart and in order.
func NextSession(booked []session, total, gapDays int, start time.Time, loc *time.Location) (int, error) {
	no := 1
	for _, s := range booked {
		if s.No != no {
			break
		}
		no++
	}
	if no > total {
		return 0, httpx.Conflict("ALL_SESSIONS_BOOKED", fmt.Sprintf("bu kalemin %d seansının tamamı planlanmış", total))
	}
	for _, s := range booked {
		if s.No < no {
			if d := DaysBetween(s.Start, start, loc); d < gapDays {
				return 0, httpx.Conflict("MIN_GAP_VIOLATION", fmt.Sprintf(
					"%d. seans, %d. seanstan en az %d gün sonra olmalı (en erken %s)", no, s.No, gapDays,
					s.Start.In(loc).AddDate(0, 0, gapDays).Format("02.01.2006")))
			}
		} else if d := DaysBetween(start, s.Start, loc); d < gapDays {
			return 0, httpx.Conflict("MIN_GAP_VIOLATION", fmt.Sprintf(
				"%d. seans, %d. seanstan en az %d gün önce olmalı", no, s.No, gapDays))
		}
	}
	return no, nil
}

func checkDependency(ctx context.Context, tx pgx.Tx, depID int64, start time.Time) error {
	var status, code string
	var total, scheduled int
	var lastEnd *time.Time
	err := tx.QueryRow(ctx, `
		SELECT pi.status, pi.treatment_code, pi.sessions_total,
		       (SELECT count(*) FROM "tn-appointments" a WHERE a.plan_item_id = pi.id AND a.status IN ('booked','completed')),
		       (SELECT max(upper(a.during)) FROM "tn-appointments" a WHERE a.plan_item_id = pi.id AND a.status IN ('booked','completed'))
		FROM "tn-plan_items" pi WHERE pi.id = $1`, depID).Scan(&status, &code, &total, &scheduled, &lastEnd)
	if err != nil {
		return err
	}
	if status == "done" || status == "cancelled" {
		return nil
	}
	if scheduled < total || lastEnd == nil || start.Before(*lastEnd) {
		return httpx.Conflict("DEPENDENCY_NOT_MET",
			fmt.Sprintf("önce bağlı olduğu %s kaleminin tüm seansları planlanmalı ve bu randevu onlardan sonra olmalı", code))
	}
	return nil
}

func notify(ctx context.Context, tx pgx.Tx, patientID int64, apptID *int64, kind, body string) error {
	_, err := tx.Exec(ctx, `INSERT INTO "tn-notifications" (patient_id, appointment_id, kind, body) VALUES ($1, $2, $3, $4)`,
		patientID, apptID, kind, body)
	return err
}

// recordConflict keeps a trace of rejected double bookings (outside the rolled back transaction)
// so the dashboard can show how many conflicts the engine prevented.
func (e *Engine) recordConflict(ctx context.Context, actorID int64, req BookRequest, err error) {
	he := httpx.FromDB(err)
	if he == nil {
		if !errors.As(err, &he) {
			return
		}
	}
	if he.Status != http.StatusConflict {
		return
	}
	_ = pgx.BeginFunc(ctx, e.Pool, func(tx pgx.Tx) error {
		return audit.Write(ctx, tx, audit.Entry{Actor: actorID, Action: "booking.conflict", Entity: "dentist",
			EntityID: req.DentistID, New: map[string]any{"code": he.Code, "start": req.Start, "patient_id": req.PatientID}})
	})
}
