package seed

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sudekazan/ddo_proje/backend/internal/auth"
)

// Tables in dependency order; reset truncates them all.
var Tables = []string{
	"tn-stock_alerts", "tn-stock_movements", "tn-recipes", "tn-supply_lots", "tn-supplies",
	"tn-installments", "tn-ledger_entries", "tn-refunds", "tn-payments", "tn-patient_accounts", "tn-price_list",
	"tn-tooth_events", "tn-tooth_status", "tn-notifications", "tn-waitlist", "tn-appointments",
	"tn-plan_items", "tn-plan_versions", "tn-treatment_plans", "tn-treatment_types", "tn-staff_leaves",
	"tn-units", "tn-staff", "tn-patients", "tn-institutions",
	"tn-idempotency_keys", "tn-audit_log", "tn-refresh_tokens", "tn-dk-users",
}

// IsSeeded reports whether the database already holds the demo data.
func IsSeeded(ctx context.Context, pool *pgxpool.Pool) (bool, error) {
	var n int64
	err := pool.QueryRow(ctx, `SELECT count(*) FROM "tn-dk-users"`).Scan(&n)
	return n > 0, err
}

func tsRange(a, b time.Time) pgtype.Range[pgtype.Timestamptz] {
	return pgtype.Range[pgtype.Timestamptz]{
		Lower: pgtype.Timestamptz{Time: a, Valid: true}, Upper: pgtype.Timestamptz{Time: b, Valid: true},
		LowerType: pgtype.Inclusive, UpperType: pgtype.Exclusive, Valid: true,
	}
}

// Load writes the dataset in a single transaction (all or nothing). When reset is true every
// table is truncated first, which returns the system to the initial seed state.
func Load(ctx context.Context, pool *pgxpool.Pool, ds *Dataset, reset bool) error {
	hash, err := auth.HashPassword(DemoPassword)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if reset {
			list := ""
			for i, t := range Tables {
				if i > 0 {
					list += ", "
				}
				list += pgx.Identifier{t}.Sanitize()
			}
			if _, err := tx.Exec(ctx, "TRUNCATE "+list+" RESTART IDENTITY CASCADE"); err != nil {
				return err
			}
		}
		copyRows := func(table string, cols []string, rows [][]any) error {
			_, err := tx.CopyFrom(ctx, pgx.Identifier{table}, cols, pgx.CopyFromRows(rows))
			if err != nil {
				return fmt.Errorf("copy %s: %w", table, err)
			}
			return nil
		}
		var rows [][]any
		for _, u := range ds.Users {
			rows = append(rows, []any{u.ID, u.Email, hash, u.FullName, u.Role})
		}
		if err := copyRows("tn-dk-users", []string{"id", "email", "password_hash", "full_name", "role"}, rows); err != nil {
			return err
		}
		rows = nil
		for _, i := range ds.Institutions {
			rows = append(rows, []any{i[0], i[1], int32(i[2].(int))})
		}
		if err := copyRows("tn-institutions", []string{"id", "name", "discount_bp"}, rows); err != nil {
			return err
		}
		rows = nil
		for _, s := range ds.Staff {
			rows = append(rows, []any{s.ID, s.FullName, s.Kind, s.UserID})
		}
		if err := copyRows("tn-staff", []string{"id", "full_name", "kind", "user_id"}, rows); err != nil {
			return err
		}
		rows = nil
		for i, u := range ds.Units {
			rows = append(rows, []any{int64(i + 1), u})
		}
		if err := copyRows("tn-units", []string{"id", "name"}, rows); err != nil {
			return err
		}
		rows = nil
		for _, t := range ds.Treatments {
			rows = append(rows, []any{t.Code, t.Name, int32(t.DurationMin), int32(t.Sessions), int32(t.MinGapDays), t.RequiresTooth})
		}
		if err := copyRows("tn-treatment_types", []string{"code", "name", "duration_min", "sessions", "min_gap_days", "requires_tooth"}, rows); err != nil {
			return err
		}
		for _, p := range ds.Prices {
			if _, err := tx.Exec(ctx, `INSERT INTO "tn-price_list" (treatment_code, price_kurus, valid_during) VALUES ($1, $2, daterange($3::date, $4::date))`,
				p.Code, p.Kurus, p.From.Format(time.DateOnly), dateOrNil(p.To)); err != nil {
				return err
			}
		}
		for _, l := range ds.Leaves {
			if _, err := tx.Exec(ctx, `INSERT INTO "tn-staff_leaves" (staff_id, during, reason) VALUES ($1, tstzrange($2, $3), $4)`,
				l.StaffID, l.Start, l.End, l.Reason); err != nil {
				return err
			}
		}
		rows = nil
		for _, p := range ds.Patients {
			rows = append(rows, []any{p.ID, p.NationalID, p.FirstName, p.LastName, p.BirthDate, p.Phone, p.InstitutionID, p.UserID, p.CreatedAt})
		}
		if err := copyRows("tn-patients", []string{"id", "national_id", "first_name", "last_name", "birth_date", "phone", "institution_id", "user_id", "created_at"}, rows); err != nil {
			return err
		}
		rows = nil
		for _, a := range ds.Appointments {
			var tooth *int16
			if a.Tooth != nil {
				t := int16(*a.Tooth)
				tooth = &t
			}
			var completed *time.Time
			if a.Status == "completed" {
				completed = &a.End
			}
			rows = append(rows, []any{a.ID, a.PatientID, a.DentistID, a.AssistantID, a.UnitID, a.Code, tooth,
				tsRange(a.Start, a.End), a.Status, a.CreatedAt, completed})
		}
		if err := copyRows("tn-appointments", []string{"id", "patient_id", "dentist_id", "assistant_id", "unit_id", "treatment_code",
			"tooth", "during", "status", "created_at", "completed_at"}, rows); err != nil {
			return err
		}
		rows = nil
		for _, e := range ds.ToothEvents {
			rows = append(rows, []any{e.PatientID, int16(e.Tooth), e.Code, e.Before, e.After, e.At, e.DentistID, e.AppointmentID})
		}
		if err := copyRows("tn-tooth_events", []string{"patient_id", "tooth", "treatment_code", "state_before", "state_after",
			"performed_at", "dentist_id", "appointment_id"}, rows); err != nil {
			return err
		}
		rows = nil
		for k, st := range ds.ToothStatus {
			rows = append(rows, []any{k[0], int16(k[1]), st, ds.Anchor})
		}
		if err := copyRows("tn-tooth_status", []string{"patient_id", "tooth", "state", "updated_at"}, rows); err != nil {
			return err
		}
		rows = nil
		for _, p := range ds.Payments {
			rows = append(rows, []any{p.ID, p.PatientID, p.Amount, p.Method, p.ReceivedAt})
		}
		if err := copyRows("tn-payments", []string{"id", "patient_id", "amount_kurus", "method", "received_at"}, rows); err != nil {
			return err
		}
		rows = nil
		for _, l := range ds.Ledger {
			rows = append(rows, []any{l.ID, l.PatientID, l.Kind, l.Amount, l.BalanceAfter, l.AppointmentID, l.PaymentID, l.Description, l.At})
		}
		if err := copyRows("tn-ledger_entries", []string{"id", "patient_id", "kind", "amount_kurus", "balance_after", "appointment_id",
			"payment_id", "description", "created_at"}, rows); err != nil {
			return err
		}
		rows = nil
		for _, p := range ds.Patients {
			rows = append(rows, []any{p.ID, ds.Balances[p.ID], ds.Anchor})
		}
		if err := copyRows("tn-patient_accounts", []string{"patient_id", "balance_kurus", "updated_at"}, rows); err != nil {
			return err
		}
		rows = nil
		for _, s := range ds.Supplies {
			rows = append(rows, []any{s.ID, s.SKU, s.Name, s.Unit, s.MinMilli})
		}
		if err := copyRows("tn-supplies", []string{"id", "sku", "name", "unit", "min_stock_milli"}, rows); err != nil {
			return err
		}
		for _, l := range ds.Lots {
			var lotID int64
			if err := tx.QueryRow(ctx, `INSERT INTO "tn-supply_lots" (supply_id, lot_no, expires_on, qty_milli, received_at)
				VALUES ($1, $2, $3, $4, $5) RETURNING id`, l.SupplyID, l.LotNo, l.Expires.Format(time.DateOnly), l.QtyMilli, ds.Anchor.AddDate(0, -3, 0)).Scan(&lotID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO "tn-stock_movements" (supply_id, lot_id, kind, qty_milli, created_at)
				VALUES ($1, $2, 'receive', $3, $4)`, l.SupplyID, lotID, l.QtyMilli, ds.Anchor.AddDate(0, -3, 0)); err != nil {
				return err
			}
		}
		rows = nil
		for _, r := range ds.Recipes {
			rows = append(rows, []any{r.Code, r.SupplyID, r.QtyMilli})
		}
		if err := copyRows("tn-recipes", []string{"treatment_code", "supply_id", "qty_milli"}, rows); err != nil {
			return err
		}
		// Explicit ids were used: move the sequences past them.
		for _, t := range []string{"tn-dk-users", "tn-institutions", "tn-staff", "tn-units", "tn-patients", "tn-appointments",
			"tn-payments", "tn-ledger_entries", "tn-supplies"} {
			q := fmt.Sprintf(`SELECT setval(pg_get_serial_sequence('%s', 'id'), (SELECT max(id) FROM %s))`,
				pgx.Identifier{t}.Sanitize(), pgx.Identifier{t}.Sanitize())
			if _, err := tx.Exec(ctx, q); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `INSERT INTO "tn-audit_log" (actor_user_id, action, entity, entity_id, new_value)
			VALUES (1, 'seed.load', 'system', '0', $1)`, map[string]any{
			"patients": len(ds.Patients), "appointments": len(ds.Appointments), "anchor": ds.Anchor.Format(time.DateOnly), "reset": reset})
		return err
	})
}

func dateOrNil(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.Format(time.DateOnly)
}
