package auth

import (
	"testing"
	"time"
)

var secret = []byte("unit-test-secret-0123456789abcdef0123")

func TestAccessTokenRoundTrip(t *testing.T) {
	p := Principal{UserID: 7, Role: RoleDentist, StaffID: 3}
	tok, err := signAccess(secret, p, time.Minute, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseAccess(secret, tok)
	if err != nil || got != p {
		t.Fatalf("got %+v err=%v", got, err)
	}
}

func TestAccessTokenRejectsTamperingExpiryAndWrongKey(t *testing.T) {
	p := Principal{UserID: 7, Role: RolePatient, PatientID: 9}
	tok, _ := signAccess(secret, p, time.Minute, time.Now())
	if _, err := ParseAccess([]byte("another-secret-0123456789abcdef0123"), tok); err == nil {
		t.Fatal("token accepted with a different key")
	}
	if _, err := ParseAccess(secret, tok[:len(tok)-2]+"xx"); err == nil {
		t.Fatal("tampered token accepted")
	}
	old, _ := signAccess(secret, p, time.Minute, time.Now().Add(-time.Hour))
	if _, err := ParseAccess(secret, old); err == nil {
		t.Fatal("expired token accepted")
	}
}

func TestPrincipalPatientScope(t *testing.T) {
	pat := Principal{Role: RolePatient, PatientID: 4}
	if !pat.CanSeePatient(4) || pat.CanSeePatient(5) {
		t.Fatal("patient scope wrong")
	}
	if !(Principal{Role: RoleReception}).CanSeePatient(5) {
		t.Fatal("staff should see patients")
	}
}

func TestPasswordHash(t *testing.T) {
	h, err := HashPassword("Demo123!")
	if err != nil || !CheckPassword(h, "Demo123!") || CheckPassword(h, "demo123!") {
		t.Fatal("bcrypt round trip failed")
	}
}
