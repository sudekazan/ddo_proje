// Package reports serves read-only operational and financial reports, plus simulated SMS
// notifications (appointment reminders).
package reports

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sudekazan/ddo_proje/backend/internal/auth"
	"github.com/sudekazan/ddo_proje/backend/internal/httpx"
)

type Service struct {
	Pool *pgxpool.Pool
	Loc  *time.Location
}

// dateRange parses ?from=&to= (inclusive dates); defaults to the last 30 days.
func (s *Service) dateRange(r *http.Request) (time.Time, time.Time, error) {
	now := time.Now().In(s.Loc)
	to := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, s.Loc).AddDate(0, 0, 1)
	from := to.AddDate(0, 0, -30)
	var err error
	if v := r.URL.Query().Get("from"); v != "" {
		if from, err = time.ParseInLocation(time.DateOnly, v, s.Loc); err != nil {
			return from, to, httpx.BadRequest("from YYYY-AA-GG olmalı")
		}
	}
	if v := r.URL.Query().Get("to"); v != "" {
		t, err := time.ParseInLocation(time.DateOnly, v, s.Loc)
		if err != nil {
			return from, to, httpx.BadRequest("to YYYY-AA-GG olmalı")
		}
		to = t.AddDate(0, 0, 1)
	}
	if !to.After(from) || to.Sub(from) > 3*366*24*time.Hour {
		return from, to, httpx.BadRequest("tarih aralığı geçersiz (en fazla 3 yıl)")
	}
	return from, to, nil
}

func collect[T any](ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) ([]T, error) {
	rows, err := pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[T])
}

type Activity struct {
	ID       int64     `json:"id"`
	At       time.Time `json:"at"`
	Action   string    `json:"action"`
	Entity   string    `json:"entity"`
	EntityID string    `json:"entity_id"`
	Actor    *string   `json:"actor"`
}

type Dashboard struct {
	ServerTime         time.Time        `json:"server_time"`
	TodayByStatus      map[string]int64 `json:"today_by_status"`
	UpcomingBooked     int64            `json:"upcoming_booked"`
	BookedLastHour     int64            `json:"booked_last_hour"`
	WaitlistWaiting    int64            `json:"waitlist_waiting"`
	OpenStockAlerts    int64            `json:"open_stock_alerts"`
	TotalPatients      int64            `json:"total_patients"`
	ConflictsPrevented int64            `json:"conflicts_prevented_last_hour"`
	RecentActivity     []Activity       `json:"recent_activity"`
}

// dashboard is polled by the UI every few seconds to show that the system is live.
func (s *Service) dashboard(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	now := time.Now().In(s.Loc)
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, s.Loc)
	d := Dashboard{ServerTime: now, TodayByStatus: map[string]int64{}}
	rows, err := s.Pool.Query(ctx, `SELECT status, count(*) FROM "tn-appointments"
		WHERE lower(during) >= $1 AND lower(during) < $2 GROUP BY status`, dayStart, dayStart.AddDate(0, 0, 1))
	if err != nil {
		return err
	}
	var st string
	var n int64
	if _, err := pgx.ForEachRow(rows, []any{&st, &n}, func() error { d.TodayByStatus[st] = n; return nil }); err != nil {
		return err
	}
	err = s.Pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM "tn-appointments" WHERE status = 'booked' AND lower(during) >= now()),
		(SELECT count(*) FROM "tn-appointments" WHERE created_at >= now() - interval '1 hour'),
		(SELECT count(*) FROM "tn-waitlist" WHERE status = 'waiting'),
		(SELECT count(*) FROM "tn-stock_alerts" WHERE resolved_at IS NULL),
		(SELECT count(*) FROM "tn-patients"),
		(SELECT count(*) FROM "tn-audit_log" WHERE action = 'booking.conflict' AND at >= now() - interval '1 hour')`).
		Scan(&d.UpcomingBooked, &d.BookedLastHour, &d.WaitlistWaiting, &d.OpenStockAlerts, &d.TotalPatients, &d.ConflictsPrevented)
	if err != nil {
		return err
	}
	if d.RecentActivity, err = collect[Activity](ctx, s.Pool, `SELECT a.id, a.at, a.action, a.entity, a.entity_id, u.full_name
		FROM "tn-audit_log" a LEFT JOIN "tn-dk-users" u ON u.id = a.actor_user_id
		WHERE a.action <> 'login' ORDER BY a.id DESC LIMIT 15`); err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, d)
	return nil
}

type RevenueRow struct {
	Month          string `json:"month"`
	ChargedKurus   int64  `json:"charged_kurus"`
	CollectedKurus int64  `json:"collected_kurus"`
	RefundedKurus  int64  `json:"refunded_kurus"`
}

func (s *Service) revenue(w http.ResponseWriter, r *http.Request) error {
	from, to, err := s.dateRange(r)
	if err != nil {
		return err
	}
	items, err := collect[RevenueRow](r.Context(), s.Pool, `
		SELECT to_char(date_trunc('month', created_at AT TIME ZONE $3), 'YYYY-MM'),
		       COALESCE(sum(amount_kurus) FILTER (WHERE kind IN ('charge','charge_reversal')), 0)::bigint,
		       COALESCE(-sum(amount_kurus) FILTER (WHERE kind = 'payment'), 0)::bigint,
		       COALESCE(sum(amount_kurus) FILTER (WHERE kind = 'refund'), 0)::bigint
		FROM "tn-ledger_entries" WHERE created_at >= $1 AND created_at < $2
		GROUP BY 1 ORDER BY 1`, from, to, s.Loc.String())
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"from": from, "to": to, "items": items})
	return nil
}

type DentistRow struct {
	DentistID    int64  `json:"dentist_id"`
	DentistName  string `json:"dentist_name"`
	Completed    int64  `json:"completed"`
	Cancelled    int64  `json:"cancelled"`
	NoShow       int64  `json:"no_show"`
	ChargedKurus int64  `json:"charged_kurus"`
}

func (s *Service) dentists(w http.ResponseWriter, r *http.Request) error {
	from, to, err := s.dateRange(r)
	if err != nil {
		return err
	}
	items, err := collect[DentistRow](r.Context(), s.Pool, `
		SELECT d.id, d.full_name,
		       count(*) FILTER (WHERE a.status = 'completed'),
		       count(*) FILTER (WHERE a.status = 'cancelled'),
		       count(*) FILTER (WHERE a.status = 'no_show'),
		       COALESCE((SELECT sum(l.amount_kurus) FROM "tn-ledger_entries" l JOIN "tn-appointments" a2 ON a2.id = l.appointment_id
		                 WHERE a2.dentist_id = d.id AND l.kind IN ('charge','charge_reversal') AND l.created_at >= $1 AND l.created_at < $2), 0)::bigint
		FROM "tn-staff" d LEFT JOIN "tn-appointments" a ON a.dentist_id = d.id AND lower(a.during) >= $1 AND lower(a.during) < $2
		WHERE d.kind = 'dentist' GROUP BY d.id ORDER BY d.id`, from, to)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"from": from, "to": to, "items": items})
	return nil
}

type UnitRow struct {
	UnitID       int64   `json:"unit_id"`
	UnitName     string  `json:"unit_name"`
	BookedMin    int64   `json:"booked_minutes"`
	Appointments int64   `json:"appointments"`
	Utilization  float64 `json:"utilization"`
}

func (s *Service) units(w http.ResponseWriter, r *http.Request) error {
	day := time.Now().In(s.Loc)
	if v := r.URL.Query().Get("date"); v != "" {
		var err error
		if day, err = time.ParseInLocation(time.DateOnly, v, s.Loc); err != nil {
			return httpx.BadRequest("date YYYY-AA-GG olmalı")
		}
	}
	start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, s.Loc)
	items, err := collect[UnitRow](r.Context(), s.Pool, `
		SELECT u.id, u.name,
		       COALESCE(sum(EXTRACT(EPOCH FROM upper(a.during) - lower(a.during)) / 60), 0)::bigint,
		       count(a.id),
		       round(COALESCE(sum(EXTRACT(EPOCH FROM upper(a.during) - lower(a.during)) / 60), 0) / 540.0, 3)::float8
		FROM "tn-units" u LEFT JOIN "tn-appointments" a ON a.unit_id = u.id AND a.status IN ('booked','completed')
		     AND a.during && tstzrange($1, $2)
		WHERE u.active
		GROUP BY u.id ORDER BY u.id`, start, start.AddDate(0, 0, 1))
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"date": start.Format(time.DateOnly), "items": items})
	return nil
}

type Debtor struct {
	PatientID    int64  `json:"patient_id"`
	PatientName  string `json:"patient_name"`
	BalanceKurus int64  `json:"balance_kurus"`
}

func (s *Service) receivables(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	var total, overdue, overdueCount int64
	if err := s.Pool.QueryRow(ctx, `SELECT
		COALESCE((SELECT sum(balance_kurus) FROM "tn-patient_accounts" WHERE balance_kurus > 0), 0)::bigint,
		COALESCE((SELECT sum(amount_kurus - paid_kurus) FROM "tn-installments" WHERE status = 'open' AND due_date < current_date), 0)::bigint,
		(SELECT count(*) FROM "tn-installments" WHERE status = 'open' AND due_date < current_date)`).Scan(&total, &overdue, &overdueCount); err != nil {
		return err
	}
	top, err := collect[Debtor](ctx, s.Pool, `SELECT a.patient_id, p.first_name || ' ' || p.last_name, a.balance_kurus
		FROM "tn-patient_accounts" a JOIN "tn-patients" p ON p.id = a.patient_id
		WHERE a.balance_kurus > 0 ORDER BY a.balance_kurus DESC, a.patient_id LIMIT 20`)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"total_receivable_kurus": total, "overdue_installments_kurus": overdue,
		"overdue_installments": overdueCount, "top_debtors": top})
	return nil
}

type StockRow struct {
	SKU           string `json:"sku"`
	Name          string `json:"name"`
	Unit          string `json:"unit"`
	ConsumedMilli int64  `json:"consumed_milli"`
	ReturnedMilli int64  `json:"returned_milli"`
}

func (s *Service) stock(w http.ResponseWriter, r *http.Request) error {
	from, to, err := s.dateRange(r)
	if err != nil {
		return err
	}
	items, err := collect[StockRow](r.Context(), s.Pool, `
		SELECT s.sku, s.name, s.unit,
		       COALESCE(-sum(m.qty_milli) FILTER (WHERE m.kind = 'consume'), 0)::bigint,
		       COALESCE(sum(m.qty_milli) FILTER (WHERE m.kind = 'return'), 0)::bigint
		FROM "tn-supplies" s LEFT JOIN "tn-stock_movements" m ON m.supply_id = s.id AND m.created_at >= $1 AND m.created_at < $2
		GROUP BY s.id ORDER BY s.name`, from, to)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"from": from, "to": to, "items": items})
	return nil
}

type Notification struct {
	ID            int64     `json:"id"`
	AppointmentID *int64    `json:"appointment_id"`
	Channel       string    `json:"channel"`
	Kind          string    `json:"kind"`
	Body          string    `json:"body"`
	CreatedAt     time.Time `json:"created_at"`
}

func (s *Service) notifications(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.Int64Param(chi.URLParam(r, "patientID"), "patientID")
	if err != nil {
		return err
	}
	if !auth.MustFromContext(r.Context()).CanSeePatient(id) {
		return httpx.Forbidden()
	}
	items, err := collect[Notification](r.Context(), s.Pool, `SELECT id, appointment_id, channel, kind, body, created_at
		FROM "tn-notifications" WHERE patient_id = $1 ORDER BY id DESC LIMIT 50`, id)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
	return nil
}

// reminders creates one simulated SMS reminder per booked appointment of the next day.
func (s *Service) reminders(w http.ResponseWriter, r *http.Request) error {
	now := time.Now().In(s.Loc)
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, s.Loc).AddDate(0, 0, 1)
	tag, err := s.Pool.Exec(r.Context(), `
		INSERT INTO "tn-notifications" (patient_id, appointment_id, kind, body)
		SELECT a.patient_id, a.id, 'reminder',
		       'Hatırlatma: yarın ' || to_char(lower(a.during) AT TIME ZONE $3, 'HH24:MI') || ' saatinde ' || t.name || ' randevunuz var.'
		FROM "tn-appointments" a JOIN "tn-treatment_types" t ON t.code = a.treatment_code
		WHERE a.status = 'booked' AND lower(a.during) >= $1 AND lower(a.during) < $2
		  AND NOT EXISTS (SELECT 1 FROM "tn-notifications" n WHERE n.appointment_id = a.id AND n.kind = 'reminder')`,
		start, start.AddDate(0, 0, 1), s.Loc.String())
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"created": tag.RowsAffected()})
	return nil
}

func (s *Service) Routes(r chi.Router) {
	r.With(auth.Require(auth.Staff...)).Get("/reports/dashboard", httpx.HandlerFunc(s.dashboard).ServeHTTP)
	r.With(auth.Require(auth.RoleAccounting)).Get("/reports/revenue", httpx.HandlerFunc(s.revenue).ServeHTTP)
	r.With(auth.Require(auth.RoleAccounting, auth.RoleDentist)).Get("/reports/dentists", httpx.HandlerFunc(s.dentists).ServeHTTP)
	r.With(auth.Require(auth.RoleReception, auth.RoleDentist, auth.RoleAccounting)).Get("/reports/units", httpx.HandlerFunc(s.units).ServeHTTP)
	r.With(auth.Require(auth.RoleAccounting)).Get("/reports/receivables", httpx.HandlerFunc(s.receivables).ServeHTTP)
	r.With(auth.Require(auth.RoleAssistant, auth.RoleAccounting)).Get("/reports/stock-usage", httpx.HandlerFunc(s.stock).ServeHTTP)
	r.With(auth.Require(auth.RolePatient, auth.RoleReception)).Get("/patients/{patientID}/notifications", httpx.HandlerFunc(s.notifications).ServeHTTP)
	r.With(auth.Require(auth.RoleReception)).Post("/notifications/reminders", httpx.HandlerFunc(s.reminders).ServeHTTP)
}
