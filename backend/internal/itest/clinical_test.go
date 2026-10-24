package itest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/sudekazan/ddo_proje/backend/internal/auth"
	"github.com/sudekazan/ddo_proje/backend/internal/billing"
	"github.com/sudekazan/ddo_proje/backend/internal/dental"
	"github.com/sudekazan/ddo_proje/backend/internal/httpx"
	"github.com/sudekazan/ddo_proje/backend/internal/scheduling"
)

type httpxError = httpx.Error

func httpxFromDB(err error) error {
	if he := httpx.FromDB(err); he != nil {
		return he
	}
	return err
}

func approvedPlan(t *testing.T, e *env, patient int64, items ...dental.ItemInput) dental.Plan {
	t.Helper()
	ctx := context.Background()
	p, err := e.dental.CreatePlan(ctx, e.dentist, dental.PlanRequest{PatientID: patient, Items: items})
	if err != nil {
		t.Fatal(err)
	}
	if p, err = e.dental.Approve(ctx, e.reception, p.ID, p.CurrentVersion); err != nil {
		t.Fatal(err)
	}
	return p
}

func itemOf(p dental.Plan, code string) dental.PlanItem {
	for _, it := range p.Items {
		if it.TreatmentCode == code {
			return it
		}
	}
	return dental.PlanItem{}
}

// T-1 + T-2 + T-3: root canal sessions respect the 7 day gap; the crown waits for the root canal;
// three progress charges add up exactly to the discounted item price.
func TestMultiSessionPlanGapDependencyAndProgressBilling(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	const patient = 2 // institution patient (15% discount)
	p := approvedPlan(t, e, patient, dental.ItemInput{Tooth: ptr(46), TreatmentCode: "CROWN"}, dental.ItemInput{Tooth: ptr(46), TreatmentCode: "ROOT_CANAL"})
	rc, crown := itemOf(p, "ROOT_CANAL"), itemOf(p, "CROWN")
	if crown.DependsOn == nil || *crown.DependsOn != rc.ID || rc.Seq != 1 {
		t.Fatalf("crown must follow root canal: %+v %+v", rc, crown)
	}
	if rc.DiscountKurus != billing.Discount(rc.UnitPriceKurus, 1500) {
		t.Fatalf("institution discount not applied: %+v", rc)
	}
	book := func(item int64, start time.Time) (scheduling.Appointment, error) {
		return e.engine.Book(ctx, e.reception, scheduling.BookRequest{PatientID: patient, DentistID: 1, PlanItemID: &item, Start: start})
	}
	if _, err := book(crown.ID, at(e.monday, 9, 0)); errCode(err) != "DEPENDENCY_NOT_MET" {
		t.Fatalf("crown before root canal: %v", err)
	}
	s1, err := book(rc.ID, at(e.monday, 9, 0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := book(rc.ID, at(e.monday.AddDate(0, 0, 3), 9, 0)); errCode(err) != "MIN_GAP_VIOLATION" {
		t.Fatalf("session 2 after 3 days: %v", err)
	}
	s2, err := book(rc.ID, at(e.monday.AddDate(0, 0, 7), 9, 0))
	if err != nil {
		t.Fatal(err)
	}
	s3, err := book(rc.ID, at(e.monday.AddDate(0, 0, 14), 9, 0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := book(rc.ID, at(e.monday.AddDate(0, 0, 21), 9, 0)); errCode(err) != "ALL_SESSIONS_BOOKED" {
		t.Fatalf("4th session: %v", err)
	}
	addLot(t, e.pool, "L1", time.Now().AddDate(1, 0, 0), 100_000)
	// Sessions complete in order only.
	if _, err := e.flow.Complete(ctx, e.assistant, s2.ID, ""); errCode(err) != "SESSION_ORDER" {
		t.Fatalf("out of order completion: %v", err)
	}
	var billed int64
	for _, s := range []scheduling.Appointment{s1, s2, s3} {
		r, err := e.flow.Complete(ctx, e.assistant, s.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		billed += r.ChargedKurus
	}
	if billed != rc.NetKurus {
		t.Fatalf("billed %d != item net %d", billed, rc.NetKurus)
	}
	var state string
	if err := e.pool.QueryRow(ctx, `SELECT state FROM "tn-tooth_status" WHERE patient_id = $1 AND tooth = 46`, patient).Scan(&state); err != nil || state != dental.RootCanal {
		t.Fatalf("tooth state %q err=%v", state, err)
	}
	if _, err := book(crown.ID, at(e.monday.AddDate(0, 0, 15), 9, 0)); err != nil {
		t.Fatalf("crown after root canal: %v", err)
	}
	assertConsistent(t, e)
}

func TestExtractedToothRejectsFilling(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a, err := e.engine.Book(ctx, e.reception, scheduling.BookRequest{PatientID: 3, DentistID: 1, TreatmentCode: "EXTRACTION", Tooth: ptr(26), Start: at(e.monday, 9, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.flow.Complete(ctx, e.assistant, a.ID, ""); err != nil {
		t.Fatal(err)
	}
	_, err = e.engine.Book(ctx, e.reception, scheduling.BookRequest{PatientID: 3, DentistID: 1, TreatmentCode: "FILLING", Tooth: ptr(26), Start: at(e.monday, 10, 0)})
	if errCode(err) != "INVALID_TOOTH_PROCEDURE" {
		t.Fatalf("filling on extracted tooth booked: %v", err)
	}
	_, err = e.dental.CreatePlan(ctx, e.dentist, dental.PlanRequest{PatientID: 3, Items: []dental.ItemInput{{Tooth: ptr(26), TreatmentCode: "FILLING"}}})
	if errCode(err) != "INVALID_TOOTH_PROCEDURE" {
		t.Fatalf("filling on extracted tooth planned: %v", err)
	}
}

func TestPlanVersioningRequiresFreshApproval(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	p, err := e.dental.CreatePlan(ctx, e.dentist, dental.PlanRequest{PatientID: 4, Items: []dental.ItemInput{{TreatmentCode: "CLEANING"}}})
	if err != nil {
		t.Fatal(err)
	}
	if p, err = e.dental.Approve(ctx, e.reception, p.ID, 1); err != nil {
		t.Fatal(err)
	}
	p, err = e.dental.Revise(ctx, e.dentist, p.ID, dental.PlanRequest{Items: []dental.ItemInput{{TreatmentCode: "CLEANING"}, {Tooth: ptr(11), TreatmentCode: "FILLING"}}})
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != "draft" || p.CurrentVersion != 2 || len(p.Versions) != 2 || p.Versions[0].ApprovedAt == nil {
		t.Fatalf("revision: %+v", p)
	}
	if _, err := e.dental.Approve(ctx, e.reception, p.ID, 1); errCode(err) != "PLAN_VERSION_MISMATCH" {
		t.Fatalf("stale approval: %v", err)
	}
	// A patient may approve only their own plan.
	if _, err := e.dental.Approve(ctx, auth.Principal{UserID: 5, Role: auth.RolePatient, PatientID: 1}, p.ID, 2); errCode(err) != "FORBIDDEN" {
		t.Fatalf("foreign patient approval: %v", err)
	}
}

// T-4 proof: concurrent completions never overdraw stock.
func TestConcurrentCompletionNeverOverdrawsStock(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	addLot(t, e.pool, "EXPIRED", time.Now().AddDate(0, 0, -1), 50_000)
	addLot(t, e.pool, "OK", time.Now().AddDate(0, 6, 0), 3000) // enough for exactly 3 cleanings
	const n = 10
	ids := make([]int64, n)
	for i := range n {
		a, err := e.engine.Book(ctx, e.reception, scheduling.BookRequest{PatientID: int64(i + 1), DentistID: int64(i%numDentists + 1),
			TreatmentCode: "CLEANING", Start: at(e.monday.AddDate(0, 0, i/numDentists), 9, 0)})
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = a.ID
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, short := 0, 0
	for _, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.flow.Complete(ctx, e.assistant, id, "")
			mu.Lock()
			defer mu.Unlock()
			switch errCode(err) {
			case "":
				ok++
			case "INSUFFICIENT_STOCK":
				short++
			default:
				t.Errorf("unexpected: %v", err)
			}
		}()
	}
	wg.Wait()
	var okLot, expired int64
	if err := e.pool.QueryRow(ctx, `SELECT (SELECT qty_milli FROM "tn-supply_lots" WHERE lot_no = 'OK'), (SELECT qty_milli FROM "tn-supply_lots" WHERE lot_no = 'EXPIRED')`).Scan(&okLot, &expired); err != nil {
		t.Fatal(err)
	}
	if ok != 3 || short != n-3 || okLot != 0 || expired != 50_000 {
		t.Fatalf("ok=%d short=%d lot=%d expired=%d", ok, short, okLot, expired)
	}
	var alerts int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM "tn-stock_alerts" WHERE resolved_at IS NULL`).Scan(&alerts); err != nil || alerts != 1 {
		t.Fatalf("min stock alert count=%d err=%v", alerts, err)
	}
}

func TestRevertReturnsStockToSameLotAndReversesCharge(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	addLot(t, e.pool, "SOON", time.Now().AddDate(0, 1, 0), 300)
	addLot(t, e.pool, "LATER", time.Now().AddDate(0, 6, 0), 1000)
	a, err := e.engine.Book(ctx, e.reception, scheduling.BookRequest{PatientID: 5, DentistID: 1, TreatmentCode: "FILLING", Tooth: ptr(14), Start: at(e.monday, 9, 0)})
	if err != nil {
		t.Fatal(err)
	}
	r, err := e.flow.Complete(ctx, e.assistant, a.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	lots := func() (soon, later int64) {
		_ = e.pool.QueryRow(ctx, `SELECT (SELECT qty_milli FROM "tn-supply_lots" WHERE lot_no = 'SOON'), (SELECT qty_milli FROM "tn-supply_lots" WHERE lot_no = 'LATER')`).Scan(&soon, &later)
		return
	}
	if s, l := lots(); s != 0 || l != 800 { // FEFO: 300 from SOON, 200 from LATER
		t.Fatalf("after complete soon=%d later=%d", s, l)
	}
	back, err := e.flow.Revert(ctx, e.dentist, a.ID, "yanlış hasta")
	if err != nil {
		t.Fatal(err)
	}
	if s, l := lots(); s != 300 || l != 1000 {
		t.Fatalf("after revert soon=%d later=%d", s, l)
	}
	if back.ChargedKurus != -r.ChargedKurus || back.BalanceAfter != 0 {
		t.Fatalf("charge not reversed: %+v", back)
	}
	var state string
	if err := e.pool.QueryRow(ctx, `SELECT state FROM "tn-tooth_status" WHERE patient_id = 5 AND tooth = 14`).Scan(&state); err != nil || state != dental.Healthy {
		t.Fatalf("tooth state %q", state)
	}
	assertConsistent(t, e)
}

func TestConcurrentPaymentsKeepLedgerConsistent(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := e.billing.Pay(ctx, e.accounting, billing.PaymentRequest{PatientID: int64(i%3 + 1), AmountKurus: 101, Method: "cash"}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	var sum int64
	if err := e.pool.QueryRow(ctx, `SELECT sum(balance_kurus) FROM "tn-patient_accounts"`).Scan(&sum); err != nil || sum != -50*101 {
		t.Fatalf("sum of balances %d", sum)
	}
	assertConsistent(t, e)
}

func TestPlanCancelRefundsOverpayment(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	addLot(t, e.pool, "L", time.Now().AddDate(1, 0, 0), 10_000)
	p := approvedPlan(t, e, 7, dental.ItemInput{TreatmentCode: "CLEANING"}, dental.ItemInput{Tooth: ptr(21), TreatmentCode: "CROWN"})
	net := p.Versions[0].NetKurus
	if _, err := e.billing.CreateInstallments(ctx, e.accounting, p.ID, billing.InstallmentPlanRequest{DownPaymentKurus: 50_000, Count: 5,
		FirstDueDate: e.monday.Format(time.DateOnly)}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.billing.Pay(ctx, e.accounting, billing.PaymentRequest{PatientID: 7, PlanID: &p.ID, AmountKurus: 200_000, Method: "card"}); err != nil {
		t.Fatal(err)
	}
	cl := itemOf(p, "CLEANING").ID
	a, err := e.engine.Book(ctx, e.reception, scheduling.BookRequest{PatientID: 7, DentistID: 1, PlanItemID: &cl, Start: at(e.monday, 9, 0)})
	if err != nil {
		t.Fatal(err)
	}
	done, err := e.flow.Complete(ctx, e.assistant, a.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.flow.CancelPlan(ctx, e.accounting, p.ID, "vazgeçti")
	if err != nil {
		t.Fatal(err)
	}
	if res.RefundKurus != 200_000-done.ChargedKurus || res.BalanceAfter != 0 || net <= 0 {
		t.Fatalf("refund %+v (charged %d)", res, done.ChargedKurus)
	}
	var open int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM "tn-installments" WHERE plan_id = $1 AND status = 'open'`, p.ID).Scan(&open); err != nil || open != 0 {
		t.Fatalf("open installments after cancel: %d", open)
	}
	assertConsistent(t, e)
}

func TestAppendOnlyTablesRejectUpdates(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if _, err := e.billing.Pay(ctx, e.accounting, billing.PaymentRequest{PatientID: 1, AmountKurus: 100, Method: "cash"}); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`UPDATE "tn-ledger_entries" SET amount_kurus = -1`,
		`DELETE FROM "tn-ledger_entries"`,
		`UPDATE "tn-audit_log" SET action = 'x'`,
		`DELETE FROM "tn-audit_log"`,
	} {
		_, err := e.pool.Exec(ctx, sql)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "P0001" {
			t.Fatalf("%s: expected append-only error, got %v", sql, err)
		}
	}
}

func assertConsistent(t *testing.T, e *env) {
	t.Helper()
	c, err := billing.CheckConsistency(context.Background(), e.pool)
	if err != nil || !c.OK {
		t.Fatalf("ledger inconsistent: %+v err=%v", c, err)
	}
}
