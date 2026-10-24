// Package billing implements time-valid price lists, institution discounts, progress
// billing, installments, refunds and the patient ledger. All amounts are integer kuruş.
package billing

// Discount returns the discount for amount at bp basis points (1500 = %15),
// rounded half-up to the nearest kuruş.
func Discount(amount int64, bp int) int64 {
	if amount <= 0 || bp <= 0 {
		return 0
	}
	return (amount*int64(bp) + 5000) / 10000
}

// Split divides total into n parts that differ by at most one kuruş and always sum to
// total (largest remainder method). Earlier parts receive the extra kuruş.
func Split(total int64, n int) []int64 {
	if n <= 0 {
		return nil
	}
	parts := make([]int64, n)
	base, rem := total/int64(n), total%int64(n)
	for i := range parts {
		parts[i] = base
		if int64(i) < rem {
			parts[i]++
		}
	}
	return parts
}

// SessionShare is the amount billed for session k (1-based) of an item split into n sessions.
func SessionShare(total int64, n, k int) int64 {
	if k < 1 || k > n {
		return 0
	}
	return Split(total, n)[k-1]
}

// RefundOnCancel: whatever was paid towards the plan beyond the work already billed.
func RefundOnCancel(paid, billed int64) int64 {
	if paid > billed {
		return paid - billed
	}
	return 0
}
