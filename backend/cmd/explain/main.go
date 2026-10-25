// Command explain prints EXPLAIN (ANALYZE, BUFFERS) for the critical queries, first with the
// index in place and then without it (the index is dropped inside a transaction that is
// rolled back, so the database is not changed). Used to produce the numbers in the README.
package main

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

type probe struct {
	name, index, sql string
	args             []any
}

var probes = []probe{
	{"Hasta arama (soyad öneki + keyset)", "patients_last_name_idx",
		`SELECT id, first_name, last_name FROM "tn-patients" WHERE lower(last_name) LIKE 'yıl%' AND (lower(last_name), id) > ('yılmaz', 5000)
		 ORDER BY lower(last_name), id LIMIT 26`, nil},
	{"Cari hesap defteri (hasta başına son 50 hareket)", "ledger_patient_idx",
		`SELECT id, kind, amount_kurus, balance_after FROM "tn-ledger_entries" WHERE patient_id = 4242 ORDER BY id DESC LIMIT 51`, nil},
	{"Hasta randevu geçmişi", "appointments_patient_idx",
		`SELECT id FROM "tn-appointments" WHERE patient_id = 4242 ORDER BY lower(during) DESC LIMIT 50`, nil},
	{"Hekim çakışma kontrolü (randevu motoru)", "appt_no_dentist_overlap",
		`SELECT EXISTS (SELECT 1 FROM "tn-appointments" WHERE dentist_id = 1 AND status IN ('booked','completed')
		 AND during && tstzrange(now() - interval '400 days', now() - interval '400 days' + interval '45 minutes'))`, nil},
}

var timing = regexp.MustCompile(`Execution Time: ([0-9.]+) ms`)

func main() {
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, "ANALYZE"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, p := range probes {
		with, err := explain(ctx, conn, p, false)
		if err != nil {
			fmt.Fprintln(os.Stderr, p.name, err)
			os.Exit(1)
		}
		without, err := explain(ctx, conn, p, true)
		if err != nil {
			fmt.Fprintln(os.Stderr, p.name, err)
			os.Exit(1)
		}
		fmt.Printf("### %s — `%s`\n\n**Dizinli:**\n```\n%s```\n**Dizinsiz:**\n```\n%s```\n", p.name, p.index, with, without)
		fmt.Printf("Özet: dizinli %s ms, dizinsiz %s ms\n\n", ms(with), ms(without))
	}
}

func ms(plan string) string {
	if m := timing.FindStringSubmatch(plan); m != nil {
		return m[1]
	}
	return "?"
}

func explain(ctx context.Context, conn *pgx.Conn, p probe, dropIndex bool) (string, error) {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if dropIndex {
		if strings.HasPrefix(p.index, "appt_") {
			_, err = tx.Exec(ctx, `ALTER TABLE "tn-appointments" DROP CONSTRAINT `+p.index)
		} else {
			_, err = tx.Exec(ctx, "DROP INDEX "+p.index)
		}
		if err != nil {
			return "", err
		}
	}
	// Warm-up run so both measurements see a hot cache.
	if _, err := tx.Exec(ctx, p.sql, p.args...); err != nil {
		return "", err
	}
	rows, err := tx.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS) "+p.sql, p.args...)
	if err != nil {
		return "", err
	}
	lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return "", err
	}
	return strings.Join(lines, "\n") + "\n", nil
}
