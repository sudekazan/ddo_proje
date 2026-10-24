// Package api wires every module into the HTTP router.
package api

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sudekazan/ddo_proje/backend/internal/platform"
)

func NewRouter(cfg platform.Config, pool *pgxpool.Pool, log *slog.Logger) http.Handler {
	r := chi.NewRouter()
	return r
}
