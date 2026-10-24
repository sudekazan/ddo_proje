package scheduling

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

type Interval struct{ Start, End time.Time }

func (i Interval) overlaps(s, e time.Time) bool { return i.Start.Before(e) && s.Before(i.End) }

func free(busy []Interval, s, e time.Time) bool {
	for _, b := range busy {
		if b.overlaps(s, e) {
			return false
		}
	}
	return true
}

type Resource struct {
	ID   int64
	Name string
}

// Busy holds occupied intervals per resource for one day (appointments and leave).
type Busy struct {
	Dentist   map[int64][]Interval
	Unit      map[int64][]Interval
	Assistant map[int64][]Interval
}

type Slot struct {
	DentistID   int64     `json:"dentist_id"`
	DentistName string    `json:"dentist_name"`
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
	UnitID      int64     `json:"unit_id"`
	AssistantID int64     `json:"assistant_id"`
}

// ComputeSlots lists every 15-minute start on day where the dentist, at least one unit and
// at least one assistant are free for the whole treatment duration.
func ComputeSlots(day time.Time, dur time.Duration, dentists []Resource, units, assistants []int64,
	busy Busy, notBefore time.Time, loc *time.Location) []Slot {
	y, m, d := day.In(loc).Date()
	if day.In(loc).Weekday() == time.Sunday {
		return []Slot{}
	}
	open := time.Date(y, m, d, OpenHour, 0, 0, 0, loc)
	closing := time.Date(y, m, d, CloseHour, 0, 0, 0, loc)
	slots := []Slot{}
	for _, dent := range dentists {
		for s := open; !s.Add(dur).After(closing); s = s.Add(SlotStep) {
			e := s.Add(dur)
			if s.Before(notBefore) || !free(busy.Dentist[dent.ID], s, e) {
				continue
			}
			unit := firstFree(units, busy.Unit, s, e)
			asst := firstFree(assistants, busy.Assistant, s, e)
			if unit == 0 || asst == 0 {
				continue
			}
			slots = append(slots, Slot{DentistID: dent.ID, DentistName: dent.Name, Start: s, End: e, UnitID: unit, AssistantID: asst})
		}
	}
	return slots
}

func firstFree(ids []int64, busy map[int64][]Interval, s, e time.Time) int64 {
	for _, id := range ids {
		if free(busy[id], s, e) {
			return id
		}
	}
	return 0
}

// Availability loads one day's state and computes free slots for a treatment.
func (e *Engine) Availability(ctx context.Context, date time.Time, code string, dentistID int64) ([]Slot, error) {
	y, m, d := date.Date()
	dayStart := time.Date(y, m, d, 0, 0, 0, 0, e.Loc)
	dayEnd := dayStart.AddDate(0, 0, 1)
	var slots []Slot
	err := pgx.BeginTxFunc(ctx, e.Pool, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		tt, err := loadTreatment(ctx, tx, code)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id, full_name FROM "tn-staff" WHERE kind = 'dentist' AND active AND ($1 = 0 OR id = $1) ORDER BY id`, dentistID)
		if err != nil {
			return err
		}
		dentists, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Resource])
		if err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `SELECT id FROM "tn-units" WHERE active ORDER BY id`)
		if err != nil {
			return err
		}
		units, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `SELECT id FROM "tn-staff" WHERE kind = 'assistant' AND active ORDER BY id`)
		if err != nil {
			return err
		}
		assistants, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return err
		}
		busy := Busy{Dentist: map[int64][]Interval{}, Unit: map[int64][]Interval{}, Assistant: map[int64][]Interval{}}
		rows, err = tx.Query(ctx, `SELECT dentist_id, assistant_id, unit_id, lower(during), upper(during) FROM "tn-appointments"
			WHERE status IN ('booked','completed') AND during && tstzrange($1, $2)`, dayStart, dayEnd)
		if err != nil {
			return err
		}
		var dID, aID, uID int64
		var s, en time.Time
		if _, err := pgx.ForEachRow(rows, []any{&dID, &aID, &uID, &s, &en}, func() error {
			iv := Interval{s, en}
			busy.Dentist[dID] = append(busy.Dentist[dID], iv)
			busy.Assistant[aID] = append(busy.Assistant[aID], iv)
			busy.Unit[uID] = append(busy.Unit[uID], iv)
			return nil
		}); err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `SELECT l.staff_id, s.kind, lower(l.during), upper(l.during) FROM "tn-staff_leaves" l
			JOIN "tn-staff" s ON s.id = l.staff_id WHERE l.during && tstzrange($1, $2)`, dayStart, dayEnd)
		if err != nil {
			return err
		}
		var kind string
		if _, err := pgx.ForEachRow(rows, []any{&dID, &kind, &s, &en}, func() error {
			if kind == "dentist" {
				busy.Dentist[dID] = append(busy.Dentist[dID], Interval{s, en})
			} else {
				busy.Assistant[dID] = append(busy.Assistant[dID], Interval{s, en})
			}
			return nil
		}); err != nil {
			return err
		}
		slots = ComputeSlots(dayStart, time.Duration(tt.DurationMin)*time.Minute, dentists, units, assistants, busy, e.now(), e.Loc)
		return nil
	})
	return slots, err
}
