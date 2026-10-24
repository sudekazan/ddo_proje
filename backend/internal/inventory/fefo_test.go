package inventory

import "testing"

func TestAllocateFEFOSpansLots(t *testing.T) {
	lots := []Lot{{ID: 1, QtyMilli: 300}, {ID: 2, QtyMilli: 0}, {ID: 3, QtyMilli: 1000}}
	takes, ok := AllocateFEFO(lots, 500)
	if !ok || len(takes) != 2 || takes[0] != (Take{1, 300}) || takes[1] != (Take{3, 200}) {
		t.Fatalf("takes=%v ok=%v", takes, ok)
	}
}

func TestAllocateFEFOInsufficient(t *testing.T) {
	if takes, ok := AllocateFEFO([]Lot{{ID: 1, QtyMilli: 100}}, 101); ok || takes != nil {
		t.Fatalf("expected failure, got %v", takes)
	}
}

func TestAllocateFEFOExact(t *testing.T) {
	takes, ok := AllocateFEFO([]Lot{{ID: 7, QtyMilli: 500}, {ID: 8, QtyMilli: 500}}, 500)
	if !ok || len(takes) != 1 || takes[0].LotID != 7 {
		t.Fatalf("takes=%v", takes)
	}
}
