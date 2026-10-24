// Package sim drives live traffic against the API so the UI visibly changes: bookings
// (including deliberate concurrent attempts on the same slot), cancellations, waitlist
// requests, payments and SMS reminders.
package sim

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"

	"github.com/sudekazan/ddo_proje/backend/internal/apiclient"
	"github.com/sudekazan/ddo_proje/backend/internal/seed"
)

type Runner struct {
	BaseURL  string
	Interval time.Duration
	Seed     uint64
	Log      *slog.Logger
	Loc      *time.Location

	r          *rand.Rand
	reception  *apiclient.Client
	accounting *apiclient.Client
	loggedIn   time.Time
}

func (s *Runner) login(ctx context.Context) error {
	s.reception, s.accounting = apiclient.New(s.BaseURL), apiclient.New(s.BaseURL)
	resp, err := s.reception.Login(ctx, "resepsiyon@dishekimim.local", seed.DemoPassword)
	if err := apiclient.Expect(resp, err, http.StatusOK, nil); err != nil {
		return err
	}
	resp, err = s.accounting.Login(ctx, "muhasebe@dishekimim.local", seed.DemoPassword)
	if err := apiclient.Expect(resp, err, http.StatusOK, nil); err != nil {
		return err
	}
	s.loggedIn = time.Now()
	return nil
}

func (s *Runner) Run(ctx context.Context) error {
	s.r = rand.New(rand.NewPCG(s.Seed, 7))
	t := time.NewTicker(s.Interval)
	defer t.Stop()
	tick := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		if s.reception == nil || time.Since(s.loggedIn) > time.Minute {
			if err := s.login(ctx); err != nil {
				s.Log.Warn("sim login failed", "error", err.Error())
				continue
			}
		}
		tick++
		var err error
		switch n := s.r.IntN(100); {
		case tick%20 == 0:
			err = s.reminders(ctx)
		case n < 40:
			err = s.book(ctx)
		case n < 60:
			err = s.race(ctx)
		case n < 75:
			err = s.cancel(ctx)
		case n < 85:
			err = s.waitlist(ctx)
		default:
			err = s.pay(ctx)
		}
		if err != nil {
			s.Log.Warn("sim action failed", "error", err.Error())
		}
	}
}

func (s *Runner) randomDay() time.Time {
	d := time.Now().In(s.Loc).AddDate(0, 0, 1+s.r.IntN(13))
	if d.Weekday() == time.Sunday {
		d = d.AddDate(0, 0, 1)
	}
	return d
}

var simCodes = []string{"EXAM", "CLEANING", "EXAM", "CLEANING", "FILLING"}

type slot struct {
	DentistID int64     `json:"dentist_id"`
	Start     time.Time `json:"start"`
}

func (s *Runner) freeSlot(ctx context.Context, code string) (*slot, error) {
	var out struct {
		Items []slot `json:"items"`
	}
	path := fmt.Sprintf("/api/availability?date=%s&treatment_code=%s", s.randomDay().Format(time.DateOnly), code)
	resp, err := s.reception.Do(ctx, http.MethodGet, path, nil)
	if err := apiclient.Expect(resp, err, http.StatusOK, &out); err != nil || len(out.Items) == 0 {
		return nil, err
	}
	return &out.Items[s.r.IntN(len(out.Items))], nil
}

func (s *Runner) bookBody(code string, sl *slot) map[string]any {
	body := map[string]any{"patient_id": 2 + s.r.IntN(seed.NumPatients-1), "dentist_id": sl.DentistID,
		"treatment_code": code, "start": sl.Start}
	if code == "FILLING" {
		body["tooth"] = []int{14, 15, 24, 25, 34, 35, 44, 45}[s.r.IntN(8)]
	}
	return body
}

func (s *Runner) book(ctx context.Context) error {
	code := simCodes[s.r.IntN(len(simCodes))]
	sl, err := s.freeSlot(ctx, code)
	if err != nil || sl == nil {
		return err
	}
	resp, err := s.reception.Do(ctx, http.MethodPost, "/api/appointments", s.bookBody(code, sl),
		"Idempotency-Key", fmt.Sprintf("sim-%d-%d", time.Now().UnixNano(), s.r.Int64()))
	if err == nil {
		s.Log.Info("sim book", "status", resp.Status, "code", resp.ErrorCode())
	}
	return err
}

// race fires several bookings for the same dentist and slot at once: exactly one must win.
func (s *Runner) race(ctx context.Context) error {
	sl, err := s.freeSlot(ctx, "EXAM")
	if err != nil || sl == nil {
		return err
	}
	bodies := []map[string]any{s.bookBody("EXAM", sl), s.bookBody("EXAM", sl), s.bookBody("EXAM", sl)}
	var wg sync.WaitGroup
	statuses := make([]int, len(bodies))
	for i := range bodies {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := s.reception.Do(ctx, http.MethodPost, "/api/appointments", bodies[i])
			if err == nil {
				statuses[i] = resp.Status
			}
		}()
	}
	wg.Wait()
	s.Log.Info("sim race", "statuses", statuses)
	return nil
}

func (s *Runner) cancel(ctx context.Context) error {
	var page struct {
		Items []struct {
			ID int64 `json:"id"`
		} `json:"items"`
	}
	path := fmt.Sprintf("/api/appointments?date=%s&status=booked&limit=50", s.randomDay().Format(time.DateOnly))
	resp, err := s.reception.Do(ctx, http.MethodGet, path, nil)
	if err := apiclient.Expect(resp, err, http.StatusOK, &page); err != nil || len(page.Items) == 0 {
		return err
	}
	id := page.Items[s.r.IntN(len(page.Items))].ID
	resp, err = s.reception.Do(ctx, http.MethodPost, fmt.Sprintf("/api/appointments/%d/cancel", id), map[string]string{"reason": "Hasta erteledi (simülasyon)"})
	if err == nil {
		s.Log.Info("sim cancel", "appointment", id, "status", resp.Status)
	}
	return err
}

func (s *Runner) waitlist(ctx context.Context) error {
	from := s.randomDay()
	from = time.Date(from.Year(), from.Month(), from.Day(), 9, 0, 0, 0, s.Loc)
	body := map[string]any{"patient_id": 2 + s.r.IntN(seed.NumPatients-1), "treatment_code": "EXAM",
		"from": from, "to": from.AddDate(0, 0, 3)}
	resp, err := s.reception.Do(ctx, http.MethodPost, "/api/waitlist", body)
	if err == nil {
		s.Log.Info("sim waitlist", "status", resp.Status)
	}
	return err
}

func (s *Runner) pay(ctx context.Context) error {
	var rec struct {
		TopDebtors []struct {
			PatientID    int64 `json:"patient_id"`
			BalanceKurus int64 `json:"balance_kurus"`
		} `json:"top_debtors"`
	}
	resp, err := s.accounting.Do(ctx, http.MethodGet, "/api/reports/receivables", nil)
	if err := apiclient.Expect(resp, err, http.StatusOK, &rec); err != nil || len(rec.TopDebtors) == 0 {
		return err
	}
	d := rec.TopDebtors[s.r.IntN(len(rec.TopDebtors))]
	amount := max(d.BalanceKurus/4, 100)
	resp, err = s.accounting.Do(ctx, http.MethodPost, "/api/payments",
		map[string]any{"patient_id": d.PatientID, "amount_kurus": amount, "method": "card"},
		"Idempotency-Key", fmt.Sprintf("sim-pay-%d", time.Now().UnixNano()))
	if err == nil {
		s.Log.Info("sim payment", "patient", d.PatientID, "status", resp.Status)
	}
	return err
}

func (s *Runner) reminders(ctx context.Context) error {
	resp, err := s.reception.Do(ctx, http.MethodPost, "/api/notifications/reminders", map[string]any{})
	if err == nil {
		s.Log.Info("sim reminders", "status", resp.Status)
	}
	return err
}
