package dental

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
	"github.com/sudekazan/ddo_proje/backend/internal/billing"
	"github.com/sudekazan/ddo_proje/backend/internal/httpx"
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

// ---------- Tooth chart & timeline ----------

type ToothState struct {
	Tooth     int        `json:"tooth"`
	State     string     `json:"state"`
	UpdatedAt *time.Time `json:"updated_at"`
}

type ToothEvent struct {
	ID            int64      `json:"id"`
	Tooth         int        `json:"tooth"`
	TreatmentCode string     `json:"treatment_code"`
	TreatmentName string     `json:"treatment_name"`
	StateBefore   string     `json:"state_before"`
	StateAfter    string     `json:"state_after"`
	PerformedAt   time.Time  `json:"performed_at"`
	DentistName   *string    `json:"dentist_name"`
	AppointmentID *int64     `json:"appointment_id"`
	Note          string     `json:"note"`
	RevertedAt    *time.Time `json:"reverted_at"`
}

type querier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Chart returns the current state of the patient's teeth (unrecorded teeth are healthy).
func Chart(ctx context.Context, q querier, patientID int64) (map[int]string, error) {
	rows, err := q.Query(ctx, `SELECT tooth, state FROM "tn-tooth_status" WHERE patient_id = $1`, patientID)
	if err != nil {
		return nil, err
	}
	chart := map[int]string{}
	var tooth int
	var state string
	_, err = pgx.ForEachRow(rows, []any{&tooth, &state}, func() error {
		chart[tooth] = state
		return nil
	})
	return chart, err
}

func (s *Service) chart(w http.ResponseWriter, r *http.Request) error {
	id, err := patientParam(r)
	if err != nil {
		return err
	}
	rows, err := s.Pool.Query(r.Context(), `SELECT tooth, state, updated_at FROM "tn-tooth_status" WHERE patient_id = $1`, id)
	if err != nil {
		return err
	}
	stored, err := pgx.CollectRows(rows, pgx.RowToStructByPos[ToothState])
	if err != nil {
		return err
	}
	byTooth := map[int]ToothState{}
	for _, t := range stored {
		byTooth[t.Tooth] = t
	}
	build := func(teeth []int) []ToothState {
		out := make([]ToothState, 0, len(teeth))
		for _, t := range teeth {
			if st, ok := byTooth[t]; ok {
				out = append(out, st)
			} else {
				out = append(out, ToothState{Tooth: t, State: Healthy})
			}
		}
		return out
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"permanent": build(AllPermanent()), "primary": build(AllPrimary())})
	return nil
}

const eventSelect = `SELECT e.id, e.tooth, e.treatment_code, t.name, e.state_before, e.state_after, e.performed_at,
	d.full_name, e.appointment_id, e.note, e.reverted_at
	FROM "tn-tooth_events" e JOIN "tn-treatment_types" t ON t.code = e.treatment_code
	LEFT JOIN "tn-staff" d ON d.id = e.dentist_id`

// timeline lists tooth events newest first, optionally for one tooth, with keyset pagination.
func (s *Service) timeline(w http.ResponseWriter, r *http.Request) error {
	id, err := patientParam(r)
	if err != nil {
		return err
	}
	tooth := 0
	if v := chi.URLParam(r, "tooth"); v != "" {
		if tooth, err = strconv.Atoi(v); err != nil || !ValidFDI(tooth) {
			return httpx.Unprocessable("INVALID_TOOTH", "geçerli bir FDI diş numarası olmalı (11-48, 51-85)")
		}
	}
	limit := httpx.Limit(r, 50, 200)
	before := int64(1 << 62)
	if c := r.URL.Query().Get("cursor"); c != "" {
		if before, err = strconv.ParseInt(c, 10, 64); err != nil {
			return httpx.BadRequest("cursor geçersiz")
		}
	}
	rows, err := s.Pool.Query(r.Context(), eventSelect+`
		WHERE e.patient_id = $1 AND ($2 = 0 OR e.tooth = $2) AND e.id < $3
		ORDER BY e.performed_at DESC, e.id DESC LIMIT $4`, id, tooth, before, limit+1)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[ToothEvent])
	if err != nil {
		return err
	}
	page := httpx.Page[ToothEvent]{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		page.NextCursor = strconv.FormatInt(page.Items[limit-1].ID, 10)
	}
	httpx.JSON(w, http.StatusOK, page)
	return nil
}

// ApplyProcedure validates and records a procedure on a tooth. finalSession=false records the
// event (e.g. 1st of 3 root canal sessions) without changing the tooth state yet.
func ApplyProcedure(ctx context.Context, tx pgx.Tx, patientID int64, tooth int, code string,
	apptID, dentistID int64, at time.Time, finalSession bool, note string) error {
	if !ValidFDI(tooth) {
		return httpx.Unprocessable("INVALID_TOOTH", "geçerli bir FDI diş numarası olmalı")
	}
	before, err := lockTooth(ctx, tx, patientID, tooth)
	if err != nil {
		return err
	}
	after, err := Apply(code, tooth, before)
	if err != nil {
		return err
	}
	if !finalSession {
		after = before
	}
	if _, err := tx.Exec(ctx, `INSERT INTO "tn-tooth_events" (patient_id, tooth, treatment_code, state_before, state_after,
		performed_at, dentist_id, appointment_id, note) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		patientID, tooth, code, before, after, at, dentistID, apptID, note); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE "tn-tooth_status" SET state = $3, updated_at = $4 WHERE patient_id = $1 AND tooth = $2`,
		patientID, tooth, after, at)
	return err
}

func lockTooth(ctx context.Context, tx pgx.Tx, patientID int64, tooth int) (string, error) {
	if _, err := tx.Exec(ctx, `INSERT INTO "tn-tooth_status" (patient_id, tooth, state) VALUES ($1, $2, 'healthy')
		ON CONFLICT DO NOTHING`, patientID, tooth); err != nil {
		return "", err
	}
	var state string
	err := tx.QueryRow(ctx, `SELECT state FROM "tn-tooth_status" WHERE patient_id = $1 AND tooth = $2 FOR UPDATE`,
		patientID, tooth).Scan(&state)
	return state, err
}

// RevertProcedure undoes the tooth event of an appointment. It refuses when a later procedure
// was recorded on the same tooth, because that one was based on the state being reverted.
func RevertProcedure(ctx context.Context, tx pgx.Tx, apptID int64) error {
	var id, patientID int64
	var tooth int
	var before string
	err := tx.QueryRow(ctx, `SELECT id, patient_id, tooth, state_before FROM "tn-tooth_events"
		WHERE appointment_id = $1 AND reverted_at IS NULL FOR UPDATE`, apptID).Scan(&id, &patientID, &tooth, &before)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := lockTooth(ctx, tx, patientID, tooth); err != nil {
		return err
	}
	var later bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM "tn-tooth_events" WHERE patient_id = $1 AND tooth = $2
		AND id > $3 AND reverted_at IS NULL)`, patientID, tooth, id).Scan(&later); err != nil {
		return err
	}
	if later {
		return httpx.Conflict("LATER_TOOTH_EVENTS", fmt.Sprintf("%d numaralı dişe sonradan işlem yapılmış; önce o geri alınmalı", tooth))
	}
	if _, err := tx.Exec(ctx, `UPDATE "tn-tooth_events" SET reverted_at = now() WHERE id = $1`, id); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE "tn-tooth_status" SET state = $3, updated_at = now() WHERE patient_id = $1 AND tooth = $2`,
		patientID, tooth, before)
	return err
}

type observationRequest struct {
	State string `json:"state"`
	Note  string `json:"note"`
}

// observe records a finding (e.g. tooth missing on first visit) as an EXAM event.
func (s *Service) observe(w http.ResponseWriter, r *http.Request) error {
	id, err := patientParam(r)
	if err != nil {
		return err
	}
	tooth, err := strconv.Atoi(chi.URLParam(r, "tooth"))
	if err != nil || !ValidFDI(tooth) {
		return httpx.Unprocessable("INVALID_TOOTH", "geçerli bir FDI diş numarası olmalı (11-48, 51-85)")
	}
	var req observationRequest
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	valid := false
	for _, st := range ValidStates {
		valid = valid || st == req.State
	}
	if !valid {
		return (&httpx.V{}).ErrField("state", "geçersiz diş durumu")
	}
	actor := auth.MustFromContext(r.Context())
	err = platform.InTx(r.Context(), s.Pool, func(tx pgx.Tx) error {
		before, err := lockTooth(r.Context(), tx, id, tooth)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `INSERT INTO "tn-tooth_events" (patient_id, tooth, treatment_code, state_before, state_after,
			performed_at, dentist_id, note) VALUES ($1, $2, 'EXAM', $3, $4, now(), NULLIF($5, 0), $6)`,
			id, tooth, before, req.State, actor.StaffID, req.Note); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `UPDATE "tn-tooth_status" SET state = $3, updated_at = now() WHERE patient_id = $1 AND tooth = $2`,
			id, tooth, req.State); err != nil {
			return err
		}
		return audit.Write(r.Context(), tx, audit.Entry{Actor: actor.UserID, Action: "tooth.observe", Entity: "patient", EntityID: id,
			Old: map[string]any{"tooth": tooth, "state": before}, New: map[string]any{"tooth": tooth, "state": req.State}})
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusCreated, ToothState{Tooth: tooth, State: req.State})
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

// ---------- Treatment plans ----------

type Plan struct {
	ID             int64         `json:"id"`
	PatientID      int64         `json:"patient_id"`
	PatientName    string        `json:"patient_name"`
	DentistID      int64         `json:"dentist_id"`
	DentistName    string        `json:"dentist_name"`
	Status         string        `json:"status"`
	CurrentVersion int           `json:"current_version"`
	CreatedAt      time.Time     `json:"created_at"`
	Versions       []PlanVersion `json:"versions"`
	Items          []PlanItem    `json:"items"`
}

type PlanVersion struct {
	Version        int        `json:"version"`
	PriceDate      time.Time  `json:"price_date"`
	DiscountBP     int        `json:"discount_bp"`
	GrossKurus     int64      `json:"gross_kurus"`
	DiscountKurus  int64      `json:"discount_kurus"`
	NetKurus       int64      `json:"net_kurus"`
	Note           string     `json:"note"`
	CreatedAt      time.Time  `json:"created_at"`
	ApprovedAt     *time.Time `json:"approved_at"`
	ApprovalMethod *string    `json:"approval_method"`
}

type PlanItem struct {
	ID             int64  `json:"id"`
	Seq            int    `json:"seq"`
	Tooth          *int   `json:"tooth"`
	TreatmentCode  string `json:"treatment_code"`
	TreatmentName  string `json:"treatment_name"`
	DependsOn      *int64 `json:"depends_on"`
	SessionsTotal  int    `json:"sessions_total"`
	SessionsDone   int    `json:"sessions_done"`
	UnitPriceKurus int64  `json:"unit_price_kurus"`
	DiscountKurus  int64  `json:"discount_kurus"`
	NetKurus       int64  `json:"net_kurus"`
	BilledKurus    int64  `json:"billed_kurus"`
	Status         string `json:"status"`
}

func GetPlan(ctx context.Context, q querier, id int64) (Plan, error) {
	var p Plan
	err := q.QueryRow(ctx, `SELECT tp.id, tp.patient_id, pa.first_name || ' ' || pa.last_name, tp.dentist_id, d.full_name,
		tp.status, tp.current_version, tp.created_at
		FROM "tn-treatment_plans" tp JOIN "tn-patients" pa ON pa.id = tp.patient_id JOIN "tn-staff" d ON d.id = tp.dentist_id
		WHERE tp.id = $1`, id).
		Scan(&p.ID, &p.PatientID, &p.PatientName, &p.DentistID, &p.DentistName, &p.Status, &p.CurrentVersion, &p.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, httpx.NotFound("tedavi planı bulunamadı")
	}
	if err != nil {
		return p, err
	}
	rows, err := q.Query(ctx, `SELECT version, price_date, discount_bp, gross_kurus, discount_kurus, net_kurus, note, created_at,
		approved_at, approval_method FROM "tn-plan_versions" WHERE plan_id = $1 ORDER BY version`, id)
	if err != nil {
		return p, err
	}
	if p.Versions, err = pgx.CollectRows(rows, pgx.RowToStructByPos[PlanVersion]); err != nil {
		return p, err
	}
	rows, err = q.Query(ctx, `SELECT pi.id, pi.seq, pi.tooth, pi.treatment_code, t.name, pi.depends_on, pi.sessions_total,
		pi.sessions_done, pi.unit_price_kurus, pi.discount_kurus, pi.net_kurus, pi.billed_kurus, pi.status
		FROM "tn-plan_items" pi JOIN "tn-treatment_types" t ON t.code = pi.treatment_code
		WHERE pi.plan_id = $1 AND pi.version = $2 ORDER BY pi.seq`, id, p.CurrentVersion)
	if err != nil {
		return p, err
	}
	p.Items, err = pgx.CollectRows(rows, pgx.RowToStructByPos[PlanItem])
	return p, err
}

type PlanRequest struct {
	PatientID int64       `json:"patient_id"`
	Items     []ItemInput `json:"items"`
	Note      string      `json:"note"`
}

func (r PlanRequest) Validate() error {
	var v httpx.V
	v.Check(r.Items != nil && len(r.Items) >= 1 && len(r.Items) <= 40, "items", "1-40 kalem olmalı")
	for i, it := range r.Items {
		f := fmt.Sprintf("items[%d]", i)
		v.Check(it.TreatmentCode != "", f+".treatment_code", "zorunlu")
		v.Check(it.Tooth == nil || ValidFDI(*it.Tooth), f+".tooth", "geçerli bir FDI diş numarası olmalı (11-48, 51-85)")
	}
	return v.Err()
}

type ttInfo struct {
	sessions      int
	requiresTooth bool
}

// writeVersion validates the items (FDI, dependencies, chart simulation), prices them at today's
// list price with the patient's institution discount and stores them as a new plan version.
func (s *Service) writeVersion(ctx context.Context, tx pgx.Tx, planID int64, version int, patientID int64,
	items []ItemInput, note string, actorID int64) error {
	types := map[string]ttInfo{}
	rows, err := tx.Query(ctx, `SELECT code, sessions, requires_tooth FROM "tn-treatment_types"`)
	if err != nil {
		return err
	}
	var code string
	var info ttInfo
	if _, err := pgx.ForEachRow(rows, []any{&code, &info.sessions, &info.requiresTooth}, func() error {
		types[code] = info
		return nil
	}); err != nil {
		return err
	}
	var v httpx.V
	for i := range items {
		t, ok := types[items[i].TreatmentCode]
		f := fmt.Sprintf("items[%d]", i)
		v.Check(ok, f+".treatment_code", "tanımsız tedavi türü")
		v.Check(!t.requiresTooth || items[i].Tooth != nil, f+".tooth", "bu tedavi için diş numarası zorunlu")
		if ok && !t.requiresTooth {
			items[i].Tooth = nil
		}
	}
	if err := v.Err(); err != nil {
		return err
	}
	deps, err := ResolveDependencies(items)
	if err != nil {
		return err
	}
	order, err := TopoOrder(deps)
	if err != nil {
		return err
	}
	chart, err := Chart(ctx, tx, patientID)
	if err != nil {
		return err
	}
	if err := SimulateTeeth(items, order, chart); err != nil {
		return err
	}
	bp, err := billing.PatientDiscountBP(ctx, tx, patientID)
	if err != nil {
		return err
	}
	priceDate := s.now().In(s.Loc)
	prices := make([]int64, len(items))
	var gross, discount int64
	for i, it := range items {
		if prices[i], err = billing.PriceAt(ctx, tx, it.TreatmentCode, priceDate); err != nil {
			return err
		}
		gross += prices[i]
		discount += billing.Discount(prices[i], bp)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO "tn-plan_versions" (plan_id, version, price_date, discount_bp, gross_kurus,
		discount_kurus, net_kurus, note, created_by) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		planID, version, priceDate.Format(time.DateOnly), bp, gross, discount, gross-discount, note, actorID); err != nil {
		return err
	}
	ids := make([]int64, len(items))
	for pos, i := range order {
		it := items[i]
		var dep *int64
		if deps[i] >= 0 {
			dep = &ids[deps[i]]
		}
		d := billing.Discount(prices[i], bp)
		if err := tx.QueryRow(ctx, `INSERT INTO "tn-plan_items" (plan_id, version, seq, tooth, treatment_code, depends_on,
			sessions_total, unit_price_kurus, discount_kurus, net_kurus) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id`,
			planID, version, pos+1, it.Tooth, it.TreatmentCode, dep, types[it.TreatmentCode].sessions,
			prices[i], d, prices[i]-d).Scan(&ids[i]); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) CreatePlan(ctx context.Context, actor auth.Principal, req PlanRequest) (Plan, error) {
	var p Plan
	if actor.StaffID == 0 {
		return p, httpx.Forbidden()
	}
	err := platform.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		var id int64
		if err := tx.QueryRow(ctx, `INSERT INTO "tn-treatment_plans" (patient_id, dentist_id) VALUES ($1, $2) RETURNING id`,
			req.PatientID, actor.StaffID).Scan(&id); err != nil {
			return err
		}
		items := append([]ItemInput(nil), req.Items...)
		if err := s.writeVersion(ctx, tx, id, 1, req.PatientID, items, req.Note, actor.UserID); err != nil {
			return err
		}
		var err error
		if p, err = GetPlan(ctx, tx, id); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{Actor: actor.UserID, Action: "plan.create", Entity: "plan", EntityID: id, New: p})
	})
	return p, err
}

// Revise stores a new version and sends the plan back to draft: the patient must approve again.
// Allowed only before any session of the current version was booked or performed.
func (s *Service) Revise(ctx context.Context, actor auth.Principal, planID int64, req PlanRequest) (Plan, error) {
	var p Plan
	err := platform.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		var status string
		var current int
		var patientID int64
		err := tx.QueryRow(ctx, `SELECT status, current_version, patient_id FROM "tn-treatment_plans" WHERE id = $1 FOR UPDATE`, planID).
			Scan(&status, &current, &patientID)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound("tedavi planı bulunamadı")
		}
		if err != nil {
			return err
		}
		if status != "draft" && status != "approved" {
			return httpx.Conflict("PLAN_NOT_REVISABLE", "başlamış, tamamlanmış veya iptal edilmiş plan revize edilemez")
		}
		var sessions int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM "tn-appointments" a JOIN "tn-plan_items" pi ON pi.id = a.plan_item_id
			WHERE pi.plan_id = $1 AND a.status IN ('booked','completed')`, planID).Scan(&sessions); err != nil {
			return err
		}
		if sessions > 0 {
			return httpx.Conflict("PLAN_HAS_SESSIONS", "planın randevulanmış seansları var; önce iptal edilmeli")
		}
		before, err := GetPlan(ctx, tx, planID)
		if err != nil {
			return err
		}
		items := append([]ItemInput(nil), req.Items...)
		if err := s.writeVersion(ctx, tx, planID, current+1, patientID, items, req.Note, actor.UserID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE "tn-treatment_plans" SET current_version = $2, status = 'draft' WHERE id = $1`, planID, current+1); err != nil {
			return err
		}
		if p, err = GetPlan(ctx, tx, planID); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{Actor: actor.UserID, Action: "plan.revise", Entity: "plan", EntityID: planID, Old: before, New: p})
	})
	return p, err
}

type approveRequest struct {
	Version int `json:"version"`
}

// Approve records patient consent for a specific version; approving a stale version fails.
func (s *Service) Approve(ctx context.Context, actor auth.Principal, planID int64, version int) (Plan, error) {
	method := "signed_form"
	if actor.Role == auth.RolePatient {
		method = "patient_portal"
	}
	var p Plan
	err := platform.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		var status string
		var current int
		var patientID int64
		err := tx.QueryRow(ctx, `SELECT status, current_version, patient_id FROM "tn-treatment_plans" WHERE id = $1 FOR UPDATE`, planID).
			Scan(&status, &current, &patientID)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound("tedavi planı bulunamadı")
		}
		if err != nil {
			return err
		}
		if !actor.CanSeePatient(patientID) {
			return httpx.Forbidden()
		}
		if version != current {
			return httpx.Conflict("PLAN_VERSION_MISMATCH", fmt.Sprintf("güncel plan sürümü %d; onay o sürüm için verilmeli", current))
		}
		if status != "draft" {
			return httpx.Conflict("PLAN_NOT_DRAFT", "plan onay bekleyen durumda değil (durum: "+status+")")
		}
		if _, err := tx.Exec(ctx, `UPDATE "tn-plan_versions" SET approved_at = now(), approved_by = $3, approval_method = $4
			WHERE plan_id = $1 AND version = $2`, planID, version, actor.UserID, method); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE "tn-treatment_plans" SET status = 'approved' WHERE id = $1`, planID); err != nil {
			return err
		}
		if p, err = GetPlan(ctx, tx, planID); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{Actor: actor.UserID, Action: "plan.approve", Entity: "plan", EntityID: planID,
			New: map[string]any{"version": version, "method": method}})
	})
	return p, err
}

// ---------- HTTP ----------

func (s *Service) createPlan(w http.ResponseWriter, r *http.Request) error {
	var req PlanRequest
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	if req.PatientID <= 0 {
		return (&httpx.V{}).ErrField("patient_id", "zorunlu")
	}
	p, err := s.CreatePlan(r.Context(), auth.MustFromContext(r.Context()), req)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusCreated, p)
	return nil
}

func (s *Service) revise(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.Int64Param(chi.URLParam(r, "planID"), "planID")
	if err != nil {
		return err
	}
	var req PlanRequest
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	p, err := s.Revise(r.Context(), auth.MustFromContext(r.Context()), id, req)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusCreated, p)
	return nil
}

func (s *Service) approve(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.Int64Param(chi.URLParam(r, "planID"), "planID")
	if err != nil {
		return err
	}
	var req approveRequest
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	p, err := s.Approve(r.Context(), auth.MustFromContext(r.Context()), id, req.Version)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, p)
	return nil
}

func (s *Service) getPlan(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.Int64Param(chi.URLParam(r, "planID"), "planID")
	if err != nil {
		return err
	}
	p, err := GetPlan(r.Context(), s.Pool, id)
	if err != nil {
		return err
	}
	if !auth.MustFromContext(r.Context()).CanSeePatient(p.PatientID) {
		return httpx.Forbidden()
	}
	httpx.JSON(w, http.StatusOK, p)
	return nil
}

func (s *Service) listPlans(w http.ResponseWriter, r *http.Request) error {
	id, err := patientParam(r)
	if err != nil {
		return err
	}
	rows, err := s.Pool.Query(r.Context(), `SELECT id FROM "tn-treatment_plans" WHERE patient_id = $1 ORDER BY id DESC LIMIT 50`, id)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	plans := make([]Plan, 0, len(ids))
	for _, pid := range ids {
		p, err := GetPlan(r.Context(), s.Pool, pid)
		if err != nil {
			return err
		}
		plans = append(plans, p)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": plans})
	return nil
}

func (s *Service) Routes(r chi.Router) {
	dentist := auth.Require(auth.RoleDentist)
	clinical := auth.Require(auth.RoleDentist, auth.RoleAssistant, auth.RoleReception, auth.RolePatient)
	r.With(clinical).Get("/patients/{patientID}/teeth", httpx.HandlerFunc(s.chart).ServeHTTP)
	r.With(clinical).Get("/patients/{patientID}/tooth-events", httpx.HandlerFunc(s.timeline).ServeHTTP)
	r.With(clinical).Get("/patients/{patientID}/teeth/{tooth}/events", httpx.HandlerFunc(s.timeline).ServeHTTP)
	r.With(dentist).Post("/patients/{patientID}/teeth/{tooth}/observations", httpx.HandlerFunc(s.observe).ServeHTTP)

	r.With(dentist).Post("/plans", httpx.HandlerFunc(s.createPlan).ServeHTTP)
	r.With(dentist).Post("/plans/{planID}/revisions", httpx.HandlerFunc(s.revise).ServeHTTP)
	r.With(auth.Require(auth.RolePatient, auth.RoleReception)).Post("/plans/{planID}/approve", httpx.HandlerFunc(s.approve).ServeHTTP)
	r.Get("/plans/{planID}", httpx.HandlerFunc(s.getPlan).ServeHTTP)
	r.Get("/patients/{patientID}/plans", httpx.HandlerFunc(s.listPlans).ServeHTTP)
}
