package seed

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/sudekazan/ddo_proje/backend/internal/clinic"
	"github.com/sudekazan/ddo_proje/backend/internal/dental"
)

func digest(ds *Dataset) string {
	// JSON dereferences pointers, so the digest depends on values only.
	b, err := json.Marshal([]any{ds.Patients, ds.Appointments, ds.ToothEvents, ds.Payments, ds.Ledger, ds.Lots})
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

var ist, _ = time.LoadLocation("Europe/Istanbul")
var anchor = time.Date(2026, 10, 19, 0, 0, 0, 0, ist)

func TestGenerateIsDeterministic(t *testing.T) {
	a, b := Generate(42, anchor, ist), Generate(42, anchor, ist)
	if digest(a) != digest(b) {
		t.Fatal("same seed produced different data")
	}
	if digest(a) == digest(Generate(43, anchor, ist)) {
		t.Fatal("different seeds produced identical data")
	}
}

func TestGeneratedDataIsConsistent(t *testing.T) {
	ds := Generate(42, anchor, ist)
	if len(ds.Patients) != NumPatients || len(ds.Staff) != 2*NumDentists {
		t.Fatalf("patients=%d staff=%d", len(ds.Patients), len(ds.Staff))
	}
	if ds.Users[0].Email != "seed.orman@ddo-case.invalid" {
		t.Fatalf("first user is %s", ds.Users[0].Email)
	}
	if len(ds.Appointments) < 30000 {
		t.Fatalf("only %d appointments for 3 years", len(ds.Appointments))
	}
	for _, p := range ds.Patients {
		if !clinic.ValidTCKN(p.NationalID) {
			t.Fatalf("invalid national id %s", p.NationalID)
		}
	}
	// Ledger: balance_after is the running sum per patient and matches the final balance.
	running := map[int64]int64{}
	for _, e := range ds.Ledger {
		running[e.PatientID] += e.Amount
		if running[e.PatientID] != e.BalanceAfter {
			t.Fatalf("ledger %d running sum mismatch", e.ID)
		}
	}
	for pid, b := range ds.Balances {
		if running[pid] != b {
			t.Fatalf("patient %d balance mismatch", pid)
		}
	}
	// Tooth history replays to valid transitions; no resource is double booked.
	for _, e := range ds.ToothEvents {
		if after, err := dental.Apply(e.Code, e.Tooth, e.Before); err != nil || after != e.After {
			t.Fatalf("invalid tooth event %+v: %v", e, err)
		}
	}
	type key struct {
		kind string
		id   int64
	}
	busy := map[key][]Appointment{}
	for _, a := range ds.Appointments {
		if a.Status == "cancelled" || a.Status == "no_show" {
			continue
		}
		for _, k := range []key{{"d", a.DentistID}, {"u", a.UnitID}, {"a", a.AssistantID}, {"p", a.PatientID}} {
			for _, o := range busy[k] {
				if a.Start.Before(o.End) && o.Start.Before(a.End) {
					t.Fatalf("overlap %s%d: %d and %d", k.kind, k.id, a.ID, o.ID)
				}
			}
			busy[k] = append(busy[k], a)
		}
	}
}
