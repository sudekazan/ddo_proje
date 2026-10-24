package auth

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/sudekazan/ddo_proje/backend/internal/httpx"
)

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (r loginRequest) Validate() error {
	var v httpx.V
	v.Check(r.Email != "", "email", "zorunlu")
	v.Check(r.Password != "", "password", "zorunlu")
	return v.Err()
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

func (r refreshRequest) Validate() error {
	var v httpx.V
	v.Check(r.RefreshToken != "", "refresh_token", "zorunlu")
	return v.Err()
}

// Routes mounts the public authentication endpoints.
func (s *Service) Routes(r chi.Router) {
	r.Post("/login", func(w http.ResponseWriter, r *http.Request) {
		var req loginRequest
		if err := httpx.Decode(r, &req); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		pair, err := s.Login(r.Context(), req.Email, req.Password)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, pair)
	})
	r.Post("/refresh", func(w http.ResponseWriter, r *http.Request) {
		var req refreshRequest
		if err := httpx.Decode(r, &req); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		pair, err := s.Refresh(r.Context(), req.RefreshToken)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, pair)
	})
	r.Post("/logout", func(w http.ResponseWriter, r *http.Request) {
		var req refreshRequest
		if err := httpx.Decode(r, &req); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		if err := s.Logout(r.Context(), req.RefreshToken); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
