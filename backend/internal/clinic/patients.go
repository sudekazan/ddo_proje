package clinic

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sudekazan/ddo_proje/backend/internal/audit"
	"github.com/sudekazan/ddo_proje/backend/internal/auth"
	"github.com/sudekazan/ddo_proje/backend/internal/httpx"
	"github.com/sudekazan/ddo_proje/backend/internal/platform"
)

type Handler struct {
	Pool *pgxpool.Pool
}

type Patient struct {
	ID            int64     `json:"id"`
	NationalID    string    `json:"national_id"`
	FirstName     string    `json:"first_name"`
	LastName      string    `json:"last_name"`
	BirthDate     string    `json:"birth_date"`
	Phone         string    `json:"phone"`
	Email         *string   `json:"email"`
	InstitutionID *int64    `json:"institution_id"`
	Institution   *string   `json:"institution"`
	DiscountBP    int       `json:"discount_bp"`
	BalanceKurus  int64     `json:"balance_kurus"`
	CreatedAt     time.Time `json:"created_at"`
}

const patientSelect = `
	SELECT p.id, p.national_id, p.first_name, p.last_name, to_char(p.birth_date, 'YYYY-MM-DD'), p.phone, p.email,
	       p.institution_id, i.name, COALESCE(i.discount_bp, 0), COALESCE(a.balance_kurus, 0), p.created_at
	FROM "tn-patients" p
	LEFT JOIN "tn-institutions" i ON i.id = p.institution_id
	LEFT JOIN "tn-patient_accounts" a ON a.patient_id = p.id`

func scanPatient(row pgx.Row) (Patient, error) {
	var p Patient
	err := row.Scan(&p.ID, &p.NationalID, &p.FirstName, &p.LastName, &p.BirthDate, &p.Phone, &p.Email,
		&p.InstitutionID, &p.Institution, &p.DiscountBP, &p.BalanceKurus, &p.CreatedAt)
	return p, err
}

var phoneRe = regexp.MustCompile(`^\+?[0-9]{10,15}$`)

type createPatientRequest struct {
	NationalID    string  `json:"national_id"`
	FirstName     string  `json:"first_name"`
	LastName      string  `json:"last_name"`
	BirthDate     string  `json:"birth_date"`
	Phone         string  `json:"phone"`
	Email         *string `json:"email"`
	InstitutionID *int64  `json:"institution_id"`
}

func (r *createPatientRequest) Validate() error {
	r.FirstName, r.LastName = strings.TrimSpace(r.FirstName), strings.TrimSpace(r.LastName)
	var v httpx.V
	v.Check(ValidTCKN(r.NationalID), "national_id", "geçerli bir TC kimlik numarası olmalı")
	v.Check(r.FirstName != "" && len(r.FirstName) <= 100, "first_name", "1-100 karakter olmalı")
	v.Check(r.LastName != "" && len(r.LastName) <= 100, "last_name", "1-100 karakter olmalı")
	bd, err := time.Parse(time.DateOnly, r.BirthDate)
	v.Check(err == nil && bd.Year() > 1900 && bd.Before(time.Now()), "birth_date", "YYYY-AA-GG biçiminde geçmiş bir tarih olmalı")
	v.Check(phoneRe.MatchString(r.Phone), "phone", "10-15 haneli telefon numarası olmalı")
	v.Check(r.Email == nil || strings.Contains(*r.Email, "@"), "email", "geçerli bir e-posta olmalı")
	return v.Err()
}

func (h *Handler) createPatient(w http.ResponseWriter, r *http.Request) error {
	var req createPatientRequest
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	actor := auth.MustFromContext(r.Context())
	var p Patient
	err := platform.InTx(r.Context(), h.Pool, func(tx pgx.Tx) error {
		var id int64
		err := tx.QueryRow(r.Context(), `
			INSERT INTO "tn-patients" (national_id, first_name, last_name, birth_date, phone, email, institution_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
			req.NationalID, req.FirstName, req.LastName, req.BirthDate, req.Phone, req.Email, req.InstitutionID).Scan(&id)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(r.Context(), `INSERT INTO "tn-patient_accounts" (patient_id) VALUES ($1)`, id); err != nil {
			return err
		}
		if p, err = scanPatient(tx.QueryRow(r.Context(), patientSelect+` WHERE p.id = $1`, id)); err != nil {
			return err
		}
		return audit.Write(r.Context(), tx, audit.Entry{Actor: actor.UserID, Action: "patient.create",
			Entity: "patient", EntityID: id, New: p})
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusCreated, p)
	return nil
}

// listPatients uses keyset pagination: newest first by id, or by (lower(last_name), id)
// when searching by surname prefix. An 11 digit q matches the national id exactly.
func (h *Handler) listPatients(w http.ResponseWriter, r *http.Request) error {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	limit := httpx.Limit(r, 25, 100)
	cursor := r.URL.Query().Get("cursor")
	var rows pgx.Rows
	var err error
	ctx := r.Context()
	switch {
	case len(q) == 11 && ValidTCKN(q):
		rows, err = h.Pool.Query(ctx, patientSelect+` WHERE p.national_id = $1`, q)
	case q != "":
		lastName, afterID := "", int64(0)
		if cursor != "" {
			lastName, afterID, err = decodeNameCursor(cursor)
			if err != nil {
				return err
			}
		}
		rows, err = h.Pool.Query(ctx, patientSelect+`
			WHERE lower(p.last_name) LIKE $1 AND ($2 = '' OR (lower(p.last_name), p.id) > ($2, $3))
			ORDER BY lower(p.last_name), p.id LIMIT $4`,
			likePrefix(strings.ToLower(q)), lastName, afterID, limit+1)
	default:
		before := int64(1 << 62)
		if cursor != "" {
			if before, err = strconv.ParseInt(cursor, 10, 64); err != nil {
				return httpx.BadRequest("cursor geçersiz")
			}
		}
		rows, err = h.Pool.Query(ctx, patientSelect+` WHERE p.id < $1 ORDER BY p.id DESC LIMIT $2`, before, limit+1)
	}
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Patient, error) { return scanPatient(row) })
	if err != nil {
		return err
	}
	page := httpx.Page[Patient]{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		last := page.Items[limit-1]
		if q != "" {
			page.NextCursor = encodeNameCursor(strings.ToLower(last.LastName), last.ID)
		} else {
			page.NextCursor = strconv.FormatInt(last.ID, 10)
		}
	}
	httpx.JSON(w, http.StatusOK, page)
	return nil
}

func likePrefix(s string) string {
	s = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
	return s + "%"
}

func encodeNameCursor(name string, id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(name + "\x00" + strconv.FormatInt(id, 10)))
}

func decodeNameCursor(c string) (string, int64, error) {
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err == nil {
		if name, idStr, ok := strings.Cut(string(b), "\x00"); ok {
			if id, err := strconv.ParseInt(idStr, 10, 64); err == nil {
				return name, id, nil
			}
		}
	}
	return "", 0, httpx.BadRequest("cursor geçersiz")
}

// GetPatient loads one patient; exported for other modules.
func GetPatient(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, id int64) (Patient, error) {
	p, err := scanPatient(q.QueryRow(ctx, patientSelect+` WHERE p.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return p, httpx.NotFound("hasta bulunamadı")
	}
	return p, err
}

// PatientIDParam parses {patientID} and enforces that patients only reach their own records.
func PatientIDParam(r *http.Request) (int64, error) {
	id, err := httpx.Int64Param(chi.URLParam(r, "patientID"), "patientID")
	if err != nil {
		return 0, err
	}
	if !auth.MustFromContext(r.Context()).CanSeePatient(id) {
		return 0, httpx.Forbidden()
	}
	return id, nil
}

func (h *Handler) getPatient(w http.ResponseWriter, r *http.Request) error {
	id, err := PatientIDParam(r)
	if err != nil {
		return err
	}
	p, err := GetPatient(r.Context(), h.Pool, id)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, p)
	return nil
}

type updatePatientRequest struct {
	Phone            *string `json:"phone"`
	Email            *string `json:"email"`
	InstitutionID    *int64  `json:"institution_id"`
	ClearInstitution bool    `json:"clear_institution"`
}

func (r updatePatientRequest) Validate() error {
	var v httpx.V
	v.Check(r.Phone == nil || phoneRe.MatchString(*r.Phone), "phone", "10-15 haneli telefon numarası olmalı")
	v.Check(r.Email == nil || strings.Contains(*r.Email, "@"), "email", "geçerli bir e-posta olmalı")
	return v.Err()
}

func (h *Handler) updatePatient(w http.ResponseWriter, r *http.Request) error {
	id, err := PatientIDParam(r)
	if err != nil {
		return err
	}
	var req updatePatientRequest
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	actor := auth.MustFromContext(r.Context())
	var after Patient
	err = platform.InTx(r.Context(), h.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `SELECT 1 FROM "tn-patients" WHERE id = $1 FOR UPDATE`, id); err != nil {
			return err
		}
		before, err := GetPatient(r.Context(), tx, id)
		if err != nil {
			return err
		}
		_, err = tx.Exec(r.Context(), `
			UPDATE "tn-patients" SET phone = COALESCE($2, phone), email = COALESCE($3, email),
			       institution_id = CASE WHEN $5 THEN NULL ELSE COALESCE($4, institution_id) END
			WHERE id = $1`, id, req.Phone, req.Email, req.InstitutionID, req.ClearInstitution)
		if err != nil {
			return err
		}
		if after, err = GetPatient(r.Context(), tx, id); err != nil {
			return err
		}
		return audit.Write(r.Context(), tx, audit.Entry{Actor: actor.UserID, Action: "patient.update",
			Entity: "patient", EntityID: id, Old: before, New: after})
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, after)
	return nil
}
