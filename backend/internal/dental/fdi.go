// Package dental implements the FDI tooth chart, per-tooth treatment history and
// versioned treatment plans with dependency ordering.
package dental

// ValidFDI reports whether t is an FDI tooth number: permanent 11–48, primary 51–85.
// Mirrors the fdi_valid() SQL function used in CHECK constraints.
func ValidFDI(t int) bool {
	q, n := t/10, t%10
	switch {
	case q >= 1 && q <= 4:
		return n >= 1 && n <= 8
	case q >= 5 && q <= 8:
		return n >= 1 && n <= 5
	}
	return false
}

// IsPrimary reports whether t is a primary (süt) tooth.
func IsPrimary(t int) bool { return t/10 >= 5 }

// AllPermanent and AllPrimary list every tooth in chart order.
func AllPermanent() []int { return quadrantTeeth(1, 4, 8) }
func AllPrimary() []int   { return quadrantTeeth(5, 8, 5) }

func quadrantTeeth(from, to, n int) []int {
	out := make([]int, 0, (to-from+1)*n)
	for q := from; q <= to; q++ {
		for i := 1; i <= n; i++ {
			out = append(out, q*10+i)
		}
	}
	return out
}
