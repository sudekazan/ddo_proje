// Package inventory implements consumable stock with lots, expiry dates (SKT),
// per-treatment recipes and FEFO consumption.
package inventory

// Lot is a stock lot candidate, already ordered by expiry (first-expired-first-out).
type Lot struct {
	ID       int64
	QtyMilli int64
}

// Take is how much to consume from one lot.
type Take struct {
	LotID    int64
	QtyMilli int64
}

// AllocateFEFO consumes need from lots in the given order. ok is false when the lots
// do not hold enough stock; no partial allocation is returned in that case.
func AllocateFEFO(lots []Lot, need int64) (takes []Take, ok bool) {
	for _, l := range lots {
		if need == 0 {
			break
		}
		if l.QtyMilli <= 0 {
			continue
		}
		q := min(l.QtyMilli, need)
		takes = append(takes, Take{LotID: l.ID, QtyMilli: q})
		need -= q
	}
	if need > 0 {
		return nil, false
	}
	return takes, true
}
