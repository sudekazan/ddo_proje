// Command sim is the data generator service.
//
//	sim seed   load the deterministic demo dataset unless the database is already seeded
//	sim reset  truncate everything and load the dataset again (make reset-demo)
//	sim live   generate continuous API traffic so the UI shows a live clinic
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
	_ "time/tzdata" // container images carry no zoneinfo

	"github.com/sudekazan/ddo_proje/backend/internal/auth"
	"github.com/sudekazan/ddo_proje/backend/internal/dental"
	"github.com/sudekazan/ddo_proje/backend/internal/platform"
	"github.com/sudekazan/ddo_proje/backend/internal/seed"
	"github.com/sudekazan/ddo_proje/backend/internal/sim"
)

func main() {
	cmd := "seed"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	var err error
	switch cmd {
	case "seed", "reset":
		err = load(ctx, cmd == "reset")
	case "live":
		err = live(ctx)
	default:
		err = fmt.Errorf("unknown command %q", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func seedValue() uint64 {
	v, err := strconv.ParseUint(os.Getenv("SEED"), 10, 64)
	if err != nil {
		return 42
	}
	return v
}

func location() *time.Location {
	loc, err := time.LoadLocation(envOr("CLINIC_TZ", "Europe/Istanbul"))
	if err != nil {
		return time.UTC
	}
	return loc
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func load(ctx context.Context, reset bool) error {
	log := platform.NewLogger(envOr("LOG_LEVEL", "info"))
	loc := location()
	connectCtx, cancel := context.WithTimeout(ctx, time.Minute)
	pool, err := platform.Connect(connectCtx, os.Getenv("DATABASE_URL"))
	cancel()
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := platform.Migrate(ctx, pool); err != nil {
		return err
	}
	if !reset {
		if done, err := seed.IsSeeded(ctx, pool); err != nil {
			return err
		} else if done {
			log.Info("database already seeded; skipping")
			return nil
		}
	}
	anchor := time.Now().In(loc)
	if v := os.Getenv("SEED_ANCHOR"); v != "" {
		if anchor, err = time.ParseInLocation(time.DateOnly, v, loc); err != nil {
			return fmt.Errorf("SEED_ANCHOR: %w", err)
		}
	}
	start := time.Now()
	ds := seed.Generate(seedValue(), anchor, loc)
	if err := seed.Load(ctx, pool, ds, reset); err != nil {
		return err
	}
	// A demo treatment plan built through the real domain service: kanal → kuron on 36 and a filling.
	svc := &dental.Service{Pool: pool, Loc: loc}
	dentist := auth.Principal{UserID: 6, Role: auth.RoleDentist, StaffID: 1}
	plan, err := svc.CreatePlan(ctx, dentist, dental.PlanRequest{PatientID: 1, Note: "Demo plan", Items: []dental.ItemInput{
		{Tooth: ptr(36), TreatmentCode: "CROWN"}, {Tooth: ptr(36), TreatmentCode: "ROOT_CANAL"}, {Tooth: ptr(15), TreatmentCode: "FILLING"},
	}})
	if err != nil {
		return fmt.Errorf("demo plan: %w", err)
	}
	if _, err := svc.Approve(ctx, auth.Principal{UserID: 5, Role: auth.RolePatient, PatientID: 1}, plan.ID, 1); err != nil {
		return fmt.Errorf("demo plan approve: %w", err)
	}
	log.Info("seed loaded", "seed", seedValue(), "anchor", anchor.Format(time.DateOnly), "reset", reset,
		"patients", len(ds.Patients), "appointments", len(ds.Appointments), "ledger_entries", len(ds.Ledger),
		"duration_ms", time.Since(start).Milliseconds())
	return nil
}

func ptr(v int) *int { return &v }

func live(ctx context.Context) error {
	interval, err := time.ParseDuration(envOr("SIM_INTERVAL", "3s"))
	if err != nil {
		return err
	}
	r := &sim.Runner{BaseURL: envOr("API_URL", "http://api:8080"), Interval: interval, Seed: seedValue(),
		Log: platform.NewLogger(envOr("LOG_LEVEL", "info")), Loc: location()}
	return r.Run(ctx)
}
