package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/sudekazan/ddo_proje/backend/internal/apiclient"
	"github.com/sudekazan/ddo_proje/backend/internal/seed"
)

// ---------- health, authentication, authorization ----------

func (s *suite) health(ctx context.Context) error {
	var h struct {
		Status   string `json:"status"`
		Database string `json:"database"`
	}
	if err := s.get(ctx, apiclient.New(s.base), "/health", &h); err != nil {
		return err
	}
	if h.Status != "ok" || h.Database != "ok" {
		return fail("sağlık durumu %+v", h)
	}
	return nil
}

func (s *suite) authWrongPassword(ctx context.Context) error {
	resp, err := apiclient.New(s.base).Login(ctx, "resepsiyon@dishekimim.local", "yanlis-parola")
	return expectCode(resp, err, http.StatusUnauthorized, "UNAUTHORIZED")
}

func (s *suite) loginAll(ctx context.Context) error {
	login := func(email string) (*apiclient.Client, error) {
		c := apiclient.New(s.base)
		resp, err := c.Login(ctx, email, seed.DemoPassword)
		if err := apiclient.Expect(resp, err, http.StatusOK, nil); err != nil {
			return nil, fmt.Errorf("%s: %w", email, err)
		}
		return c, nil
	}
	var err error
	if s.reception, err = login("resepsiyon@dishekimim.local"); err != nil {
		return err
	}
	if s.dentist, err = login("hekim@dishekimim.local"); err != nil {
		return err
	}
	if s.assistant, err = login("asistan@dishekimim.local"); err != nil {
		return err
	}
	if s.accounting, err = login("muhasebe@dishekimim.local"); err != nil {
		return err
	}
	if s.patient, err = login("hasta@dishekimim.local"); err != nil {
		return err
	}
	if _, err = login("seed.orman@ddo-case.invalid"); err != nil {
		return err
	}
	sid, ok1 := s.dentist.User["staff_id"].(float64)
	pid, ok2 := s.patient.User["patient_id"].(float64)
	if !ok1 || !ok2 {
		return fail("giriş yanıtında staff_id/patient_id yok")
	}
	s.dentistStaffID, s.demoPatientID = int64(sid), int64(pid)
	// Work far enough in the future to stay clear of the simulator's calendar.
	n := time.Now().In(s.loc)
	s.baseDay = time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, s.loc).AddDate(0, 0, 30+int(n.UnixNano()%200))
	return nil
}

func (s *suite) authMissingToken(ctx context.Context) error {
	c := apiclient.New(s.base)
	resp, err := c.Do(ctx, http.MethodGet, "/api/patients", nil)
	if err := expectCode(resp, err, http.StatusUnauthorized, "UNAUTHORIZED"); err != nil {
		return err
	}
	c.Token = s.reception.Token[:len(s.reception.Token)-4] + "abcd"
	resp, err = c.Do(ctx, http.MethodGet, "/api/patients", nil)
	return expectCode(resp, err, http.StatusUnauthorized, "UNAUTHORIZED")
}

func (s *suite) authRefresh(ctx context.Context) error {
	c := apiclient.New(s.base)
	resp, err := c.Login(ctx, "muhasebe@dishekimim.local", seed.DemoPassword)
	if err := apiclient.Expect(resp, err, http.StatusOK, nil); err != nil {
		return err
	}
	old := c.Refresh
	var pair struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := s.post(ctx, c, "/api/auth/refresh", map[string]string{"refresh_token": old}, http.StatusOK, &pair); err != nil {
		return err
	}
	if pair.RefreshToken == old || pair.AccessToken == "" {
		return fail("refresh token dönmedi")
	}
	// Re-using a rotated token is treated as theft: 401 and every session of the user is revoked.
	resp, err = c.Do(ctx, http.MethodPost, "/api/auth/refresh", map[string]string{"refresh_token": old})
	if err := expectCode(resp, err, http.StatusUnauthorized, ""); err != nil {
		return err
	}
	resp, err = c.Do(ctx, http.MethodPost, "/api/auth/refresh", map[string]string{"refresh_token": pair.RefreshToken})
	return expectCode(resp, err, http.StatusUnauthorized, "")
}

func (s *suite) rbacPatientList(ctx context.Context) error {
	resp, err := s.patient.Do(ctx, http.MethodGet, "/api/patients", nil)
	return expectCode(resp, err, http.StatusForbidden, "FORBIDDEN")
}

func (s *suite) rbacOtherPatient(ctx context.Context) error {
	other := s.demoPatientID + 1
	for _, path := range []string{"/api/patients/%d", "/api/patients/%d/ledger", "/api/patients/%d/teeth", "/api/patients/%d/plans"} {
		resp, err := s.patient.Do(ctx, http.MethodGet, fmt.Sprintf(path, other), nil)
		if err := expectCode(resp, err, http.StatusForbidden, "FORBIDDEN"); err != nil {
			return fmt.Errorf("%s: %w", fmt.Sprintf(path, other), err)
		}
	}
	// …but their own record is visible.
	return s.get(ctx, s.patient, fmt.Sprintf("/api/patients/%d", s.demoPatientID), nil)
}

func (s *suite) rbacRoles(ctx context.Context) error {
	cases := []struct {
		c    *apiclient.Client
		path string
		body any
	}{
		{s.assistant, "/api/payments", map[string]any{"patient_id": 2, "amount_kurus": 100, "method": "cash"}},
		{s.reception, "/api/plans", map[string]any{"patient_id": 2, "items": []map[string]any{{"treatment_code": "EXAM"}}}},
		{s.dentist, "/api/prices", map[string]any{"treatment_code": "EXAM", "price_kurus": 1, "valid_from": "2099-01-01"}},
		{s.accounting, "/api/appointments", map[string]any{"patient_id": 2, "dentist_id": 1, "treatment_code": "EXAM", "start": time.Now()}},
		{s.patient, "/api/inventory/supplies", nil},
	}
	for _, c := range cases {
		method := http.MethodPost
		if c.body == nil {
			method = http.MethodGet
		}
		resp, err := c.c.Do(ctx, method, c.path, c.body)
		if err := expectCode(resp, err, http.StatusForbidden, "FORBIDDEN"); err != nil {
			return fmt.Errorf("%s %s: %w", method, c.path, err)
		}
	}
	return nil
}

// ---------- F: functional scope ----------

func (s *suite) f1(ctx context.Context) error {
	p, err := s.newPatient(ctx, nil)
	if err != nil {
		return err
	}
	bad := map[string]any{"national_id": "12345678901", "first_name": "A", "last_name": "B", "birth_date": "1990-01-01", "phone": "05551234567"}
	resp, err := s.reception.Do(ctx, http.MethodPost, "/api/patients", bad)
	if err := expectCode(resp, err, http.StatusUnprocessableEntity, "VALIDATION_ERROR"); err != nil {
		return fmt.Errorf("geçersiz TC: %w", err)
	}
	dup := map[string]any{"national_id": p.NationalID, "first_name": "A", "last_name": "B", "birth_date": "1990-01-01", "phone": "05551234567"}
	resp, err = s.reception.Do(ctx, http.MethodPost, "/api/patients", dup)
	if err := expectCode(resp, err, http.StatusConflict, "DUPLICATE_PATIENT"); err != nil {
		return fmt.Errorf("mükerrer TC: %w", err)
	}
	var page1, page2 struct {
		Items      []patient `json:"items"`
		NextCursor string    `json:"next_cursor"`
	}
	if err := s.get(ctx, s.reception, "/api/patients?q=yıl&limit=3", &page1); err != nil {
		return err
	}
	if len(page1.Items) != 3 || page1.NextCursor == "" {
		return fail("keyset ilk sayfa beklenmedik: %d kayıt, cursor=%q", len(page1.Items), page1.NextCursor)
	}
	if err := s.get(ctx, s.reception, "/api/patients?q=yıl&limit=3&cursor="+page1.NextCursor, &page2); err != nil {
		return err
	}
	if len(page2.Items) == 0 || page2.Items[0].ID == page1.Items[2].ID {
		return fail("keyset ikinci sayfa ilk sayfayı tekrar ediyor")
	}
	var byTC struct {
		Items []patient `json:"items"`
	}
	if err := s.get(ctx, s.reception, "/api/patients?q="+p.NationalID, &byTC); err != nil {
		return err
	}
	if len(byTC.Items) != 1 || byTC.Items[0].ID != p.ID {
		return fail("TC ile arama hastayı bulamadı")
	}
	return nil
}

func (s *suite) f2(ctx context.Context) error {
	p, err := s.newPatient(ctx, nil)
	if err != nil {
		return err
	}
	a, err := s.bookNext(ctx, p.ID, "EXAM", nil, nil, s.baseDay)
	if err != nil {
		return err
	}
	if a.UnitID == 0 || a.AssistantID == 0 || a.Status != "booked" {
		return fail("kaynaklar atanmadı: %+v", a)
	}
	var day struct {
		Items []appointment `json:"items"`
	}
	path := fmt.Sprintf("/api/appointments?date=%s&dentist_id=%d", a.Start.In(s.loc).Format(time.DateOnly), s.dentistStaffID)
	if err := s.get(ctx, s.reception, path, &day); err != nil {
		return err
	}
	found := false
	for _, x := range day.Items {
		found = found || x.ID == a.ID
	}
	if !found {
		return fail("randevu günlük listede yok")
	}
	var cancelled struct {
		Appointment appointment `json:"appointment"`
	}
	if err := s.post(ctx, s.reception, fmt.Sprintf("/api/appointments/%d/cancel", a.ID), map[string]string{"reason": "doğrulama"}, http.StatusOK, &cancelled); err != nil {
		return err
	}
	if cancelled.Appointment.Status != "cancelled" {
		return fail("iptal sonrası durum %s", cancelled.Appointment.Status)
	}
	resp, err := s.reception.Do(ctx, http.MethodPost, fmt.Sprintf("/api/appointments/%d/cancel", a.ID), map[string]string{})
	if err := expectCode(resp, err, http.StatusConflict, "NOT_CANCELLABLE"); err != nil {
		return err
	}
	// Unit management: a new unit can be created and deactivated.
	var u struct {
		ID     int64 `json:"id"`
		Active bool  `json:"active"`
	}
	if err := s.post(ctx, s.reception, "/api/units", map[string]string{"name": "Doğrulama Ünitesi " + idemKey("u")}, http.StatusCreated, &u); err != nil {
		return err
	}
	resp, err = s.reception.Do(ctx, http.MethodPatch, fmt.Sprintf("/api/units/%d", u.ID), map[string]bool{"active": false})
	if err := apiclient.Expect(resp, err, http.StatusOK, &u); err != nil {
		return err
	}
	if u.Active {
		return fail("ünite pasifleştirilemedi")
	}
	return nil
}

func (s *suite) f3(ctx context.Context) error {
	p, err := s.newPatient(ctx, nil)
	if err != nil {
		return err
	}
	a, err := s.bookNext(ctx, p.ID, "FILLING", tooth(16), nil, s.baseDay)
	if err != nil {
		return err
	}
	c, err := s.complete(ctx, a.ID)
	if err != nil {
		return err
	}
	if c.ToothState != "filled" {
		return fail("16 numaralı diş durumu %q, beklenen filled", c.ToothState)
	}
	var chart struct {
		Permanent []struct {
			Tooth int    `json:"tooth"`
			State string `json:"state"`
		} `json:"permanent"`
		Primary []struct{} `json:"primary"`
	}
	if err := s.get(ctx, s.dentist, fmt.Sprintf("/api/patients/%d/teeth", p.ID), &chart); err != nil {
		return err
	}
	if len(chart.Permanent) != 32 || len(chart.Primary) != 20 {
		return fail("diş haritası 32+20 değil")
	}
	var events struct {
		Items []struct {
			TreatmentCode string `json:"treatment_code"`
			StateAfter    string `json:"state_after"`
			AppointmentID *int64 `json:"appointment_id"`
		} `json:"items"`
	}
	if err := s.get(ctx, s.dentist, fmt.Sprintf("/api/patients/%d/teeth/16/events", p.ID), &events); err != nil {
		return err
	}
	if len(events.Items) != 1 || events.Items[0].TreatmentCode != "FILLING" || events.Items[0].AppointmentID == nil || *events.Items[0].AppointmentID != a.ID {
		return fail("zaman çizelgesi beklenmedik: %+v", events.Items)
	}
	// Patients can read their own chart.
	return s.get(ctx, s.patient, fmt.Sprintf("/api/patients/%d/teeth", s.demoPatientID), nil)
}

func (s *suite) f4(ctx context.Context) error {
	p, err := s.newPatient(ctx, nil)
	if err != nil {
		return err
	}
	pl, err := s.createPlan(ctx, p.ID, []map[string]any{{"treatment_code": "CLEANING"}, {"treatment_code": "FILLING", "tooth": 21}})
	if err != nil {
		return err
	}
	if pl.Status != "draft" || len(pl.Items) != 2 {
		return fail("plan taslak değil: %+v", pl)
	}
	item := pl.item("FILLING")
	resp, err := s.reception.Do(ctx, http.MethodPost, "/api/appointments", map[string]any{"patient_id": p.ID, "dentist_id": s.dentistStaffID,
		"plan_item_id": item.ID, "start": s.baseDay.Add(10 * time.Hour)})
	if err := expectCode(resp, err, http.StatusConflict, "PLAN_NOT_APPROVED"); err != nil {
		return fmt.Errorf("onaysız plana seans: %w", err)
	}
	if pl, err = s.approve(ctx, pl); err != nil {
		return err
	}
	a, err := s.bookNext(ctx, p.ID, "FILLING", nil, &item.ID, s.baseDay)
	if err != nil {
		return err
	}
	if a.SessionNo == nil || *a.SessionNo != 1 {
		return fail("seans numarası atanmadı")
	}
	if _, err := s.complete(ctx, a.ID); err != nil {
		return err
	}
	var after plan
	if err := s.get(ctx, s.dentist, fmt.Sprintf("/api/plans/%d", pl.ID), &after); err != nil {
		return err
	}
	if after.item("FILLING").Status != "done" || after.Status != "in_progress" {
		return fail("plan ilerlemesi yanlış: plan=%s kalem=%s", after.Status, after.item("FILLING").Status)
	}
	var list struct {
		Items []plan `json:"items"`
	}
	if err := s.get(ctx, s.patient, fmt.Sprintf("/api/patients/%d/plans", s.demoPatientID), &list); err != nil {
		return err
	}
	if len(list.Items) == 0 {
		return fail("demo hastanın planı görünmüyor")
	}
	return nil
}

func (s *suite) f5(ctx context.Context) error {
	p, err := s.newPatient(ctx, nil)
	if err != nil {
		return err
	}
	pl, err := s.createPlan(ctx, p.ID, []map[string]any{{"treatment_code": "ROOT_CANAL", "tooth": 46}, {"treatment_code": "CROWN", "tooth": 46}})
	if err != nil {
		return err
	}
	if pl, err = s.approve(ctx, pl); err != nil {
		return err
	}
	_, _, net := pl.current()
	var inst struct {
		Items []struct {
			ID     int64  `json:"id"`
			Seq    int    `json:"seq"`
			Amount int64  `json:"amount_kurus"`
			Paid   int64  `json:"paid_kurus"`
			Status string `json:"status"`
		} `json:"items"`
	}
	down := int64(100_000)
	if err := s.post(ctx, s.accounting, fmt.Sprintf("/api/plans/%d/installments", pl.ID),
		map[string]any{"down_payment_kurus": down, "count": 3, "first_due_date": s.baseDay.Format(time.DateOnly)}, http.StatusCreated, &inst); err != nil {
		return err
	}
	var sum int64
	for _, i := range inst.Items {
		sum += i.Amount
	}
	if len(inst.Items) != 4 || inst.Items[0].Seq != 0 || inst.Items[0].Amount != down || sum != net {
		return fail("taksitler toplamı %d, plan neti %d (kalem=%d)", sum, net, len(inst.Items))
	}
	var pay struct {
		ID          int64 `json:"id"`
		Allocations []struct {
			Seq    int   `json:"seq"`
			Amount int64 `json:"amount_kurus"`
		} `json:"installment_allocations"`
	}
	amount := down + 1000
	if err := s.post(ctx, s.accounting, "/api/payments", map[string]any{"patient_id": p.ID, "plan_id": pl.ID, "amount_kurus": amount, "method": "card"},
		http.StatusCreated, &pay, "Idempotency-Key", idemKey("f5")); err != nil {
		return err
	}
	if len(pay.Allocations) != 2 || pay.Allocations[0].Seq != 0 || pay.Allocations[0].Amount != down || pay.Allocations[1].Amount != 1000 {
		return fail("ödeme dağıtımı yanlış: %+v", pay.Allocations)
	}
	var acc struct {
		Balance int64 `json:"balance_kurus"`
		Paid    int64 `json:"paid_kurus"`
	}
	if err := s.get(ctx, s.accounting, fmt.Sprintf("/api/patients/%d/account", p.ID), &acc); err != nil {
		return err
	}
	if acc.Paid != amount || acc.Balance != -amount {
		return fail("cari hesap: %+v", acc)
	}
	return nil
}

func (s *suite) f6(ctx context.Context) error {
	var sup struct {
		Items []struct {
			ID         int64 `json:"id"`
			StockMilli int64 `json:"stock_milli"`
		} `json:"items"`
	}
	if err := s.get(ctx, s.assistant, "/api/inventory/supplies", &sup); err != nil {
		return err
	}
	if len(sup.Items) == 0 {
		return fail("sarf malzeme listesi boş")
	}
	id, before := sup.Items[0].ID, sup.Items[0].StockMilli
	lot := map[string]any{"lot_no": idemKey("lot"), "expires_on": time.Now().AddDate(1, 0, 0).Format(time.DateOnly), "qty_milli": 5000}
	if err := s.post(ctx, s.assistant, fmt.Sprintf("/api/inventory/supplies/%d/lots", id), lot, http.StatusCreated, nil); err != nil {
		return err
	}
	if err := s.get(ctx, s.assistant, "/api/inventory/supplies", &sup); err != nil {
		return err
	}
	for _, it := range sup.Items {
		if it.ID == id && it.StockMilli != before+5000 {
			return fail("lot girişi stoğa yansımadı: %d → %d", before, it.StockMilli)
		}
	}
	var mv struct {
		Items []struct {
			Kind string `json:"kind"`
		} `json:"items"`
	}
	if err := s.get(ctx, s.assistant, "/api/inventory/movements?limit=5", &mv); err != nil {
		return err
	}
	if len(mv.Items) == 0 || mv.Items[0].Kind != "receive" {
		return fail("stok hareketi kaydedilmedi")
	}
	return s.get(ctx, s.assistant, "/api/inventory/recipes/FILLING", nil)
}

func (s *suite) f7(ctx context.Context) error {
	checks := []struct {
		c    *apiclient.Client
		path string
	}{
		{s.reception, "/api/reports/dashboard"},
		{s.accounting, "/api/reports/revenue"},
		{s.accounting, "/api/reports/dentists"},
		{s.reception, "/api/reports/units"},
		{s.accounting, "/api/reports/receivables"},
		{s.assistant, "/api/reports/stock-usage"},
	}
	for _, c := range checks {
		var out map[string]any
		if err := s.get(ctx, c.c, c.path, &out); err != nil {
			return fmt.Errorf("%s: %w", c.path, err)
		}
	}
	var rev struct {
		Items []struct {
			Month   string `json:"month"`
			Charged int64  `json:"charged_kurus"`
		} `json:"items"`
	}
	if err := s.get(ctx, s.accounting, "/api/reports/revenue?from="+time.Now().AddDate(-1, 0, 0).Format(time.DateOnly), &rev); err != nil {
		return err
	}
	if len(rev.Items) < 6 {
		return fail("son 1 yılın gelir raporu %d ay döndü", len(rev.Items))
	}
	resp, err := s.reception.Do(ctx, http.MethodGet, "/api/reports/revenue", nil)
	return expectCode(resp, err, http.StatusForbidden, "FORBIDDEN")
}

func (s *suite) ui(ctx context.Context, fe string) error {
	c := apiclient.New(fe)
	resp, err := c.Do(ctx, http.MethodGet, "/", nil)
	if err := apiclient.Expect(resp, err, http.StatusOK, nil); err != nil {
		return err
	}
	if !contains(string(resp.Body), "<div id=\"root\"") {
		return fail("index.html beklenen kök öğeyi içermiyor")
	}
	resp, err = c.Do(ctx, http.MethodGet, "/health", nil)
	return apiclient.Expect(resp, err, http.StatusOK, nil)
}
