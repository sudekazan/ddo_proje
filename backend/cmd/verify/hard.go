package main

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/sudekazan/ddo_proje/backend/internal/apiclient"
	"github.com/sudekazan/ddo_proje/backend/internal/billing"
)

// ---------- T-1: conflict-free scheduling ----------

func (s *suite) t1(ctx context.Context) error {
	// (a) 20 concurrent requests for the same dentist and slot: exactly one wins.
	sl, err := s.findSlot(ctx, "EXAM", s.dentistStaffID, s.baseDay.AddDate(0, 0, 1), 30)
	if err != nil {
		return err
	}
	const n = 20
	patients := make([]patient, n)
	for i := range patients {
		if patients[i], err = s.newPatient(ctx, nil); err != nil {
			return err
		}
	}
	statuses := make([]int, n)
	codes := make([]string, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := s.reception.Do(ctx, http.MethodPost, "/api/appointments", map[string]any{
				"patient_id": patients[i].ID, "dentist_id": s.dentistStaffID, "treatment_code": "EXAM", "start": sl.Start})
			if err == nil {
				statuses[i], codes[i] = resp.Status, resp.ErrorCode()
			}
		}()
	}
	wg.Wait()
	created := 0
	for i, st := range statuses {
		switch {
		case st == http.StatusCreated:
			created++
		case st == http.StatusConflict && codes[i] == "DENTIST_BUSY":
		default:
			return fail("beklenmeyen yanıt %d %s", st, codes[i])
		}
	}
	if created != 1 {
		return fail("20 eşzamanlı istekten %d tanesi başarılı (beklenen 1); durumlar=%v", created, sortedStatuses(statuses))
	}
	var day struct {
		Items []appointment `json:"items"`
	}
	path := fmt.Sprintf("/api/appointments?date=%s&dentist_id=%d&status=booked", sl.Start.In(s.loc).Format(time.DateOnly), s.dentistStaffID)
	if err := s.get(ctx, s.reception, path, &day); err != nil {
		return err
	}
	var winner appointment
	for _, a := range day.Items {
		if a.Start.Before(sl.End) && sl.Start.Before(a.End) {
			if winner.ID != 0 {
				return fail("aynı slotta iki aktif randevu: %d ve %d", winner.ID, a.ID)
			}
			winner = a
		}
	}

	// (b) A second dentist cannot take the same unit (or assistant) at the same time.
	other, err := s.newPatient(ctx, nil)
	if err != nil {
		return err
	}
	otherDentist := s.dentistStaffID%6 + 1
	resp, err := s.reception.Do(ctx, http.MethodPost, "/api/appointments", map[string]any{"patient_id": other.ID, "dentist_id": otherDentist,
		"treatment_code": "EXAM", "start": sl.Start, "unit_id": winner.UnitID})
	if err := expectCode(resp, err, http.StatusConflict, "UNIT_BUSY"); err != nil {
		return fmt.Errorf("ünite çakışması: %w", err)
	}
	resp, err = s.reception.Do(ctx, http.MethodPost, "/api/appointments", map[string]any{"patient_id": other.ID, "dentist_id": otherDentist,
		"treatment_code": "EXAM", "start": sl.Start, "assistant_id": winner.AssistantID})
	if err := expectCode(resp, err, http.StatusConflict, "ASSISTANT_BUSY"); err != nil {
		return fmt.Errorf("asistan çakışması: %w", err)
	}

	// (c) Duration comes from the treatment type (dolgu 45 dk, kanal 60 dk).
	a, err := s.bookNext(ctx, other.ID, "FILLING", tooth(24), nil, s.baseDay.AddDate(0, 0, 2))
	if err != nil {
		return err
	}
	if a.End.Sub(a.Start) != 45*time.Minute {
		return fail("dolgu süresi %s, beklenen 45dk", a.End.Sub(a.Start))
	}

	// (d) Dentist leave blocks booking.
	leaveDay, err := s.findSlot(ctx, "EXAM", s.dentistStaffID, s.baseDay.AddDate(0, 0, 40), 30)
	if err != nil {
		return err
	}
	ls := time.Date(leaveDay.Start.Year(), leaveDay.Start.Month(), leaveDay.Start.Day(), 0, 0, 0, 0, s.loc)
	resp, err = s.reception.Do(ctx, http.MethodPost, fmt.Sprintf("/api/staff/%d/leaves", s.dentistStaffID),
		map[string]any{"start": ls, "end": ls.AddDate(0, 0, 1), "reason": "doğrulama izni"})
	if err := apiclient.Expect(resp, err, http.StatusCreated, nil); err != nil {
		return fmt.Errorf("izin kaydı: %w", err)
	}
	resp, err = s.reception.Do(ctx, http.MethodPost, "/api/appointments", map[string]any{"patient_id": other.ID, "dentist_id": s.dentistStaffID,
		"treatment_code": "EXAM", "start": leaveDay.Start})
	if err := expectCode(resp, err, http.StatusConflict, "DENTIST_ON_LEAVE"); err != nil {
		return fmt.Errorf("izinli hekim: %w", err)
	}

	// (e) Root canal sessions need at least 7 days between them.
	rc, err := s.newPatient(ctx, nil)
	if err != nil {
		return err
	}
	pl, err := s.createPlan(ctx, rc.ID, []map[string]any{{"treatment_code": "ROOT_CANAL", "tooth": 46}})
	if err != nil {
		return err
	}
	if pl, err = s.approve(ctx, pl); err != nil {
		return err
	}
	item := pl.item("ROOT_CANAL").ID
	s1, err := s.bookNext(ctx, rc.ID, "ROOT_CANAL", nil, &item, s.baseDay.AddDate(0, 0, 3))
	if err != nil {
		return err
	}
	early, err := s.findSlot(ctx, "ROOT_CANAL", s.dentistStaffID, s1.Start.In(s.loc).AddDate(0, 0, 3), 1)
	if err == nil {
		resp, err = s.reception.Do(ctx, http.MethodPost, "/api/appointments", map[string]any{"patient_id": rc.ID, "dentist_id": s.dentistStaffID,
			"plan_item_id": item, "start": early.Start})
		if err := expectCode(resp, err, http.StatusConflict, "MIN_GAP_VIOLATION"); err != nil {
			return fmt.Errorf("3 gün sonra 2. seans: %w", err)
		}
	}
	d7 := s1.Start.In(s.loc)
	s2, err := s.bookNext(ctx, rc.ID, "ROOT_CANAL", nil, &item, time.Date(d7.Year(), d7.Month(), d7.Day(), 0, 0, 0, 0, s.loc).AddDate(0, 0, 7))
	if err != nil {
		return fmt.Errorf("7 gün sonra 2. seans: %w", err)
	}
	if s2.SessionNo == nil || *s2.SessionNo != 2 {
		return fail("2. seans numarası atanmadı")
	}

	// (f) Cancelling hands the slot to the first matching waitlist entry. Entries left behind by
	// an interrupted earlier run would legitimately be first in line, so they are withdrawn first.
	var waiting struct {
		Items []struct {
			ID          int64  `json:"id"`
			PatientName string `json:"patient_name"`
		} `json:"items"`
	}
	if err := s.get(ctx, s.reception, "/api/waitlist?status=waiting", &waiting); err != nil {
		return err
	}
	for _, w := range waiting.Items {
		if w.PatientName == "Doğrulama Testoğlu" {
			if err := s.post(ctx, s.reception, fmt.Sprintf("/api/waitlist/%d/cancel", w.ID), map[string]any{}, http.StatusNoContent, nil); err != nil {
				return err
			}
		}
	}
	wl, err := s.newPatient(ctx, nil)
	if err != nil {
		return err
	}
	holder, err := s.newPatient(ctx, nil)
	if err != nil {
		return err
	}
	booked, err := s.bookNext(ctx, holder.ID, "EXAM", nil, nil, s.baseDay.AddDate(0, 0, 5))
	if err != nil {
		return err
	}
	if err := s.post(ctx, s.reception, "/api/waitlist", map[string]any{"patient_id": wl.ID, "treatment_code": "EXAM", "dentist_id": s.dentistStaffID,
		"from": booked.Start.Add(-time.Hour), "to": booked.End.Add(time.Hour)}, http.StatusCreated, nil); err != nil {
		return err
	}
	var cancel struct {
		Waitlist *appointment `json:"waitlist_appointment"`
	}
	if err := s.post(ctx, s.reception, fmt.Sprintf("/api/appointments/%d/cancel", booked.ID), map[string]string{"reason": "doğrulama"}, http.StatusOK, &cancel); err != nil {
		return err
	}
	if cancel.Waitlist == nil || cancel.Waitlist.PatientID != wl.ID || !cancel.Waitlist.Start.Equal(booked.Start) {
		return fail("bekleme listesi devreye girmedi: %+v", cancel.Waitlist)
	}
	return nil
}

// ---------- T-2: tooth chart and treatment plans ----------

func (s *suite) t2(ctx context.Context) error {
	p, err := s.newPatient(ctx, nil)
	if err != nil {
		return err
	}
	for _, bad := range []int{19, 60, 86, 9} {
		resp, err := s.dentist.Do(ctx, http.MethodPost, "/api/plans", map[string]any{"patient_id": p.ID,
			"items": []map[string]any{{"treatment_code": "FILLING", "tooth": bad}}})
		if err := expectCode(resp, err, http.StatusUnprocessableEntity, "VALIDATION_ERROR"); err != nil {
			return fmt.Errorf("FDI %d: %w", bad, err)
		}
	}
	if _, err := s.createPlan(ctx, p.ID, []map[string]any{{"treatment_code": "FILLING", "tooth": 55}}); err != nil {
		return fmt.Errorf("süt dişi 55 kabul edilmeli: %w", err)
	}

	// An extracted tooth cannot be filled — neither in a plan nor in a booking.
	if err := s.post(ctx, s.dentist, fmt.Sprintf("/api/patients/%d/teeth/26/observations", p.ID),
		map[string]string{"state": "extracted", "note": "ilk muayenede eksik"}, http.StatusCreated, nil); err != nil {
		return err
	}
	resp, err := s.dentist.Do(ctx, http.MethodPost, "/api/plans", map[string]any{"patient_id": p.ID,
		"items": []map[string]any{{"treatment_code": "FILLING", "tooth": 26}}})
	if err := expectCode(resp, err, http.StatusUnprocessableEntity, "INVALID_TOOTH_PROCEDURE"); err != nil {
		return fmt.Errorf("çekilmiş dişe dolgu planı: %w", err)
	}
	sl, err := s.findSlot(ctx, "FILLING", s.dentistStaffID, s.baseDay.AddDate(0, 0, 6), 30)
	if err != nil {
		return err
	}
	resp, err = s.reception.Do(ctx, http.MethodPost, "/api/appointments", map[string]any{"patient_id": p.ID, "dentist_id": s.dentistStaffID,
		"treatment_code": "FILLING", "tooth": 26, "start": sl.Start})
	if err := expectCode(resp, err, http.StatusUnprocessableEntity, "INVALID_TOOTH_PROCEDURE"); err != nil {
		return fmt.Errorf("çekilmiş dişe dolgu randevusu: %w", err)
	}
	// Implant on the extracted tooth is valid, followed by a crown (implicit dependency).
	if _, err := s.createPlan(ctx, p.ID, []map[string]any{{"treatment_code": "CROWN", "tooth": 26}, {"treatment_code": "IMPLANT", "tooth": 26}}); err != nil {
		return fmt.Errorf("implant + kuron planı: %w", err)
	}

	// Ordering: crown listed first is placed after the root canal on the same tooth.
	pl, err := s.createPlan(ctx, p.ID, []map[string]any{{"treatment_code": "CROWN", "tooth": 36}, {"treatment_code": "ROOT_CANAL", "tooth": 36}})
	if err != nil {
		return err
	}
	rc, cr := pl.item("ROOT_CANAL"), pl.item("CROWN")
	if rc.Seq != 1 || cr.Seq != 2 || cr.DependsOn == nil || *cr.DependsOn != rc.ID {
		return fail("kanal→kuron sıralaması yanlış: kanal seq=%d kuron seq=%d dep=%v", rc.Seq, cr.Seq, cr.DependsOn)
	}
	resp, err = s.dentist.Do(ctx, http.MethodPost, "/api/plans", map[string]any{"patient_id": p.ID,
		"items": []map[string]any{{"treatment_code": "CLEANING", "depends_on": 2}, {"treatment_code": "EXAM", "depends_on": 1}}})
	if err := expectCode(resp, err, http.StatusUnprocessableEntity, "DEPENDENCY_CYCLE"); err != nil {
		return fmt.Errorf("döngüsel bağımlılık: %w", err)
	}

	// Versioning and consent: revising creates v2; approving the stale v1 fails.
	var v2 plan
	if err := s.post(ctx, s.dentist, fmt.Sprintf("/api/plans/%d/revisions", pl.ID), map[string]any{"note": "kuron yerine dolgu+kuron",
		"items": []map[string]any{{"treatment_code": "ROOT_CANAL", "tooth": 36}, {"treatment_code": "CROWN", "tooth": 36}, {"treatment_code": "CLEANING"}}},
		http.StatusCreated, &v2); err != nil {
		return err
	}
	if v2.CurrentVersion != 2 || len(v2.Versions) != 2 || v2.Status != "draft" {
		return fail("revizyon sürümü yanlış: v=%d sürümler=%d durum=%s", v2.CurrentVersion, len(v2.Versions), v2.Status)
	}
	resp, err = s.reception.Do(ctx, http.MethodPost, fmt.Sprintf("/api/plans/%d/approve", pl.ID), map[string]int{"version": 1})
	if err := expectCode(resp, err, http.StatusConflict, "PLAN_VERSION_MISMATCH"); err != nil {
		return fmt.Errorf("eski sürüm onayı: %w", err)
	}
	approved, err := s.approve(ctx, v2)
	if err != nil {
		return err
	}
	if approved.Status != "approved" || approved.Versions[1].ApprovalMethod == nil || *approved.Versions[1].ApprovalMethod != "signed_form" {
		return fail("onay kaydedilmedi")
	}
	// Crown cannot be booked before the root canal sessions are scheduled.
	crown := approved.item("CROWN").ID
	sl, err = s.findSlot(ctx, "CROWN", s.dentistStaffID, s.baseDay.AddDate(0, 0, 6), 30)
	if err != nil {
		return err
	}
	resp, err = s.reception.Do(ctx, http.MethodPost, "/api/appointments", map[string]any{"patient_id": p.ID, "dentist_id": s.dentistStaffID,
		"plan_item_id": crown, "start": sl.Start})
	if err := expectCode(resp, err, http.StatusConflict, "DEPENDENCY_NOT_MET"); err != nil {
		return fmt.Errorf("kanal öncesi kuron: %w", err)
	}
	var events struct {
		Items []struct {
			Tooth      int    `json:"tooth"`
			StateAfter string `json:"state_after"`
		} `json:"items"`
	}
	if err := s.get(ctx, s.dentist, fmt.Sprintf("/api/patients/%d/tooth-events", p.ID), &events); err != nil {
		return err
	}
	if len(events.Items) != 1 || events.Items[0].Tooth != 26 || events.Items[0].StateAfter != "extracted" {
		return fail("zaman çizelgesi: %+v", events.Items)
	}
	return nil
}

// ---------- T-3: pricing, progress billing, ledger ----------

func (s *suite) t3(ctx context.Context) error {
	// (a) Time-valid price list: a future price does not change today's price.
	type price struct {
		Code      string     `json:"treatment_code"`
		Price     int64      `json:"price_kurus"`
		ValidFrom time.Time  `json:"valid_from"`
		ValidTo   *time.Time `json:"valid_to"`
	}
	var hist struct {
		Items []price `json:"items"`
	}
	if err := s.get(ctx, s.accounting, "/api/prices?history=true", &hist); err != nil {
		return err
	}
	var today price
	var openFrom time.Time
	for _, p := range hist.Items {
		if p.Code == "EXAM" && p.ValidTo == nil {
			openFrom = p.ValidFrom
		}
	}
	var cur struct {
		Items []price `json:"items"`
	}
	if err := s.get(ctx, s.accounting, "/api/prices", &cur); err != nil {
		return err
	}
	for _, p := range cur.Items {
		if p.Code == "EXAM" {
			today = p
		}
	}
	future := time.Now().AddDate(2, 0, 0)
	if !openFrom.Before(future) {
		future = openFrom.AddDate(0, 0, 1)
	}
	newPrice := today.Price + 12_345
	if err := s.post(ctx, s.accounting, "/api/prices", map[string]any{"treatment_code": "EXAM", "price_kurus": newPrice,
		"valid_from": future.Format(time.DateOnly)}, http.StatusCreated, nil); err != nil {
		return err
	}
	if err := s.get(ctx, s.accounting, "/api/prices", &cur); err != nil {
		return err
	}
	for _, p := range cur.Items {
		if p.Code == "EXAM" && p.Price != today.Price {
			return fail("bugünkü fiyat değişti: %d → %d", today.Price, p.Price)
		}
	}
	if err := s.get(ctx, s.accounting, "/api/prices?date="+future.Format(time.DateOnly), &cur); err != nil {
		return err
	}
	for _, p := range cur.Items {
		if p.Code == "EXAM" && p.Price != newPrice {
			return fail("gelecek fiyat uygulanmadı")
		}
	}

	// (b) Institution discount + (c) progress billing over 3 root canal sessions.
	inst := int64(1)
	p, err := s.newPatient(ctx, &inst)
	if err != nil {
		return err
	}
	pl, err := s.createPlan(ctx, p.ID, []map[string]any{{"treatment_code": "ROOT_CANAL", "tooth": 37}})
	if err != nil {
		return err
	}
	it := pl.item("ROOT_CANAL")
	if want := billing.Discount(it.Unit, p.DiscountBP); p.DiscountBP == 0 || it.Discount != want || it.Net != it.Unit-want {
		return fail("kurum indirimi yanlış: birim=%d indirim=%d (beklenen %d, bp=%d)", it.Unit, it.Discount, want, p.DiscountBP)
	}
	gross, disc, net := pl.current()
	if gross-disc != net || net != it.Net {
		return fail("plan toplamları tutarsız")
	}
	if pl, err = s.approve(ctx, pl); err != nil {
		return err
	}
	// Peşinat and payment idempotency.
	key := idemKey("t3")
	body := map[string]any{"patient_id": p.ID, "plan_id": pl.ID, "amount_kurus": net / 2, "method": "transfer"}
	var pay1, pay2 struct {
		ID int64 `json:"id"`
	}
	if err := s.post(ctx, s.accounting, "/api/payments", body, http.StatusCreated, &pay1, "Idempotency-Key", key); err != nil {
		return err
	}
	resp, err := s.accounting.Do(ctx, http.MethodPost, "/api/payments", body, "Idempotency-Key", key)
	if err := apiclient.Expect(resp, err, http.StatusCreated, &pay2); err != nil {
		return err
	}
	if pay1.ID != pay2.ID || resp.Headers.Get("Idempotent-Replayed") != "true" {
		return fail("aynı Idempotency-Key farklı sonuç verdi: %d vs %d", pay1.ID, pay2.ID)
	}
	body["amount_kurus"] = net
	resp, err = s.accounting.Do(ctx, http.MethodPost, "/api/payments", body, "Idempotency-Key", key)
	if err := expectCode(resp, err, http.StatusUnprocessableEntity, "IDEMPOTENCY_KEY_REUSED"); err != nil {
		return err
	}

	day := s.baseDay.AddDate(0, 0, 8)
	var charges []int64
	for k := 1; k <= 3; k++ {
		a, err := s.bookNext(ctx, p.ID, "ROOT_CANAL", nil, &it.ID, day)
		if err != nil {
			return fmt.Errorf("seans %d: %w", k, err)
		}
		c, err := s.complete(ctx, a.ID)
		if err != nil {
			return fmt.Errorf("seans %d tamamlama: %w", k, err)
		}
		charges = append(charges, c.ChargedKurus)
		st := a.Start.In(s.loc)
		day = time.Date(st.Year(), st.Month(), st.Day(), 0, 0, 0, 0, s.loc).AddDate(0, 0, 7)
	}
	want := billing.Split(it.Net, 3)
	var total int64
	for i, c := range charges {
		total += c
		if c != want[i] {
			return fail("seans %d faturası %d, beklenen %d", i+1, c, want[i])
		}
	}
	if total != it.Net {
		return fail("seans faturaları toplamı %d ≠ kalem neti %d", total, it.Net)
	}
	var acc struct {
		Balance int64 `json:"balance_kurus"`
	}
	if err := s.get(ctx, s.accounting, fmt.Sprintf("/api/patients/%d/account", p.ID), &acc); err != nil {
		return err
	}
	if acc.Balance != net-net/2 {
		return fail("bakiye %d, beklenen %d", acc.Balance, net-net/2)
	}

	// (d) Cancelling a plan refunds what was paid beyond the work done.
	q, err := s.newPatient(ctx, nil)
	if err != nil {
		return err
	}
	pl2, err := s.createPlan(ctx, q.ID, []map[string]any{{"treatment_code": "CLEANING"}, {"treatment_code": "CROWN", "tooth": 11}})
	if err != nil {
		return err
	}
	if pl2, err = s.approve(ctx, pl2); err != nil {
		return err
	}
	_, _, net2 := pl2.current()
	if err := s.post(ctx, s.accounting, "/api/payments", map[string]any{"patient_id": q.ID, "plan_id": pl2.ID, "amount_kurus": net2, "method": "card"},
		http.StatusCreated, nil, "Idempotency-Key", idemKey("t3b")); err != nil {
		return err
	}
	cl := pl2.item("CLEANING").ID
	a, err := s.bookNext(ctx, q.ID, "CLEANING", nil, &cl, s.baseDay.AddDate(0, 0, 9))
	if err != nil {
		return err
	}
	done, err := s.complete(ctx, a.ID)
	if err != nil {
		return err
	}
	var cancel struct {
		Paid, Billed, Refund, Balance int64
	}
	var raw struct {
		Paid    int64 `json:"paid_kurus"`
		Billed  int64 `json:"billed_kurus"`
		Refund  int64 `json:"refund_kurus"`
		Balance int64 `json:"balance_after"`
	}
	if err := s.post(ctx, s.accounting, fmt.Sprintf("/api/plans/%d/cancel", pl2.ID), map[string]string{"reason": "hasta vazgeçti"}, http.StatusOK, &raw); err != nil {
		return err
	}
	cancel.Paid, cancel.Billed, cancel.Refund, cancel.Balance = raw.Paid, raw.Billed, raw.Refund, raw.Balance
	if cancel.Billed != done.ChargedKurus || cancel.Refund != net2-done.ChargedKurus || cancel.Balance != 0 {
		return fail("iade hesabı yanlış: %+v (seans faturası %d)", cancel, done.ChargedKurus)
	}

	// (e) Concurrent payments keep the ledger consistent.
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = s.accounting.Do(ctx, http.MethodPost, "/api/payments", map[string]any{"patient_id": q.ID, "amount_kurus": 333, "method": "cash"})
		}()
	}
	wg.Wait()
	if err := s.get(ctx, s.accounting, fmt.Sprintf("/api/patients/%d/account", q.ID), &acc); err != nil {
		return err
	}
	if acc.Balance != -3330 {
		return fail("eşzamanlı ödemeler sonrası bakiye %d, beklenen -3330", acc.Balance)
	}
	return s.consistency(ctx)
}

// ---------- T-4: consumable stock ----------

type lotView struct {
	ID       int64  `json:"id"`
	LotNo    string `json:"lot_no"`
	QtyMilli int64  `json:"qty_milli"`
}

func (s *suite) lots(ctx context.Context, supplyID int64) (map[string]int64, int64, error) {
	var out struct {
		Items []lotView `json:"items"`
	}
	if err := s.get(ctx, s.assistant, fmt.Sprintf("/api/inventory/supplies/%d/lots", supplyID), &out); err != nil {
		return nil, 0, err
	}
	m := map[string]int64{}
	var total int64
	for _, l := range out.Items {
		if l.QtyMilli < 0 {
			return nil, 0, fail("lot %s negatif: %d", l.LotNo, l.QtyMilli)
		}
		m[l.LotNo] = l.QtyMilli
		total += l.QtyMilli
	}
	return m, total, nil
}

func (s *suite) t4(ctx context.Context) error {
	// A dedicated supply makes the scenario independent of the seeded stock.
	tag := idemKey("sku")
	var sup struct {
		ID int64 `json:"id"`
	}
	if err := s.post(ctx, s.assistant, "/api/inventory/supplies", map[string]any{"sku": tag, "name": "Doğrulama kompoziti",
		"unit": "g", "min_stock_milli": 2000}, http.StatusCreated, &sup); err != nil {
		return err
	}
	now := time.Now()
	for _, l := range []map[string]any{
		{"lot_no": "EXPIRED", "expires_on": now.AddDate(0, 0, -10).Format(time.DateOnly), "qty_milli": 10_000},
		{"lot_no": "LATE", "expires_on": now.AddDate(1, 0, 0).Format(time.DateOnly), "qty_milli": 2000},
		{"lot_no": "SOON", "expires_on": now.AddDate(0, 1, 0).Format(time.DateOnly), "qty_milli": 1500},
	} {
		if err := s.post(ctx, s.assistant, fmt.Sprintf("/api/inventory/supplies/%d/lots", sup.ID), l, http.StatusCreated, nil); err != nil {
			return err
		}
	}
	// Temporarily make CLEANING consume exactly 1 g of it; the original recipe is restored afterwards.
	var orig struct {
		Items []struct {
			SupplyID int64 `json:"supply_id"`
			QtyMilli int64 `json:"qty_milli"`
		} `json:"items"`
	}
	if err := s.get(ctx, s.assistant, "/api/inventory/recipes/CLEANING", &orig); err != nil {
		return err
	}
	restore := func() {
		items := []map[string]any{}
		for _, it := range orig.Items {
			items = append(items, map[string]any{"supply_id": it.SupplyID, "qty_milli": it.QtyMilli})
		}
		_, _ = s.assistant.Do(context.Background(), http.MethodPut, "/api/inventory/recipes/CLEANING", map[string]any{"items": items})
	}
	defer restore()
	resp, err := s.assistant.Do(ctx, http.MethodPut, "/api/inventory/recipes/CLEANING",
		map[string]any{"items": []map[string]any{{"supply_id": sup.ID, "qty_milli": 1000}}})
	if err := apiclient.Expect(resp, err, http.StatusOK, nil); err != nil {
		return err
	}

	appts := make([]appointment, 7)
	for i := range appts {
		p, err := s.newPatient(ctx, nil)
		if err != nil {
			return err
		}
		if appts[i], err = s.bookNext(ctx, p.ID, "CLEANING", nil, nil, s.baseDay.AddDate(0, 0, 12)); err != nil {
			return err
		}
	}
	// (a) FEFO: the soonest non-expired lot is used; the expired lot is skipped.
	if _, err := s.complete(ctx, appts[0].ID); err != nil {
		return err
	}
	lots, _, err := s.lots(ctx, sup.ID)
	if err != nil {
		return err
	}
	if lots["SOON"] != 500 || lots["LATE"] != 2000 || lots["EXPIRED"] != 10_000 {
		return fail("FEFO/SKT ihlali: %v", lots)
	}
	// (b) Second completion spans lots (500 from SOON + 500 from LATE) and triggers the alert.
	if _, err := s.complete(ctx, appts[1].ID); err != nil {
		return err
	}
	lots, _, err = s.lots(ctx, sup.ID)
	if err != nil {
		return err
	}
	if lots["SOON"] != 0 || lots["LATE"] != 1500 {
		return fail("lotlar arası düşüm yanlış: %v", lots)
	}
	var alerts struct {
		Items []struct {
			SupplyID int64 `json:"supply_id"`
		} `json:"items"`
	}
	if err := s.get(ctx, s.assistant, "/api/inventory/alerts", &alerts); err != nil {
		return err
	}
	alerted := false
	for _, a := range alerts.Items {
		alerted = alerted || a.SupplyID == sup.ID
	}
	if !alerted {
		return fail("minimum stok uyarısı oluşmadı")
	}
	// (c) 5 concurrent completions with stock for one: exactly one succeeds, stock never negative.
	statuses := make([]int, 5)
	codes := make([]string, 5)
	var wg sync.WaitGroup
	for i := range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := s.assistant.Do(ctx, http.MethodPost, fmt.Sprintf("/api/appointments/%d/complete", appts[2+i].ID), map[string]any{})
			if err == nil {
				statuses[i], codes[i] = resp.Status, resp.ErrorCode()
			}
		}()
	}
	wg.Wait()
	ok := 0
	for i, st := range statuses {
		switch {
		case st == http.StatusOK:
			ok++
		case st == http.StatusConflict && codes[i] == "INSUFFICIENT_STOCK":
		default:
			return fail("beklenmeyen tamamlama yanıtı %d %s", st, codes[i])
		}
	}
	lots, total, err := s.lots(ctx, sup.ID)
	if err != nil {
		return err
	}
	if ok != 1 || lots["LATE"] != 500 || total != 10_500 {
		return fail("eşzamanlı tamamlama: %d başarılı (beklenen 1), lotlar=%v", ok, lots)
	}
	// (d) Reverting a wrong completion returns the stock to the same lot and reverses the charge.
	var rev struct {
		Status       string `json:"status"`
		ChargedKurus int64  `json:"charged_kurus"`
	}
	if err := s.post(ctx, s.dentist, fmt.Sprintf("/api/appointments/%d/revert", appts[0].ID), map[string]string{"reason": "yanlış hastaya işlendi"},
		http.StatusOK, &rev); err != nil {
		return err
	}
	lots, _, err = s.lots(ctx, sup.ID)
	if err != nil {
		return err
	}
	if rev.Status != "booked" || rev.ChargedKurus >= 0 || lots["SOON"] != 1000 {
		return fail("geri alma stoğu iade etmedi: %+v lotlar=%v", rev, lots)
	}
	var mv struct {
		Items []struct {
			Kind     string `json:"kind"`
			QtyMilli int64  `json:"qty_milli"`
		} `json:"items"`
	}
	if err := s.get(ctx, s.assistant, fmt.Sprintf("/api/inventory/movements?appointment_id=%d", appts[0].ID), &mv); err != nil {
		return err
	}
	kinds := map[string]int64{}
	for _, m := range mv.Items {
		kinds[m.Kind] += m.QtyMilli
	}
	if kinds["consume"] != -1000 || kinds["return"] != 1000 {
		return fail("stok hareketleri: %v", kinds)
	}
	return s.consistency(ctx)
}
