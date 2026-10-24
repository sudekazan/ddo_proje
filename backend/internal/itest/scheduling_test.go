package itest

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/sudekazan/ddo_proje/backend/internal/scheduling"
)

// T-1 proof: N concurrent bookings of the same dentist and slot produce exactly one appointment.
func TestConcurrentBookingSameSlotOnlyOneWins(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	const n = 30
	var ok, busy atomic.Int32
	var wg sync.WaitGroup
	start := at(e.monday, 10, 0)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.engine.Book(ctx, e.reception, scheduling.BookRequest{PatientID: int64(i + 1), DentistID: 1, TreatmentCode: "EXAM", Start: start})
			switch errCode(err) {
			case "":
				ok.Add(1)
			case "DENTIST_BUSY":
				busy.Add(1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 1 || busy.Load() != n-1 {
		t.Fatalf("ok=%d busy=%d, want 1/%d", ok.Load(), busy.Load(), n-1)
	}
	var active int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM "tn-appointments" WHERE dentist_id = 1 AND status = 'booked'`).Scan(&active); err != nil || active != 1 {
		t.Fatalf("active appointments=%d err=%v", active, err)
	}
}

// Units and assistants are shared: with 3 units, 3 dentists can work in parallel and the
// engine retries auto-assignment races instead of failing them.
func TestConcurrentBookingDistributesSharedUnits(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	start := at(e.monday, 11, 0)
	var wg sync.WaitGroup
	units := make([]int64, numDentists)
	for d := range numDentists {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, err := e.engine.Book(ctx, e.reception, scheduling.BookRequest{PatientID: int64(d + 1), DentistID: int64(d + 1), TreatmentCode: "CLEANING", Start: start})
			if err != nil {
				t.Errorf("dentist %d: %v", d+1, err)
				return
			}
			units[d] = a.UnitID
		}()
	}
	wg.Wait()
	seen := map[int64]bool{}
	for _, u := range units {
		if u == 0 || seen[u] {
			t.Fatalf("units not distinct: %v", units)
		}
		seen[u] = true
	}
	// A fourth parallel booking finds no free unit.
	_, err := e.engine.Book(ctx, e.reception, scheduling.BookRequest{PatientID: 10, DentistID: 1, TreatmentCode: "EXAM", Start: at(e.monday, 11, 15)})
	if errCode(err) != "DENTIST_BUSY" {
		t.Fatalf("expected dentist busy, got %v", err)
	}
}

// The database itself rejects overlaps even if application checks are bypassed.
func TestExclusionConstraintRejectsRawOverlap(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	ins := `INSERT INTO "tn-appointments" (patient_id, dentist_id, assistant_id, unit_id, treatment_code, during)
		VALUES ($1, 1, 4, $2, 'EXAM', tstzrange($3, $4))`
	if _, err := e.pool.Exec(ctx, ins, 1, 1, at(e.monday, 9, 0), at(e.monday, 9, 30)); err != nil {
		t.Fatal(err)
	}
	_, err := e.pool.Exec(ctx, ins, 2, 2, at(e.monday, 9, 15), at(e.monday, 9, 45))
	if errCode(httpxFromDB(err)) != "DENTIST_BUSY" {
		t.Fatalf("expected exclusion violation, got %v", err)
	}
	// Back-to-back appointments ([) ranges) are fine.
	if _, err := e.pool.Exec(ctx, `INSERT INTO "tn-appointments" (patient_id, dentist_id, assistant_id, unit_id, treatment_code, during)
		VALUES (2, 1, 4, 1, 'EXAM', tstzrange($1, $2))`, at(e.monday, 9, 30), at(e.monday, 9, 45)); err != nil {
		t.Fatalf("adjacent slot rejected: %v", err)
	}
}

func TestLeaveBlocksBookingAndAvailability(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	mustExec(t, e.pool, `INSERT INTO "tn-staff_leaves" (staff_id, during) VALUES (1, tstzrange($1, $2))`, e.monday, e.monday.AddDate(0, 0, 1))
	_, err := e.engine.Book(ctx, e.reception, scheduling.BookRequest{PatientID: 1, DentistID: 1, TreatmentCode: "EXAM", Start: at(e.monday, 14, 0)})
	if errCode(err) != "DENTIST_ON_LEAVE" {
		t.Fatalf("expected leave conflict, got %v", err)
	}
	slots, err := e.engine.Availability(ctx, e.monday, "EXAM", 1)
	if err != nil || len(slots) != 0 {
		t.Fatalf("dentist on leave still has %d slots (err=%v)", len(slots), err)
	}
}

func TestWaitlistIsPromotedOnCancel(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a, err := e.engine.Book(ctx, e.reception, scheduling.BookRequest{PatientID: 1, DentistID: 1, TreatmentCode: "CLEANING", Start: at(e.monday, 15, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.engine.AddWaitlist(ctx, e.reception, scheduling.WaitlistRequest{PatientID: 2, TreatmentCode: "EXAM",
		From: at(e.monday, 9, 0), To: at(e.monday, 18, 0)}); err != nil {
		t.Fatal(err)
	}
	res, err := e.engine.Cancel(ctx, e.reception, a.ID, "test")
	if err != nil {
		t.Fatal(err)
	}
	if res.Promoted == nil || res.Promoted.PatientID != 2 || !res.Promoted.Start.Equal(a.Start) || res.Promoted.UnitID != a.UnitID {
		t.Fatalf("waitlist not promoted: %+v", res.Promoted)
	}
}
