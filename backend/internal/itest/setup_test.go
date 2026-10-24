// Package itest holds integration tests that run against a real PostgreSQL 16.
// Each test gets its own freshly migrated database. Set TEST_DATABASE_URL to a server
// where the user may CREATE DATABASE (CI uses a postgres:16 service container; locally
// `make test-integration` starts one with Docker Compose). Without it the tests are skipped.
package itest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sudekazan/ddo_proje/backend/internal/auth"
	"github.com/sudekazan/ddo_proje/backend/internal/billing"
	"github.com/sudekazan/ddo_proje/backend/internal/clinic"
	"github.com/sudekazan/ddo_proje/backend/internal/dental"
	"github.com/sudekazan/ddo_proje/backend/internal/httpx"
	"github.com/sudekazan/ddo_proje/backend/internal/inventory"
	"github.com/sudekazan/ddo_proje/backend/internal/platform"
	"github.com/sudekazan/ddo_proje/backend/internal/scheduling"
	"github.com/sudekazan/ddo_proje/backend/internal/seed"
	"github.com/sudekazan/ddo_proje/backend/internal/workflow"
)

var ist, _ = time.LoadLocation("Europe/Istanbul")

// env bundles the services wired to one test database.
type env struct {
	pool                             *pgxpool.Pool
	engine                           *scheduling.Engine
	dental                           *dental.Service
	billing                          *billing.Service
	stock                            *inventory.Service
	flow                             *workflow.Service
	monday                           time.Time // a Monday at least a week ahead, 00:00 clinic time
	dentist                          auth.Principal
	reception, assistant, accounting auth.Principal
}

const (
	numDentists = 3
	numPatients = 60
	passwordFix = "Demo123!"
)

func setup(t *testing.T) *env {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	name := "dh_test_" + hex.EncodeToString(b)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database: %v", err)
	}
	u, _ := url.Parse(base)
	u.Path = "/" + name
	pool, err := platform.Connect(ctx, u.String())
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		_ = admin.Close(context.Background())
	})
	if err := platform.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	loadFixtures(t, pool)

	now := time.Now().In(ist)
	d := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, ist).AddDate(0, 0, 7)
	for d.Weekday() != time.Monday {
		d = d.AddDate(0, 0, 1)
	}
	return &env{
		pool:       pool,
		engine:     &scheduling.Engine{Pool: pool, Loc: ist},
		dental:     &dental.Service{Pool: pool, Loc: ist},
		billing:    &billing.Service{Pool: pool, Loc: ist},
		stock:      &inventory.Service{Pool: pool, Loc: ist},
		flow:       &workflow.Service{Pool: pool, Loc: ist},
		monday:     d,
		dentist:    auth.Principal{UserID: 2, Role: auth.RoleDentist, StaffID: 1},
		reception:  auth.Principal{UserID: 1, Role: auth.RoleReception},
		assistant:  auth.Principal{UserID: 3, Role: auth.RoleAssistant, StaffID: numDentists + 1},
		accounting: auth.Principal{UserID: 4, Role: auth.RoleAccounting},
	}
}

func mustExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// loadFixtures creates a small clinic: 3 dentists, 3 assistants, 3 units, 60 patients,
// the real treatment types and an open-ended price list, and one stock-tracked supply.
func loadFixtures(t *testing.T, pool *pgxpool.Pool) {
	hash, err := auth.HashPassword(passwordFix)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, pool, `INSERT INTO "tn-dk-users" (id, email, password_hash, full_name, role) VALUES
		(1, 'resepsiyon@test.local', $1, 'Resepsiyon', 'reception'),
		(2, 'hekim@test.local', $1, 'Hekim', 'dentist'),
		(3, 'asistan@test.local', $1, 'Asistan', 'assistant'),
		(4, 'muhasebe@test.local', $1, 'Muhasebe', 'accounting'),
		(5, 'hasta@test.local', $1, 'Hasta', 'patient')`, hash)
	mustExec(t, pool, `SELECT setval(pg_get_serial_sequence('"tn-dk-users"', 'id'), 5)`)
	for i := 1; i <= numDentists; i++ {
		var uid *int64
		if i == 1 {
			v := int64(2)
			uid = &v
		}
		mustExec(t, pool, `INSERT INTO "tn-staff" (full_name, kind, user_id) VALUES ($1, 'dentist', $2)`, fmt.Sprintf("Dt. %d", i), uid)
	}
	for i := 1; i <= numDentists; i++ {
		mustExec(t, pool, `INSERT INTO "tn-staff" (full_name, kind) VALUES ($1, 'assistant')`, fmt.Sprintf("Asistan %d", i))
		mustExec(t, pool, `INSERT INTO "tn-units" (name) VALUES ($1)`, fmt.Sprintf("Ünite %d", i))
	}
	mustExec(t, pool, `INSERT INTO "tn-institutions" (name, discount_bp) VALUES ('Anlaşmalı Kurum', 1500)`)
	for _, tt := range seed.Treatments {
		mustExec(t, pool, `INSERT INTO "tn-treatment_types" (code, name, duration_min, sessions, min_gap_days, requires_tooth)
			VALUES ($1, $2, $3, $4, $5, $6)`, tt.Code, tt.Name, tt.DurationMin, tt.Sessions, tt.MinGapDays, tt.RequiresTooth)
		mustExec(t, pool, `INSERT INTO "tn-price_list" (treatment_code, price_kurus, valid_during) VALUES ($1, $2, daterange('2020-01-01', NULL))`,
			tt.Code, tt.BasePriceTL*100+1) // +1 kuruş makes session splits uneven on purpose
	}
	for i := 1; i <= numPatients; i++ {
		var inst *int64
		if i%2 == 0 {
			v := int64(1)
			inst = &v
		}
		tc := clinic.CompleteTCKN([9]int{1, 2, 3, 4, i / 100 % 10, i / 10 % 10, i % 10, 5, 6})
		mustExec(t, pool, `INSERT INTO "tn-patients" (national_id, first_name, last_name, birth_date, phone, institution_id)
			VALUES ($1, 'Test', $2, '1990-01-01', '05550000000', $3)`, tc, fmt.Sprintf("Hasta%02d", i), inst)
		mustExec(t, pool, `INSERT INTO "tn-patient_accounts" (patient_id) SELECT max(id) FROM "tn-patients"`)
	}
	mustExec(t, pool, `UPDATE "tn-patients" SET user_id = 5 WHERE id = 1`)
	mustExec(t, pool, `INSERT INTO "tn-supplies" (id, sku, name, unit, min_stock_milli) VALUES (1, 'KMP', 'Kompozit', 'g', 1000)`)
	mustExec(t, pool, `INSERT INTO "tn-recipes" (treatment_code, supply_id, qty_milli) VALUES ('FILLING', 1, 500), ('CLEANING', 1, 1000)`)
}

// addLot adds stock to the fixture supply.
func addLot(t *testing.T, pool *pgxpool.Pool, lotNo string, expires time.Time, qty int64) {
	t.Helper()
	mustExec(t, pool, `INSERT INTO "tn-supply_lots" (supply_id, lot_no, expires_on, qty_milli) VALUES (1, $1, $2, $3)`,
		lotNo, expires.Format(time.DateOnly), qty)
}

func at(day time.Time, hh, mm int) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day(), hh, mm, 0, 0, ist)
}

func ptr[T any](v T) *T { return &v }

// errCode returns the API error code an error would be rendered with ("" for nil).
func errCode(err error) string {
	if err == nil {
		return ""
	}
	if he := httpx.FromDB(err); he != nil {
		return he.Code
	}
	var he *httpx.Error
	if errors.As(err, &he) {
		return he.Code
	}
	return "UNMAPPED: " + err.Error()
}
