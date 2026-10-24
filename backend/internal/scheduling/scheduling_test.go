package scheduling

import (
	"testing"
	"time"
)

var ist, _ = time.LoadLocation("Europe/Istanbul")

func at(day, hh, mm int) time.Time { return time.Date(2026, 11, day, hh, mm, 0, 0, ist) }

func TestCheckWorkingHours(t *testing.T) {
	ok := at(2, 9, 0) // Monday
	if err := CheckWorkingHours(ok, ok.Add(time.Hour), ist); err != nil {
		t.Fatal(err)
	}
	cases := []time.Time{at(1, 10, 0) /* Sunday */, at(2, 8, 45), at(2, 9, 10), at(2, 17, 30)}
	for _, s := range cases {
		if CheckWorkingHours(s, s.Add(time.Hour), ist) == nil {
			t.Errorf("%v should be rejected", s)
		}
	}
}

func TestNextSessionEnforcesMinimumGap(t *testing.T) {
	booked := []session{{No: 1, Start: at(2, 10, 0), End: at(2, 11, 0)}}
	if _, err := NextSession(booked, 3, 7, at(6, 10, 0), ist); err == nil {
		t.Fatal("4 days after session 1 must violate a 7 day gap")
	}
	no, err := NextSession(booked, 3, 7, at(9, 9, 0), ist)
	if err != nil || no != 2 {
		t.Fatalf("exactly 7 calendar days later should be session 2: %d %v", no, err)
	}
}

func TestNextSessionFillsCancelledGapAndRespectsLater(t *testing.T) {
	// Session 1 was cancelled, session 2 is booked on the 16th: a new booking becomes session 1
	// and must be at least 7 days before session 2.
	booked := []session{{No: 2, Start: at(16, 10, 0), End: at(16, 11, 0)}}
	if _, err := NextSession(booked, 3, 7, at(12, 10, 0), ist); err == nil {
		t.Fatal("must be 7 days before the next session")
	}
	if no, err := NextSession(booked, 3, 7, at(9, 10, 0), ist); err != nil || no != 1 {
		t.Fatalf("got %d %v", no, err)
	}
}

func TestNextSessionAllBooked(t *testing.T) {
	booked := []session{{No: 1, Start: at(2, 10, 0)}}
	if _, err := NextSession(booked, 1, 0, at(20, 10, 0), ist); err == nil {
		t.Fatal("single-session item cannot take a second booking")
	}
}

func TestComputeSlotsRespectsAllResources(t *testing.T) {
	day := at(3, 0, 0)
	dentists := []Resource{{ID: 1, Name: "Dt. A"}}
	busy := Busy{
		Dentist:   map[int64][]Interval{1: {{at(3, 9, 0), at(3, 17, 0)}}},
		Unit:      map[int64][]Interval{10: {{at(3, 17, 0), at(3, 17, 30)}}},
		Assistant: map[int64][]Interval{},
	}
	slots := ComputeSlots(day, 30*time.Minute, dentists, []int64{10}, []int64{20}, busy, time.Time{}, ist)
	// Dentist free 17:00-18:00, unit busy 17:00-17:30 → only 17:30 fits a 30 minute treatment.
	if len(slots) != 1 || !slots[0].Start.Equal(at(3, 17, 30)) || slots[0].UnitID != 10 || slots[0].AssistantID != 20 {
		t.Fatalf("slots=%+v", slots)
	}
	if got := ComputeSlots(at(1, 0, 0), 30*time.Minute, dentists, []int64{10}, []int64{20}, Busy{}, time.Time{}, ist); len(got) != 0 {
		t.Fatal("no slots on Sunday")
	}
}
