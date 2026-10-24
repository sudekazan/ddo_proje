package billing

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// Entry is a ledger movement. Amount is signed: positive increases what the patient owes.
type Entry struct {
	PatientID     int64
	Kind          string // charge | payment | refund | charge_reversal
	Amount        int64
	PlanID        *int64
	PlanItemID    *int64
	AppointmentID *int64
	PaymentID     *int64
	RefundID      *int64
	ReversesID    *int64
	Description   string
	CreatedBy     int64
}

type LedgerEntry struct {
	ID            int64     `json:"id"`
	Kind          string    `json:"kind"`
	AmountKurus   int64     `json:"amount_kurus"`
	BalanceAfter  int64     `json:"balance_after"`
	PlanID        *int64    `json:"plan_id"`
	AppointmentID *int64    `json:"appointment_id"`
	PaymentID     *int64    `json:"payment_id"`
	Description   string    `json:"description"`
	CreatedAt     time.Time `json:"created_at"`
}

// Post appends an entry and moves the cached balance. The account row lock serialises every
// movement of one patient, so balance_after always equals the running sum of the ledger.
func Post(ctx context.Context, tx pgx.Tx, e Entry) (int64, error) {
	if _, err := tx.Exec(ctx, `INSERT INTO "tn-patient_accounts" (patient_id) VALUES ($1) ON CONFLICT DO NOTHING`, e.PatientID); err != nil {
		return 0, err
	}
	var balance int64
	if err := tx.QueryRow(ctx, `SELECT balance_kurus FROM "tn-patient_accounts" WHERE patient_id = $1 FOR UPDATE`,
		e.PatientID).Scan(&balance); err != nil {
		return 0, err
	}
	balance += e.Amount
	var createdBy *int64
	if e.CreatedBy != 0 {
		createdBy = &e.CreatedBy
	}
	var id int64
	err := tx.QueryRow(ctx, `
		INSERT INTO "tn-ledger_entries" (patient_id, kind, amount_kurus, balance_after, plan_id, plan_item_id,
		                                 appointment_id, payment_id, refund_id, reverses_id, description, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) RETURNING id`,
		e.PatientID, e.Kind, e.Amount, balance, e.PlanID, e.PlanItemID, e.AppointmentID, e.PaymentID,
		e.RefundID, e.ReversesID, e.Description, createdBy).Scan(&id)
	if err != nil {
		return 0, err
	}
	_, err = tx.Exec(ctx, `UPDATE "tn-patient_accounts" SET balance_kurus = $2, updated_at = now() WHERE patient_id = $1`,
		e.PatientID, balance)
	return id, err
}

// Consistency counts violations of the ledger invariants; all three must be zero.
type Consistency struct {
	AccountsChecked       int64 `json:"accounts_checked"`
	BalanceMismatches     int64 `json:"balance_mismatches"`
	RunningSumMismatches  int64 `json:"running_sum_mismatches"`
	PlanBillingMismatches int64 `json:"plan_billing_mismatches"`
	OK                    bool  `json:"ok"`
}

func CheckConsistency(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) (Consistency, error) {
	var c Consistency
	err := q.QueryRow(ctx, `
		WITH sums AS (SELECT patient_id, sum(amount_kurus) AS s FROM "tn-ledger_entries" GROUP BY patient_id)
		SELECT (SELECT count(*) FROM "tn-patient_accounts"),
		       (SELECT count(*) FROM "tn-patient_accounts" a LEFT JOIN sums ON sums.patient_id = a.patient_id
		         WHERE a.balance_kurus <> COALESCE(sums.s, 0)),
		       (SELECT count(*) FROM (SELECT balance_after,
		                 sum(amount_kurus) OVER (PARTITION BY patient_id ORDER BY id) AS running
		               FROM "tn-ledger_entries") x WHERE x.balance_after <> x.running),
		       (SELECT count(*) FROM "tn-plan_items" pi
		         WHERE pi.billed_kurus <> COALESCE((SELECT sum(amount_kurus) FROM "tn-ledger_entries" l
		                 WHERE l.plan_item_id = pi.id AND l.kind IN ('charge','charge_reversal')), 0))`).
		Scan(&c.AccountsChecked, &c.BalanceMismatches, &c.RunningSumMismatches, &c.PlanBillingMismatches)
	c.OK = err == nil && c.BalanceMismatches == 0 && c.RunningSumMismatches == 0 && c.PlanBillingMismatches == 0
	return c, err
}
