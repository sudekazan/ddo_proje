// Package auth implements password hashing, JWT access tokens, rotating refresh tokens
// and role based access control.
package auth

import (
	"context"
	"net/http"
	"slices"

	"github.com/sudekazan/ddo_proje/backend/internal/httpx"
)

type Role string

const (
	RolePatient    Role = "patient"
	RoleDentist    Role = "dentist"
	RoleAssistant  Role = "assistant"
	RoleReception  Role = "reception"
	RoleAccounting Role = "accounting"
)

// Staff lists every non-patient role.
var Staff = []Role{RoleDentist, RoleAssistant, RoleReception, RoleAccounting}

// Principal is the authenticated caller. StaffID/PatientID are 0 when not applicable.
type Principal struct {
	UserID    int64 `json:"user_id"`
	Role      Role  `json:"role"`
	StaffID   int64 `json:"staff_id,omitempty"`
	PatientID int64 `json:"patient_id,omitempty"`
}

func (p Principal) Is(roles ...Role) bool { return slices.Contains(roles, p.Role) }

// CanSeePatient: patients only see themselves; every staff role may see any patient.
func (p Principal) CanSeePatient(patientID int64) bool {
	if p.Role == RolePatient {
		return p.PatientID == patientID
	}
	return true
}

type ctxKey struct{}

func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}

// MustFromContext is used by handlers mounted behind Authenticate.
func MustFromContext(ctx context.Context) Principal {
	p, ok := FromContext(ctx)
	if !ok {
		panic("auth: principal missing; route is not behind Authenticate")
	}
	return p
}

// Require rejects callers whose role is not in roles with 403.
func Require(roles ...Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := FromContext(r.Context())
			if !ok {
				httpx.WriteError(w, r, httpx.Unauthorized("kimlik doğrulaması gerekli"))
				return
			}
			if !p.Is(roles...) {
				httpx.WriteError(w, r, httpx.Forbidden())
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
