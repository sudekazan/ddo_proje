package scheduling

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sudekazan/ddo_proje/backend/internal/auth"
	"github.com/sudekazan/ddo_proje/backend/internal/httpx"
)

func (e *Engine) parseDay(s string) (time.Time, error) {
	d, err := time.ParseInLocation(time.DateOnly, s, e.Loc)
	if err != nil {
		return d, httpx.BadRequest("tarih YYYY-AA-GG biçiminde olmalı")
	}
	return d, nil
}

func (e *Engine) book(w http.ResponseWriter, r *http.Request) error {
	var req BookRequest
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	appt, err := e.Book(r.Context(), auth.MustFromContext(r.Context()), req)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusCreated, appt)
	return nil
}

func (e *Engine) list(w http.ResponseWriter, r *http.Request) error {
	p := auth.MustFromContext(r.Context())
	q := r.URL.Query()
	f := ListFilter{Status: q.Get("status"), Desc: q.Get("order") == "desc", Cursor: q.Get("cursor"), Limit: httpx.Limit(r, 50, 200)}
	if v := q.Get("date"); v != "" {
		d, err := e.parseDay(v)
		if err != nil {
			return err
		}
		to := d.AddDate(0, 0, 1)
		f.From, f.To = &d, &to
	}
	if v := q.Get("dentist_id"); v != "" {
		id, err := httpx.Int64Param(v, "dentist_id")
		if err != nil {
			return err
		}
		f.DentistID = id
	}
	if v := q.Get("patient_id"); v != "" {
		id, err := httpx.Int64Param(v, "patient_id")
		if err != nil {
			return err
		}
		f.PatientID = id
	}
	if p.Role == auth.RolePatient {
		if f.PatientID != 0 && f.PatientID != p.PatientID {
			return httpx.Forbidden()
		}
		f.PatientID = p.PatientID
	}
	page, err := e.List(r.Context(), f)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, page)
	return nil
}

func (e *Engine) get(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.Int64Param(chi.URLParam(r, "id"), "id")
	if err != nil {
		return err
	}
	a, err := e.Get(r.Context(), auth.MustFromContext(r.Context()), id)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, a)
	return nil
}

type cancelRequest struct {
	Reason string `json:"reason"`
}

func (e *Engine) cancel(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.Int64Param(chi.URLParam(r, "id"), "id")
	if err != nil {
		return err
	}
	var req cancelRequest
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	res, err := e.Cancel(r.Context(), auth.MustFromContext(r.Context()), id, req.Reason)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, res)
	return nil
}

func (e *Engine) availability(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	d, err := e.parseDay(q.Get("date"))
	if err != nil {
		return err
	}
	if q.Get("treatment_code") == "" {
		return (&httpx.V{}).ErrField("treatment_code", "zorunlu")
	}
	var dentistID int64
	if v := q.Get("dentist_id"); v != "" {
		if dentistID, err = httpx.Int64Param(v, "dentist_id"); err != nil {
			return err
		}
	}
	slots, err := e.Availability(r.Context(), d, q.Get("treatment_code"), dentistID)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": slots})
	return nil
}

func (e *Engine) addWaitlist(w http.ResponseWriter, r *http.Request) error {
	var req WaitlistRequest
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	out, err := e.AddWaitlist(r.Context(), auth.MustFromContext(r.Context()), req)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusCreated, out)
	return nil
}

func (e *Engine) listWaitlist(w http.ResponseWriter, r *http.Request) error {
	items, err := e.ListWaitlist(r.Context(), r.URL.Query().Get("status"))
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
	return nil
}

func (e *Engine) cancelWaitlist(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.Int64Param(chi.URLParam(r, "id"), "id")
	if err != nil {
		return err
	}
	if err := e.CancelWaitlist(r.Context(), auth.MustFromContext(r.Context()), id); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// Routes mounts the scheduling endpoints; r is authenticated and idempotency-aware.
func (e *Engine) Routes(r chi.Router) {
	booking := auth.Require(auth.RoleReception, auth.RolePatient)
	r.With(booking).Post("/appointments", httpx.HandlerFunc(e.book).ServeHTTP)
	r.Get("/appointments", httpx.HandlerFunc(e.list).ServeHTTP)
	r.Get("/appointments/{id}", httpx.HandlerFunc(e.get).ServeHTTP)
	r.With(booking).Post("/appointments/{id}/cancel", httpx.HandlerFunc(e.cancel).ServeHTTP)
	r.Get("/availability", httpx.HandlerFunc(e.availability).ServeHTTP)
	r.With(booking).Post("/waitlist", httpx.HandlerFunc(e.addWaitlist).ServeHTTP)
	r.With(auth.Require(auth.RoleReception, auth.RoleDentist)).Get("/waitlist", httpx.HandlerFunc(e.listWaitlist).ServeHTTP)
	r.With(booking).Post("/waitlist/{id}/cancel", httpx.HandlerFunc(e.cancelWaitlist).ServeHTTP)
}
