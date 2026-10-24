// Package httpx contains the HTTP plumbing shared by every handler: the error format,
// JSON helpers, input validation and middleware.
package httpx

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Error is a client-facing error rendered as {"error":{"code":"…","message":"…"}}.
type Error struct {
	Status  int               `json:"-"`
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Details map[string]string `json:"details,omitempty"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func E(status int, code, msg string) *Error { return &Error{Status: status, Code: code, Message: msg} }

func BadRequest(msg string) *Error     { return E(http.StatusBadRequest, "BAD_REQUEST", msg) }
func NotFound(msg string) *Error       { return E(http.StatusNotFound, "NOT_FOUND", msg) }
func Forbidden() *Error                { return E(http.StatusForbidden, "FORBIDDEN", "bu işlem için yetkiniz yok") }
func Unauthorized(msg string) *Error   { return E(http.StatusUnauthorized, "UNAUTHORIZED", msg) }
func Conflict(code, msg string) *Error { return E(http.StatusConflict, code, msg) }
func Unprocessable(code, msg string) *Error {
	return E(http.StatusUnprocessableEntity, code, msg)
}

// constraintMessages turns database constraint names into user-facing conflicts.
var constraintMessages = map[string]*Error{
	"appt_no_dentist_overlap":      Conflict("DENTIST_BUSY", "hekimin bu saatte başka bir randevusu var"),
	"appt_no_assistant_overlap":    Conflict("ASSISTANT_BUSY", "asistanın bu saatte başka bir randevusu var"),
	"appt_no_unit_overlap":         Conflict("UNIT_BUSY", "ünite bu saatte dolu"),
	"appt_no_patient_overlap":      Conflict("PATIENT_BUSY", "hastanın bu saatte başka bir randevusu var"),
	"appointments_plan_session_uq": Conflict("SESSION_ALREADY_BOOKED", "bu seans için zaten randevu var"),
	"staff_leaves_no_overlap":      Conflict("LEAVE_OVERLAP", "bu tarihlerde zaten izin kaydı var"),
	"price_list_no_overlap":        Conflict("PRICE_OVERLAP", "bu tarih aralığında zaten fiyat tanımlı"),
	"patients_national_id_key":     Conflict("DUPLICATE_PATIENT", "bu TC kimlik numarası ile kayıtlı hasta var"),
	"supply_lots_qty_milli_check":  Conflict("INSUFFICIENT_STOCK", "stok yetersiz"),
}

// FromDB maps database errors to client errors; it returns nil when err is not recognised.
func FromDB(err error) *Error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		if e, ok := constraintMessages[pgErr.ConstraintName]; ok {
			return e
		}
		switch pgErr.Code {
		case "23P01":
			return Conflict("RESOURCE_CONFLICT", "kaynak çakışması")
		case "23505":
			return Conflict("DUPLICATE", "kayıt zaten mevcut")
		case "23503":
			return Unprocessable("INVALID_REFERENCE", "ilişkili kayıt bulunamadı")
		case "23514", "22P02", "22007", "22008":
			return Unprocessable("CONSTRAINT_VIOLATION", "geçersiz değer: "+pgErr.ConstraintName)
		}
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return NotFound("kayıt bulunamadı")
	}
	return nil
}

// WriteError renders err in the standard format. Unknown errors become 500 and are logged.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	var he *Error
	switch {
	case errors.As(err, &he):
	case FromDB(err) != nil:
		he = FromDB(err)
	case errors.Is(err, context.DeadlineExceeded):
		he = E(http.StatusServiceUnavailable, "TIMEOUT", "istek zaman aşımına uğradı")
	default:
		slog.ErrorContext(r.Context(), "unhandled error", "error", err.Error(),
			"request_id", RequestID(r.Context()), "path", r.URL.Path)
		he = E(http.StatusInternalServerError, "INTERNAL", "beklenmeyen bir hata oluştu")
	}
	JSON(w, he.Status, map[string]*Error{"error": he})
}
