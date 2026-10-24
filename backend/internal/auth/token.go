package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type claims struct {
	Role      Role  `json:"role"`
	StaffID   int64 `json:"sid,omitempty"`
	PatientID int64 `json:"pid,omitempty"`
	jwt.RegisteredClaims
}

const issuer = "dishekimim"

func signAccess(secret []byte, p Principal, ttl time.Duration, now time.Time) (string, error) {
	c := claims{
		Role: p.Role, StaffID: p.StaffID, PatientID: p.PatientID,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   strconv.FormatInt(p.UserID, 10),
			Issuer:    issuer,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(secret)
}

// ParseAccess validates signature, algorithm, issuer and expiry.
func ParseAccess(secret []byte, token string) (Principal, error) {
	var c claims
	_, err := jwt.ParseWithClaims(token, &c, func(*jwt.Token) (any, error) { return secret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(issuer), jwt.WithExpirationRequired())
	if err != nil {
		return Principal{}, err
	}
	uid, err := strconv.ParseInt(c.Subject, 10, 64)
	if err != nil || uid <= 0 {
		return Principal{}, errors.New("invalid subject")
	}
	return Principal{UserID: uid, Role: c.Role, StaffID: c.StaffID, PatientID: c.PatientID}, nil
}

// newRefreshToken returns an opaque token and the SHA-256 hash stored in the database.
func newRefreshToken() (string, []byte, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	tok := base64.RawURLEncoding.EncodeToString(b)
	return tok, hashToken(tok), nil
}

func hashToken(tok string) []byte {
	h := sha256.Sum256([]byte(tok))
	return h[:]
}
