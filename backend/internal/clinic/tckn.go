// Package clinic manages patients, staff, units, staff leave and institutions.
package clinic

// ValidTCKN checks a Turkish national id (11 digits, first non-zero, two check digits).
func ValidTCKN(s string) bool {
	if len(s) != 11 || s[0] == '0' {
		return false
	}
	var d [11]int
	for i := range 11 {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
		d[i] = int(s[i] - '0')
	}
	odd := d[0] + d[2] + d[4] + d[6] + d[8]
	even := d[1] + d[3] + d[5] + d[7]
	if ((odd*7-even)%10+10)%10 != d[9] {
		return false
	}
	sum := 0
	for i := range 10 {
		sum += d[i]
	}
	return sum%10 == d[10]
}

// CompleteTCKN appends both check digits to a 9-digit prefix (used by the data generator).
func CompleteTCKN(prefix [9]int) string {
	odd := prefix[0] + prefix[2] + prefix[4] + prefix[6] + prefix[8]
	even := prefix[1] + prefix[3] + prefix[5] + prefix[7]
	d10 := ((odd*7-even)%10 + 10) % 10
	sum := d10
	b := make([]byte, 11)
	for i, v := range prefix {
		sum += v
		b[i] = byte('0' + v)
	}
	b[9] = byte('0' + d10)
	b[10] = byte('0' + sum%10)
	return string(b)
}
