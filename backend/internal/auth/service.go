package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sudekazan/ddo_proje/backend/internal/audit"
	"github.com/sudekazan/ddo_proje/backend/internal/httpx"
	"github.com/sudekazan/ddo_proje/backend/internal/platform"
)

type Service struct {
	Pool       *pgxpool.Pool
	Secret     []byte
	AccessTTL  time.Duration
	RefreshTTL time.Duration
	Now        func() time.Time
}

type TokenPair struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresIn    int       `json:"expires_in"`
	User         UserInfo  `json:"user"`
	IssuedAt     time.Time `json:"-"`
}

type UserInfo struct {
	Principal
	Email    string `json:"email"`
	FullName string `json:"full_name"`
}

var errInvalidCredentials = httpx.Unauthorized("e-posta veya parola hatalı")

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func loadUser(ctx context.Context, q pgx.Tx, where string, arg any) (UserInfo, string, bool, error) {
	var u UserInfo
	var hash string
	var active bool
	var staffID, patientID *int64
	err := q.QueryRow(ctx, `
		SELECT u.id, u.role, u.email, u.full_name, u.password_hash, u.active,
		       (SELECT s.id FROM "tn-staff" s WHERE s.user_id = u.id),
		       (SELECT p.id FROM "tn-patients" p WHERE p.user_id = u.id)
		FROM "tn-dk-users" u WHERE `+where, arg).
		Scan(&u.UserID, &u.Role, &u.Email, &u.FullName, &hash, &active, &staffID, &patientID)
	if staffID != nil {
		u.StaffID = *staffID
	}
	if patientID != nil {
		u.PatientID = *patientID
	}
	return u, hash, active, err
}

func (s *Service) Login(ctx context.Context, email, password string) (TokenPair, error) {
	var pair TokenPair
	err := platform.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		u, hash, active, err := loadUser(ctx, tx, "u.email = $1", strings.ToLower(strings.TrimSpace(email)))
		if errors.Is(err, pgx.ErrNoRows) {
			_ = CheckPassword(string(dummyHash), password)
			return errInvalidCredentials
		}
		if err != nil {
			return err
		}
		if !CheckPassword(hash, password) || !active {
			return errInvalidCredentials
		}
		pair, err = s.issue(ctx, tx, u)
		if err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{Actor: u.UserID, Action: "login", Entity: "user", EntityID: u.UserID})
	})
	return pair, err
}

// Refresh rotates the refresh token. Presenting an already rotated token revokes every
// session of that user (token theft detection).
func (s *Service) Refresh(ctx context.Context, token string) (TokenPair, error) {
	var pair TokenPair
	var reused bool
	err := platform.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		var id, userID int64
		var expires time.Time
		var revoked *time.Time
		err := tx.QueryRow(ctx, `SELECT id, user_id, expires_at, revoked_at FROM "tn-refresh_tokens"
			WHERE token_hash = $1 FOR UPDATE`, hashToken(token)).Scan(&id, &userID, &expires, &revoked)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.Unauthorized("geçersiz yenileme anahtarı")
		}
		if err != nil {
			return err
		}
		if revoked != nil {
			reused = true
			_, err = tx.Exec(ctx, `UPDATE "tn-refresh_tokens" SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`, userID)
			return err
		}
		if s.now().After(expires) {
			return httpx.Unauthorized("yenileme anahtarının süresi dolmuş")
		}
		if _, err = tx.Exec(ctx, `UPDATE "tn-refresh_tokens" SET revoked_at = now() WHERE id = $1`, id); err != nil {
			return err
		}
		u, _, active, err := loadUser(ctx, tx, "u.id = $1", userID)
		if err != nil {
			return err
		}
		if !active {
			return httpx.Unauthorized("hesap pasif")
		}
		pair, err = s.issue(ctx, tx, u)
		return err
	})
	if err == nil && reused {
		return pair, httpx.Unauthorized("yenileme anahtarı tekrar kullanıldı; tüm oturumlar kapatıldı")
	}
	return pair, err
}

func (s *Service) Logout(ctx context.Context, token string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE "tn-refresh_tokens" SET revoked_at = now() WHERE token_hash = $1 AND revoked_at IS NULL`, hashToken(token))
	return err
}

func (s *Service) issue(ctx context.Context, tx pgx.Tx, u UserInfo) (TokenPair, error) {
	now := s.now()
	access, err := signAccess(s.Secret, u.Principal, s.AccessTTL, now)
	if err != nil {
		return TokenPair{}, err
	}
	refresh, hash, err := newRefreshToken()
	if err != nil {
		return TokenPair{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO "tn-refresh_tokens" (user_id, token_hash, expires_at) VALUES ($1, $2, $3)`,
		u.UserID, hash, now.Add(s.RefreshTTL))
	return TokenPair{AccessToken: access, RefreshToken: refresh, ExpiresIn: int(s.AccessTTL.Seconds()), User: u, IssuedAt: now}, err
}

// Authenticate requires a valid Bearer access token.
func (s *Service) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		tok, ok := strings.CutPrefix(h, "Bearer ")
		if !ok || tok == "" {
			httpx.WriteError(w, r, httpx.Unauthorized("kimlik doğrulaması gerekli"))
			return
		}
		p, err := ParseAccess(s.Secret, tok)
		if err != nil {
			httpx.WriteError(w, r, httpx.Unauthorized("geçersiz veya süresi dolmuş erişim anahtarı"))
			return
		}
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
	})
}
