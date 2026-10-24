// Package seed generates the deterministic demo dataset (same seed + same anchor date →
// identical data) and bulk-loads it with COPY.
package seed

import (
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/sudekazan/ddo_proje/backend/internal/billing"
	"github.com/sudekazan/ddo_proje/backend/internal/clinic"
	"github.com/sudekazan/ddo_proje/backend/internal/dental"
)

const (
	DemoPassword = "Demo123!"
	NumDentists  = 6
	NumPatients  = 20000
	HistoryYears = 3
	FutureDays   = 14
)

type User struct {
	ID       int64
	Email    string
	FullName string
	Role     string
}

type Staff struct {
	ID       int64
	FullName string
	Kind     string
	UserID   *int64
}

type Patient struct {
	ID            int64
	NationalID    string
	FirstName     string
	LastName      string
	BirthDate     time.Time
	Phone         string
	InstitutionID *int64
	UserID        *int64
	CreatedAt     time.Time
}

type Treatment struct {
	Code          string
	Name          string
	DurationMin   int
	Sessions      int
	MinGapDays    int
	RequiresTooth bool
	BasePriceTL   int64
}

type Price struct {
	Code  string
	Kurus int64
	From  time.Time
	To    *time.Time
}

type Appointment struct {
	ID          int64
	PatientID   int64
	DentistID   int64
	AssistantID int64
	UnitID      int64
	Code        string
	Tooth       *int
	Start       time.Time
	End         time.Time
	Status      string
	CreatedAt   time.Time
}

type ToothEvent struct {
	PatientID     int64
	Tooth         int
	Code          string
	Before, After string
	At            time.Time
	DentistID     int64
	AppointmentID int64
}

type Payment struct {
	ID         int64
	PatientID  int64
	Amount     int64
	Method     string
	ReceivedAt time.Time
}

type LedgerEntry struct {
	ID            int64
	PatientID     int64
	Kind          string
	Amount        int64
	BalanceAfter  int64
	AppointmentID *int64
	PaymentID     *int64
	Description   string
	At            time.Time
}

type Supply struct {
	ID       int64
	SKU      string
	Name     string
	Unit     string
	MinMilli int64
}

type Lot struct {
	SupplyID int64
	LotNo    string
	Expires  time.Time
	QtyMilli int64
}

type Recipe struct {
	Code     string
	SupplyID int64
	QtyMilli int64
}

type Leave struct {
	StaffID    int64
	Start, End time.Time
	Reason     string
}

type Dataset struct {
	Anchor       time.Time
	Users        []User
	Institutions [][3]any // id, name, discount_bp
	Staff        []Staff
	Units        []string
	Treatments   []Treatment
	Prices       []Price
	Patients     []Patient
	Appointments []Appointment
	ToothEvents  []ToothEvent
	ToothStatus  map[[2]int64]string // (patient, tooth) → state
	Payments     []Payment
	Ledger       []LedgerEntry
	Balances     map[int64]int64
	Supplies     []Supply
	Lots         []Lot
	Recipes      []Recipe
	Leaves       []Leave
}

var Treatments = []Treatment{
	{"EXAM", "Muayene", 15, 1, 0, false, 500},
	{"CLEANING", "Diş taşı temizliği", 30, 1, 0, false, 1200},
	{"FILLING", "Kompozit dolgu", 45, 1, 0, true, 2000},
	{"ROOT_CANAL", "Kanal tedavisi", 60, 3, 7, true, 4500},
	{"CROWN", "Zirkonyum kuron", 60, 2, 7, true, 6000},
	{"EXTRACTION", "Diş çekimi", 30, 1, 0, true, 1500},
	{"IMPLANT", "İmplant", 90, 2, 60, true, 18000},
}

var treatmentWeights = []struct {
	code   string
	weight int
}{{"EXAM", 25}, {"CLEANING", 20}, {"FILLING", 25}, {"ROOT_CANAL", 10}, {"EXTRACTION", 8}, {"CROWN", 8}, {"IMPLANT", 4}}

var firstNames = []string{"Ayşe", "Fatma", "Emine", "Hatice", "Zeynep", "Elif", "Merve", "Büşra", "Esra", "Selin",
	"Mehmet", "Mustafa", "Ahmet", "Ali", "Hüseyin", "Hasan", "İbrahim", "Murat", "Emre", "Burak",
	"Can", "Deniz", "Ece", "Gizem", "Kerem", "Oğuz", "Seda", "Tolga", "Yasemin", "Zehra"}

var lastNames = []string{"Yılmaz", "Kaya", "Demir", "Şahin", "Çelik", "Yıldız", "Yıldırım", "Öztürk", "Aydın", "Özdemir",
	"Arslan", "Doğan", "Kılıç", "Aslan", "Çetin", "Kara", "Koç", "Kurt", "Özkan", "Şimşek",
	"Polat", "Korkmaz", "Erdoğan", "Güneş", "Aksoy", "Tekin", "Bulut", "Ateş", "Keskin", "Uçar"}

func ptr[T any](v T) *T { return &v }

// Generate builds the dataset. History covers HistoryYears before anchor; booked appointments
// cover FutureDays after it. Everything derives from one PCG stream seeded with seed.
func Generate(seed uint64, anchor time.Time, loc *time.Location) *Dataset {
	r := rand.New(rand.NewPCG(seed, seed^0x9E3779B97F4A7C15))
	anchor = time.Date(anchor.Year(), anchor.Month(), anchor.Day(), 0, 0, 0, 0, loc)
	ds := &Dataset{Anchor: anchor, ToothStatus: map[[2]int64]string{}, Balances: map[int64]int64{}}

	// Users: the first user is the seed account, then one demo account per role.
	ds.Users = []User{
		{1, "seed.orman@ddo-case.invalid", "Seed Orman", "reception"},
		{2, "resepsiyon@dishekimim.local", "Derya Resepsiyon", "reception"},
		{3, "muhasebe@dishekimim.local", "Mert Muhasebe", "accounting"},
		{4, "asistan@dishekimim.local", "Aslı Asistan", "assistant"},
		{5, "hasta@dishekimim.local", "Ayşe Yılmaz", "patient"},
	}
	dentistNames := []string{"Dt. Hakan Ekinci", "Dt. Selma Tunç", "Dt. Orhan Baysal", "Dt. Pınar Akın", "Dt. Levent Saraç", "Dt. Nilay Ergin"}
	for i, n := range dentistNames {
		email := "hekim@dishekimim.local"
		if i > 0 {
			email = fmt.Sprintf("hekim%d@dishekimim.local", i+1)
		}
		uid := int64(6 + i)
		ds.Users = append(ds.Users, User{uid, email, n, "dentist"})
		ds.Staff = append(ds.Staff, Staff{ID: int64(i + 1), FullName: n, Kind: "dentist", UserID: ptr(uid)})
	}
	assistantNames := []string{"Aslı Asistan", "Burcu Er", "Cem Tan", "Dilek Ay", "Eren Sağ", "Figen Bal"}
	for i, n := range assistantNames {
		s := Staff{ID: int64(NumDentists + i + 1), FullName: n, Kind: "assistant"}
		if i == 0 {
			s.UserID = ptr(int64(4))
		}
		ds.Staff = append(ds.Staff, s)
	}
	for i := 1; i <= NumDentists; i++ {
		ds.Units = append(ds.Units, fmt.Sprintf("Ünite %d", i))
	}
	ds.Institutions = [][3]any{{int64(1), "Bandırma Öğretmenler Derneği", 1500}, {int64(2), "Marmara Sigorta A.Ş.", 1000}, {int64(3), "Liman İşletmesi Personeli", 2000}}
	ds.Treatments = Treatments

	// Yearly price lists with increases; the latest one is open ended.
	firstYear := anchor.Year() - HistoryYears
	for _, t := range Treatments {
		price := t.BasePriceTL
		for y := firstYear; y <= anchor.Year(); y++ {
			from := time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC)
			p := Price{Code: t.Code, Kurus: price * 100, From: from}
			if y < anchor.Year() {
				p.To = ptr(time.Date(y+1, 1, 1, 0, 0, 0, 0, time.UTC))
			}
			ds.Prices = append(ds.Prices, p)
			price = price * 135 / 100
		}
	}
	priceAt := func(code string, at time.Time) int64 {
		d := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.UTC)
		for _, p := range ds.Prices {
			if p.Code == code && !d.Before(p.From) && (p.To == nil || d.Before(*p.To)) {
				return p.Kurus
			}
		}
		return 0
	}

	// Patients with valid, unique national ids.
	seen := map[string]bool{}
	discount := map[int64]int{}
	historyStart := anchor.AddDate(-HistoryYears, 0, 0)
	for i := int64(1); i <= NumPatients; i++ {
		var tc string
		for {
			var p [9]int
			p[0] = 1 + r.IntN(9)
			for j := 1; j < 9; j++ {
				p[j] = r.IntN(10)
			}
			tc = clinic.CompleteTCKN(p)
			if !seen[tc] {
				seen[tc] = true
				break
			}
		}
		pt := Patient{
			ID: i, NationalID: tc,
			FirstName: firstNames[r.IntN(len(firstNames))], LastName: lastNames[r.IntN(len(lastNames))],
			BirthDate: time.Date(1950+r.IntN(56), time.Month(1+r.IntN(12)), 1+r.IntN(28), 0, 0, 0, 0, time.UTC),
			Phone:     fmt.Sprintf("05%09d", r.IntN(1_000_000_000)),
			CreatedAt: historyStart.Add(-time.Duration(r.IntN(365*24)) * time.Hour),
		}
		if r.IntN(100) < 15 {
			inst := int64(1 + r.IntN(3))
			pt.InstitutionID = &inst
			discount[i] = []int{1500, 1000, 2000}[inst-1]
		}
		if i == 1 {
			pt.FirstName, pt.LastName, pt.UserID = "Ayşe", "Yılmaz", ptr(int64(5))
		}
		ds.Patients = append(ds.Patients, pt)
	}

	tt := map[string]Treatment{}
	for _, t := range Treatments {
		tt[t.Code] = t
	}
	pickCode := func() string {
		n := r.IntN(100)
		for _, w := range treatmentWeights {
			if n < w.weight {
				return w.code
			}
			n -= w.weight
		}
		return "EXAM"
	}
	state := func(p int64, tooth int) string {
		if s, ok := ds.ToothStatus[[2]int64{p, int64(tooth)}]; ok {
			return s
		}
		return dental.Healthy
	}
	teeth := dental.AllPermanent()
	// pickTooth finds a tooth where code is clinically valid for this patient.
	pickTooth := func(p int64, code string) (int, bool) {
		for range 8 {
			t := teeth[r.IntN(len(teeth))]
			if _, err := dental.Apply(code, t, state(p, t)); err == nil {
				return t, true
			}
		}
		return 0, false
	}

	var apptID, eventPayID, ledgerID int64
	post := func(e LedgerEntry) {
		ledgerID++
		e.ID = ledgerID
		ds.Balances[e.PatientID] += e.Amount
		e.BalanceAfter = ds.Balances[e.PatientID]
		ds.Ledger = append(ds.Ledger, e)
	}
	methods := []string{"cash", "card", "card", "transfer"}

	// Curated history for the demo patient: an exam, a filling on 26 and a cleaning, each the
	// first appointment of dentist 1 on that day.
	curated := map[string]struct {
		code  string
		tooth *int
	}{}
	for _, c := range []struct {
		code  string
		tooth *int
		days  int
	}{{"EXAM", nil, 400}, {"FILLING", ptr(26), 380}, {"CLEANING", nil, 120}} {
		day := anchor.AddDate(0, 0, -c.days)
		for day.Weekday() == time.Sunday {
			day = day.AddDate(0, 0, 1)
		}
		curated[day.Format(time.DateOnly)] = struct {
			code  string
			tooth *int
		}{c.code, c.tooth}
	}

	// Walk every working day; each dentist works in their own unit with their own assistant.
	end := anchor.AddDate(0, 0, FutureDays)
	for day := historyStart; day.Before(end); day = day.AddDate(0, 0, 1) {
		if day.Weekday() == time.Sunday {
			continue
		}
		future := !day.Before(anchor)
		busyPatients := map[int64]bool{}
		for d := 0; d < NumDentists; d++ {
			if future && r.IntN(100) < 35 { // leave room in the future calendar
				continue
			}
			t := time.Date(day.Year(), day.Month(), day.Day(), 9, 0, 0, 0, loc)
			closing := time.Date(day.Year(), day.Month(), day.Day(), 18, 0, 0, 0, loc)
			if c, ok := curated[day.Format(time.DateOnly)]; ok && d == 0 {
				apptID++
				dur := time.Duration(tt[c.code].DurationMin) * time.Minute
				a := Appointment{ID: apptID, PatientID: 1, DentistID: 1, AssistantID: NumDentists + 1, UnitID: 1, Code: c.code,
					Tooth: c.tooth, Start: t, End: t.Add(dur), Status: "completed", CreatedAt: t.AddDate(0, 0, -7)}
				ds.Appointments = append(ds.Appointments, a)
				busyPatients[1] = true
				t = a.End
				if c.tooth != nil {
					ds.ToothEvents = append(ds.ToothEvents, ToothEvent{1, *c.tooth, c.code, dental.Healthy, dental.Filled, a.End, 1, a.ID})
					ds.ToothStatus[[2]int64{1, int64(*c.tooth)}] = dental.Filled
				}
				amount := priceAt(c.code, a.Start)
				post(LedgerEntry{PatientID: 1, Kind: "charge", Amount: amount, AppointmentID: ptr(a.ID), Description: tt[c.code].Name, At: a.End})
				eventPayID++
				ds.Payments = append(ds.Payments, Payment{eventPayID, 1, amount, "card", a.End.Add(5 * time.Minute)})
				post(LedgerEntry{PatientID: 1, Kind: "payment", Amount: -amount, PaymentID: ptr(eventPayID), Description: "Ödeme", At: a.End.Add(5 * time.Minute)})
			}
			for {
				t = t.Add(time.Duration([]int{0, 0, 15, 30, 60}[r.IntN(5)]) * time.Minute)
				if future {
					t = t.Add(time.Duration(r.IntN(3)) * 30 * time.Minute)
				}
				code := pickCode()
				dur := time.Duration(tt[code].DurationMin) * time.Minute
				if t.Add(dur).After(closing) {
					break
				}
				var pid int64
				for {
					pid = 2 + r.Int64N(NumPatients-1) // patient 1 (demo) gets curated data
					if !busyPatients[pid] {
						break
					}
				}
				busyPatients[pid] = true
				var tooth *int
				if tt[code].RequiresTooth {
					if tn, ok := pickTooth(pid, code); ok {
						tooth = &tn
					} else {
						code, dur = "EXAM", 15*time.Minute
					}
				}
				status := "booked"
				if !future {
					switch n := r.IntN(100); {
					case n < 4:
						status = "cancelled"
					case n < 7:
						status = "no_show"
					default:
						status = "completed"
					}
				}
				apptID++
				a := Appointment{ID: apptID, PatientID: pid, DentistID: int64(d + 1), AssistantID: int64(NumDentists + d + 1),
					UnitID: int64(d + 1), Code: code, Tooth: tooth, Start: t, End: t.Add(dur), Status: status,
					CreatedAt: t.Add(-time.Duration(1+r.IntN(20*24)) * time.Hour)}
				if a.CreatedAt.After(anchor) {
					a.CreatedAt = anchor.Add(-time.Duration(1+r.IntN(72)) * time.Hour)
				}
				ds.Appointments = append(ds.Appointments, a)
				t = t.Add(dur)

				if status != "completed" {
					continue
				}
				if tooth != nil {
					before := state(pid, *tooth)
					after, _ := dental.Apply(code, *tooth, before)
					ds.ToothEvents = append(ds.ToothEvents, ToothEvent{pid, *tooth, code, before, after, a.End, a.DentistID, a.ID})
					ds.ToothStatus[[2]int64{pid, int64(*tooth)}] = after
				}
				price := priceAt(code, a.Start)
				amount := price - billing.Discount(price, discount[pid])
				post(LedgerEntry{PatientID: pid, Kind: "charge", Amount: amount, AppointmentID: ptr(a.ID),
					Description: tt[code].Name, At: a.End})
				pay := amount
				switch n := r.IntN(100); {
				case n < 3:
					pay = 0
				case n < 10:
					pay = amount / 2
				}
				if pay > 0 {
					eventPayID++
					ds.Payments = append(ds.Payments, Payment{eventPayID, pid, pay, methods[r.IntN(len(methods))], a.End.Add(5 * time.Minute)})
					post(LedgerEntry{PatientID: pid, Kind: "payment", Amount: -pay, PaymentID: ptr(eventPayID),
						Description: "Ödeme", At: a.End.Add(5 * time.Minute)})
				}
			}
		}
	}

	// Inventory: several lots per supply with different expiry dates (one already expired).
	ds.Supplies = []Supply{
		{1, "KMP-A2", "Kompozit dolgu A2", "g", 50_000},
		{2, "BND-01", "Bonding ajanı", "ml", 20_000},
		{3, "ANS-ART", "Anestezi kartuşu (artikain)", "adet", 50_000},
		{4, "GTP-25", "Gütaperka kon #25", "adet", 100_000},
		{5, "NAOCL", "Sodyum hipoklorit %2,5", "ml", 500_000},
		{6, "OLC-SIL", "Silikon ölçü maddesi", "g", 200_000},
		{7, "STR-40", "Sütur 4/0", "adet", 20_000},
		{8, "IMP-4010", "İmplant 4.0x10 mm", "adet", 5_000},
		{9, "ELD-M", "Eldiven (çift)", "adet", 200_000},
		{10, "PRF-PST", "Profilaksi pastası", "g", 50_000},
	}
	ds.Recipes = []Recipe{
		{"EXAM", 9, 1000},
		{"CLEANING", 10, 5000}, {"CLEANING", 9, 1000},
		{"FILLING", 1, 500}, {"FILLING", 2, 100}, {"FILLING", 3, 1000}, {"FILLING", 9, 1000},
		{"ROOT_CANAL", 4, 4000}, {"ROOT_CANAL", 5, 10_000}, {"ROOT_CANAL", 3, 1000}, {"ROOT_CANAL", 9, 1000},
		{"CROWN", 6, 20_000}, {"CROWN", 9, 1000},
		{"EXTRACTION", 3, 2000}, {"EXTRACTION", 7, 1000}, {"EXTRACTION", 9, 1000},
		{"IMPLANT", 8, 1000}, {"IMPLANT", 3, 2000}, {"IMPLANT", 7, 1000}, {"IMPLANT", 9, 2000},
	}
	for _, s := range ds.Supplies {
		base := s.MinMilli * 6
		ds.Lots = append(ds.Lots,
			Lot{s.ID, fmt.Sprintf("%s-EXP", s.SKU), anchor.AddDate(0, -2, 0), s.MinMilli / 2},
			Lot{s.ID, fmt.Sprintf("%s-L1", s.SKU), anchor.AddDate(0, 4, 0), base / 3},
			Lot{s.ID, fmt.Sprintf("%s-L2", s.SKU), anchor.AddDate(1, 0, 0), base},
		)
	}
	ds.Leaves = []Leave{{StaffID: NumDentists, Start: anchor.AddDate(0, 0, 3), End: anchor.AddDate(0, 0, 5), Reason: "Kongre"}}
	// Leave must not collide with already booked future appointments of that dentist.
	kept := ds.Appointments[:0]
	for _, a := range ds.Appointments {
		l := ds.Leaves[0]
		if a.DentistID == l.StaffID && a.Status == "booked" && a.Start.Before(l.End) && l.Start.Before(a.End) {
			continue
		}
		kept = append(kept, a)
	}
	ds.Appointments = kept
	return ds
}
