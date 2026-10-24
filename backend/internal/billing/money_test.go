package billing

import (
	"math/rand/v2"
	"slices"
	"testing"
)

func sum(xs []int64) int64 {
	var s int64
	for _, x := range xs {
		s += x
	}
	return s
}

func TestSplitSumsExactly(t *testing.T) {
	cases := []struct {
		total int64
		n     int
		want  []int64
	}{
		{100, 3, []int64{34, 33, 33}},
		{1000, 4, []int64{250, 250, 250, 250}},
		{1, 3, []int64{1, 0, 0}},
		{99999, 7, []int64{14286, 14286, 14286, 14286, 14285, 14285, 14285}},
	}
	for _, c := range cases {
		if got := Split(c.total, c.n); !slices.Equal(got, c.want) {
			t.Fatalf("Split(%d,%d)=%v want %v", c.total, c.n, got, c.want)
		}
	}
}

// Property: for any total and count, parts sum to the total and differ by at most 1 kuruş.
func TestSplitProperty(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for range 10000 {
		total := r.Int64N(1_000_000_000)
		n := 1 + r.IntN(36)
		parts := Split(total, n)
		if sum(parts) != total {
			t.Fatalf("sum mismatch total=%d n=%d", total, n)
		}
		if parts[0]-parts[n-1] > 1 {
			t.Fatalf("uneven split %v", parts)
		}
	}
}

func TestDiscountHalfUp(t *testing.T) {
	cases := map[int64]int64{333: 50, 330: 50, 329: 49} // %15: 49.95, 49.5, 49.35
	for amount, want := range cases {
		if got := Discount(amount, 1500); got != want {
			t.Fatalf("Discount(%d)=%d want %d", amount, got, want)
		}
	}
	if Discount(1000, 0) != 0 {
		t.Fatal("zero discount expected")
	}
}

func TestSessionSharesCoverItem(t *testing.T) {
	net := int64(250_001)
	var billed int64
	for k := 1; k <= 3; k++ {
		billed += SessionShare(net, 3, k)
	}
	if billed != net {
		t.Fatalf("sessions billed %d, item net %d", billed, net)
	}
	if SessionShare(net, 3, 4) != 0 {
		t.Fatal("out of range session must bill 0")
	}
}

func TestRefundOnCancel(t *testing.T) {
	if RefundOnCancel(50_000, 30_000) != 20_000 {
		t.Fatal("refund should be paid - billed")
	}
	if RefundOnCancel(10_000, 30_000) != 0 {
		t.Fatal("no refund when billed exceeds paid")
	}
}
