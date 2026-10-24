package clinic

import "testing"

func TestValidTCKN(t *testing.T) {
	if !ValidTCKN("10000000146") {
		t.Fatal("known valid number rejected")
	}
	for _, bad := range []string{"10000000147", "00000000146", "1000000014", "1000000014a"} {
		if ValidTCKN(bad) {
			t.Fatalf("%s accepted", bad)
		}
	}
}

func TestCompleteTCKNProducesValidNumbers(t *testing.T) {
	for i := 1; i < 2000; i++ {
		p := [9]int{1 + i%9, i % 10, (i / 10) % 10, (i / 100) % 10, 3, 4, 5, 6, i % 7}
		if s := CompleteTCKN(p); !ValidTCKN(s) {
			t.Fatalf("generated %s is invalid", s)
		}
	}
}
