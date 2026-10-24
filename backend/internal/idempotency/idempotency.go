// Package idempotency makes POST requests carrying an Idempotency-Key header safe to retry:
// the first response is stored and replayed for later requests with the same key.
package idempotency

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"io"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sudekazan/ddo_proje/backend/internal/auth"
	"github.com/sudekazan/ddo_proje/backend/internal/httpx"
)

const Header = "Idempotency-Key"

type Store struct{ Pool *pgxpool.Pool }

type recorder struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (r *recorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	r.body.Write(b)
	return r.ResponseWriter.Write(b)
}

// Middleware must be mounted after auth.Authenticate: keys are scoped per user.
func (s *Store) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get(Header)
		if r.Method != http.MethodPost || key == "" {
			next.ServeHTTP(w, r)
			return
		}
		if len(key) > 200 {
			httpx.WriteError(w, r, httpx.BadRequest("Idempotency-Key en fazla 200 karakter olabilir"))
			return
		}
		p := auth.MustFromContext(r.Context())
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			httpx.WriteError(w, r, httpx.BadRequest("istek gövdesi okunamadı"))
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		h := sha256.New()
		h.Write([]byte(r.Method + " " + r.URL.Path + "\n"))
		h.Write(body)
		hash := h.Sum(nil)

		ctx := r.Context()
		tag, err := s.Pool.Exec(ctx, `
			INSERT INTO "tn-idempotency_keys" (user_id, key, method, path, request_hash)
			VALUES ($1, $2, $3, $4, $5) ON CONFLICT DO NOTHING`,
			p.UserID, key, r.Method, r.URL.Path, hash)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		if tag.RowsAffected() == 0 {
			s.replay(w, r, p.UserID, key, hash)
			return
		}

		rec := &recorder{ResponseWriter: w}
		completed := false
		defer func() {
			// Server errors (and panics) release the key so the client may retry.
			if !completed {
				_, _ = s.Pool.Exec(ctx, `DELETE FROM "tn-idempotency_keys" WHERE user_id = $1 AND key = $2`, p.UserID, key)
			}
		}()
		next.ServeHTTP(rec, r)
		if rec.status >= 500 || rec.status == 0 {
			return
		}
		_, err = s.Pool.Exec(ctx, `UPDATE "tn-idempotency_keys" SET status_code = $3, response_body = $4
			WHERE user_id = $1 AND key = $2`, p.UserID, key, rec.status, rec.body.Bytes())
		completed = err == nil
	})
}

func (s *Store) replay(w http.ResponseWriter, r *http.Request, userID int64, key string, hash []byte) {
	var storedHash, body []byte
	var status *int
	err := s.Pool.QueryRow(r.Context(), `SELECT request_hash, status_code, response_body
		FROM "tn-idempotency_keys" WHERE user_id = $1 AND key = $2`, userID, key).Scan(&storedHash, &status, &body)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		httpx.WriteError(w, r, httpx.Conflict("IDEMPOTENCY_RETRY", "önceki istek başarısız oldu; tekrar deneyin"))
	case err != nil:
		httpx.WriteError(w, r, err)
	case !bytes.Equal(storedHash, hash):
		httpx.WriteError(w, r, httpx.Unprocessable("IDEMPOTENCY_KEY_REUSED",
			"bu Idempotency-Key farklı bir istekle kullanılmış"))
	case status == nil:
		httpx.WriteError(w, r, httpx.Conflict("IDEMPOTENCY_IN_PROGRESS", "aynı anahtarlı istek hâlâ işleniyor"))
	default:
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Idempotent-Replayed", "true")
		w.WriteHeader(*status)
		_, _ = w.Write(body)
	}
}
