// Command verify exercises every feature (F-1…F-7) and every hard task (T-1…T-4), plus health,
// authentication and authorization, end to end through the HTTP API of a running, seeded system.
// It prints a PASS/FAIL table and exits non-zero when anything fails.
package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/sudekazan/ddo_proje/backend/internal/apiclient"
	"github.com/sudekazan/ddo_proje/backend/internal/clinic"
)

type result struct {
	ID, Name string
	Err      error
	Took     time.Duration
}

type suite struct {
	base    string
	loc     *time.Location
	results []result

	reception, dentist, assistant, accounting, patient *apiclient.Client
	dentistStaffID                                     int64
	demoPatientID                                      int64
	baseDay                                            time.Time
}

func main() {
	base := os.Getenv("API_URL")
	if base == "" {
		base = "http://localhost:8080"
	}
	loc, err := time.LoadLocation("Europe/Istanbul")
	if err != nil {
		loc = time.UTC
	}
	s := &suite{base: base, loc: loc}
	ctx, cancel := context.WithTimeout(context.Background(), 14*time.Minute)
	defer cancel()

	fmt.Printf("DişHekimim doğrulama — %s\n\n", base)
	if err := s.waitHealthy(ctx); err != nil {
		fmt.Println("API hazır değil:", err)
		os.Exit(1)
	}
	s.run(ctx, "HEALTH", "GET /health veritabanı dahil sağlıklı", s.health)
	s.run(ctx, "AUTH-1", "Yanlış parola 401 döner", s.authWrongPassword)
	s.run(ctx, "AUTH-2", "Demo hesaplarla giriş (5 rol)", s.loginAll)
	if s.failed("AUTH-2") {
		s.report()
	}
	s.run(ctx, "AUTH-3", "Anahtarsız/bozuk anahtarlı istek 401", s.authMissingToken)
	s.run(ctx, "AUTH-4", "Refresh token rotasyonu ve tekrar kullanım tespiti", s.authRefresh)
	s.run(ctx, "RBAC-1", "Hasta, hasta listesine erişemez (403)", s.rbacPatientList)
	s.run(ctx, "RBAC-2", "Hasta başka hastanın kaydını/defterini göremez (403)", s.rbacOtherPatient)
	s.run(ctx, "RBAC-3", "Asistan ödeme alamaz, resepsiyon plan oluşturamaz, hekim fiyat değiştiremez (403)", s.rbacRoles)

	s.run(ctx, "F-1", "Hasta kaydı: oluşturma, TC doğrulama, mükerrer kayıt, keyset arama", s.f1)
	s.run(ctx, "F-2", "Randevu ve ünite: müsaitlik, otomatik kaynak atama, listeleme, iptal, ünite yönetimi", s.f2)
	s.run(ctx, "F-3", "Diş haritası ve tedavi geçmişi: işlem sonrası durum ve zaman çizelgesi", s.f3)
	s.run(ctx, "F-4", "Tedavi planı ve seanslar: plan, onay, seans randevusu, tamamlama", s.f4)
	s.run(ctx, "F-5", "Ödeme ve taksit: peşinat + taksit, ödeme dağıtımı, cari hesap", s.f5)
	s.run(ctx, "F-6", "Sarf malzeme stoğu: liste, lot girişi, hareketler", s.f6)
	s.run(ctx, "F-7", "Raporlar: panel, gelir, hekim, ünite doluluğu, alacak, stok kullanımı", s.f7)

	s.run(ctx, "T-1", "Çakışmasız randevu: 20 eşzamanlı istek, ünite çakışması, süre, izin, seans aralığı, bekleme listesi", s.t1)
	s.run(ctx, "T-2", "Diş haritası/plan: FDI, çekilmiş dişe dolgu, kanal→kuron sıralama, döngü, sürüm ve onay", s.t2)
	s.run(ctx, "T-3", "Fiyat/faturalama: zaman geçerli fiyat, kurum indirimi, ilerleme faturası, idempotans, iade, defter tutarlılığı", s.t3)
	s.run(ctx, "T-4", "Stok: FEFO + SKT, minimum stok uyarısı, eşzamanlı tamamlama, geri almada iade", s.t4)
	if fe := os.Getenv("FRONTEND_URL"); fe != "" {
		s.run(ctx, "UI", "Arayüz servis ediliyor ve API vekili çalışıyor", func(ctx context.Context) error { return s.ui(ctx, fe) })
	}
	s.report()
}

func (s *suite) run(ctx context.Context, id, name string, fn func(context.Context) error) {
	start := time.Now()
	var err error
	func() {
		defer func() {
			if v := recover(); v != nil {
				err = fmt.Errorf("panic: %v", v)
			}
		}()
		err = fn(ctx)
	}()
	r := result{ID: id, Name: name, Err: err, Took: time.Since(start)}
	s.results = append(s.results, r)
	status := "PASS"
	if err != nil {
		status = "FAIL: " + err.Error()
	}
	fmt.Printf("  %-7s %s (%s)\n", id, status, r.Took.Round(time.Millisecond))
}

func (s *suite) failed(id string) bool {
	for _, r := range s.results {
		if r.ID == id {
			return r.Err != nil
		}
	}
	return false
}

func (s *suite) report() {
	fmt.Println()
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "KİMLİK\tSONUÇ\tSÜRE\tKAPSAM")
	fmt.Fprintln(tw, "------\t-----\t----\t------")
	fails := 0
	for _, r := range s.results {
		status := "PASS"
		if r.Err != nil {
			status = "FAIL"
			fails++
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.ID, status, r.Took.Round(time.Millisecond), r.Name)
	}
	_ = tw.Flush()
	if fails > 0 {
		fmt.Println("\nHatalar:")
		for _, r := range s.results {
			if r.Err != nil {
				fmt.Printf("  %s: %v\n", r.ID, r.Err)
			}
		}
		fmt.Printf("\nSONUÇ: %d/%d BAŞARISIZ\n", fails, len(s.results))
		os.Exit(1)
	}
	fmt.Printf("\nSONUÇ: %d/%d PASS\n", len(s.results), len(s.results))
	os.Exit(0)
}

func (s *suite) waitHealthy(ctx context.Context) error {
	c := apiclient.New(s.base)
	deadline := time.Now().Add(3 * time.Minute)
	for {
		resp, err := c.Do(ctx, http.MethodGet, "/health", nil)
		if err == nil && resp.Status == http.StatusOK {
			return nil
		}
		if time.Now().After(deadline) {
			if err != nil {
				return err
			}
			return fmt.Errorf("%s", resp)
		}
		time.Sleep(2 * time.Second)
	}
}

// ---------- helpers ----------

func fail(format string, args ...any) error { return fmt.Errorf(format, args...) }

func expectCode(resp apiclient.Response, err error, status int, code string) error {
	if err != nil {
		return err
	}
	if resp.Status != status || (code != "" && resp.ErrorCode() != code) {
		return fmt.Errorf("beklenen HTTP %d %s, gelen %s", status, code, resp)
	}
	return nil
}

func (s *suite) get(ctx context.Context, c *apiclient.Client, path string, v any) error {
	resp, err := c.Do(ctx, http.MethodGet, path, nil)
	return apiclient.Expect(resp, err, http.StatusOK, v)
}

func (s *suite) post(ctx context.Context, c *apiclient.Client, path string, body any, want int, v any, headers ...string) error {
	resp, err := c.Do(ctx, http.MethodPost, path, body, headers...)
	return apiclient.Expect(resp, err, want, v)
}

func randomTCKN() string {
	var p [9]int
	for i := range p {
		n, _ := rand.Int(rand.Reader, big.NewInt(10))
		p[i] = int(n.Int64())
	}
	if p[0] == 0 {
		p[0] = 7
	}
	return clinic.CompleteTCKN(p)
}

type patient struct {
	ID         int64  `json:"id"`
	NationalID string `json:"national_id"`
	LastName   string `json:"last_name"`
	DiscountBP int    `json:"discount_bp"`
}

func (s *suite) newPatient(ctx context.Context, institution *int64) (patient, error) {
	var p patient
	body := map[string]any{"national_id": randomTCKN(), "first_name": "Doğrulama", "last_name": "Testoğlu",
		"birth_date": "1990-05-17", "phone": "05551234567", "institution_id": institution}
	err := s.post(ctx, s.reception, "/api/patients", body, http.StatusCreated, &p)
	return p, err
}

type slot struct {
	DentistID   int64     `json:"dentist_id"`
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
	UnitID      int64     `json:"unit_id"`
	AssistantID int64     `json:"assistant_id"`
}

// findSlot returns the first free slot for dentist on day or the following working days.
func (s *suite) findSlot(ctx context.Context, code string, dentist int64, day time.Time, searchDays int) (slot, error) {
	for i := 0; i < searchDays; i++ {
		d := day.AddDate(0, 0, i)
		if d.Weekday() == time.Sunday {
			continue
		}
		var out struct {
			Items []slot `json:"items"`
		}
		path := fmt.Sprintf("/api/availability?date=%s&treatment_code=%s&dentist_id=%d", d.Format(time.DateOnly), code, dentist)
		if err := s.get(ctx, s.reception, path, &out); err != nil {
			return slot{}, err
		}
		if len(out.Items) > 0 {
			return out.Items[0], nil
		}
	}
	return slot{}, fail("%s için %s'den itibaren %d günde boş slot bulunamadı", code, day.Format(time.DateOnly), searchDays)
}

type appointment struct {
	ID          int64     `json:"id"`
	PatientID   int64     `json:"patient_id"`
	DentistID   int64     `json:"dentist_id"`
	AssistantID int64     `json:"assistant_id"`
	UnitID      int64     `json:"unit_id"`
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
	Status      string    `json:"status"`
	SessionNo   *int      `json:"session_no"`
}

func (s *suite) book(ctx context.Context, body map[string]any) (appointment, error) {
	var a appointment
	err := s.post(ctx, s.reception, "/api/appointments", body, http.StatusCreated, &a)
	return a, err
}

// bookNext books code for patient at the first free slot of the verification dentist.
func (s *suite) bookNext(ctx context.Context, patientID int64, code string, tooth *int, planItem *int64, from time.Time) (appointment, error) {
	sl, err := s.findSlot(ctx, code, s.dentistStaffID, from, 30)
	if err != nil {
		return appointment{}, err
	}
	body := map[string]any{"patient_id": patientID, "dentist_id": s.dentistStaffID, "treatment_code": code, "start": sl.Start}
	if tooth != nil {
		body["tooth"] = *tooth
	}
	if planItem != nil {
		body["plan_item_id"] = *planItem
	}
	return s.book(ctx, body)
}

type completion struct {
	ChargedKurus int64  `json:"charged_kurus"`
	BalanceAfter int64  `json:"balance_after"`
	ToothState   string `json:"tooth_state"`
	Status       string `json:"status"`
}

func (s *suite) complete(ctx context.Context, id int64) (completion, error) {
	var c completion
	err := s.post(ctx, s.assistant, fmt.Sprintf("/api/appointments/%d/complete", id), map[string]any{}, http.StatusOK, &c)
	return c, err
}

type planItem struct {
	ID            int64  `json:"id"`
	Seq           int    `json:"seq"`
	Tooth         *int   `json:"tooth"`
	TreatmentCode string `json:"treatment_code"`
	DependsOn     *int64 `json:"depends_on"`
	SessionsTotal int    `json:"sessions_total"`
	SessionsDone  int    `json:"sessions_done"`
	Unit          int64  `json:"unit_price_kurus"`
	Discount      int64  `json:"discount_kurus"`
	Net           int64  `json:"net_kurus"`
	Billed        int64  `json:"billed_kurus"`
	Status        string `json:"status"`
}

type plan struct {
	ID             int64  `json:"id"`
	Status         string `json:"status"`
	CurrentVersion int    `json:"current_version"`
	Versions       []struct {
		Version        int     `json:"version"`
		Gross          int64   `json:"gross_kurus"`
		Discount       int64   `json:"discount_kurus"`
		Net            int64   `json:"net_kurus"`
		ApprovalMethod *string `json:"approval_method"`
	} `json:"versions"`
	Items []planItem `json:"items"`
}

func (p plan) current() (gross, discount, net int64) {
	for _, v := range p.Versions {
		if v.Version == p.CurrentVersion {
			return v.Gross, v.Discount, v.Net
		}
	}
	return 0, 0, 0
}

func (p plan) item(code string) planItem {
	for _, it := range p.Items {
		if it.TreatmentCode == code {
			return it
		}
	}
	return planItem{}
}

func tooth(n int) *int { return &n }

func (s *suite) createPlan(ctx context.Context, patientID int64, items []map[string]any) (plan, error) {
	var p plan
	err := s.post(ctx, s.dentist, "/api/plans", map[string]any{"patient_id": patientID, "items": items}, http.StatusCreated, &p)
	return p, err
}

func (s *suite) approve(ctx context.Context, p plan) (plan, error) {
	var out plan
	err := s.post(ctx, s.reception, fmt.Sprintf("/api/plans/%d/approve", p.ID), map[string]any{"version": p.CurrentVersion}, http.StatusOK, &out)
	return out, err
}

func idemKey(prefix string) string {
	n, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	return fmt.Sprintf("verify-%s-%d", prefix, n)
}

func (s *suite) consistency(ctx context.Context) error {
	var c struct {
		OK                    bool  `json:"ok"`
		BalanceMismatches     int64 `json:"balance_mismatches"`
		RunningSumMismatches  int64 `json:"running_sum_mismatches"`
		PlanBillingMismatches int64 `json:"plan_billing_mismatches"`
	}
	if err := s.get(ctx, s.accounting, "/api/billing/consistency", &c); err != nil {
		return err
	}
	if !c.OK {
		return fail("defter tutarsız: %+v", c)
	}
	return nil
}

func sortedStatuses(st []int) []int {
	out := append([]int(nil), st...)
	sort.Ints(out)
	return out
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
