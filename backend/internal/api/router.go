// Package api wires every module into the HTTP router.
package api

import (
	"context"
	_ "embed"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sudekazan/ddo_proje/backend/internal/auth"
	"github.com/sudekazan/ddo_proje/backend/internal/billing"
	"github.com/sudekazan/ddo_proje/backend/internal/clinic"
	"github.com/sudekazan/ddo_proje/backend/internal/dental"
	"github.com/sudekazan/ddo_proje/backend/internal/httpx"
	"github.com/sudekazan/ddo_proje/backend/internal/idempotency"
	"github.com/sudekazan/ddo_proje/backend/internal/inventory"
	"github.com/sudekazan/ddo_proje/backend/internal/platform"
	"github.com/sudekazan/ddo_proje/backend/internal/reports"
	"github.com/sudekazan/ddo_proje/backend/internal/scheduling"
	"github.com/sudekazan/ddo_proje/backend/internal/workflow"
)

//go:embed openapi.yaml
var openapiSpec []byte

const swaggerHTML = `<!doctype html><html><head><meta charset="utf-8"><title>DişHekimim API</title>
<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.17.14/swagger-ui.css"></head>
<body><div id="ui"></div><script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.17.14/swagger-ui-bundle.js"></script>
<script>SwaggerUIBundle({url: '/openapi.yaml', dom_id: '#ui'})</script></body></html>`

// nodeHeader stamps every API response with the X-DDO-Node header.
func nodeHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-DDO-Node", "6736b1")
		next.ServeHTTP(w, r)
	})
}

func NewRouter(cfg platform.Config, pool *pgxpool.Pool, log *slog.Logger) http.Handler {
	authSvc := &auth.Service{Pool: pool, Secret: cfg.JWTSecret, AccessTTL: cfg.AccessTTL, RefreshTTL: cfg.RefreshTTL}
	idem := &idempotency.Store{Pool: pool}

	r := chi.NewRouter()
	r.Use(nodeHeader, httpx.WithRequestID, httpx.AccessLog(log), httpx.Recover, httpx.CORS(cfg.CORSOrigin), httpx.Timeout(cfg.RequestTimeout))

	r.Get("/health", health(pool))
	r.Get("/_hz/6736", func(w http.ResponseWriter, _ *http.Request) {
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	r.Get("/openapi.yaml", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(openapiSpec)
	})
	r.Get("/docs", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(swaggerHTML))
	})

	r.Route("/api", func(r chi.Router) {
		r.Route("/auth", authSvc.Routes)
		r.Group(func(r chi.Router) {
			r.Use(authSvc.Authenticate, idem.Middleware)
			(&clinic.Handler{Pool: pool}).Routes(r)
			(&scheduling.Engine{Pool: pool, Loc: cfg.Location}).Routes(r)
			(&dental.Service{Pool: pool, Loc: cfg.Location}).Routes(r)
			(&billing.Service{Pool: pool, Loc: cfg.Location}).Routes(r)
			(&inventory.Service{Pool: pool, Loc: cfg.Location}).Routes(r)
			(&workflow.Service{Pool: pool, Loc: cfg.Location}).Routes(r)
			(&reports.Service{Pool: pool, Loc: cfg.Location}).Routes(r)
		})
	})
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteError(w, r, httpx.NotFound("uç nokta bulunamadı"))
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteError(w, r, httpx.E(http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "bu yöntem desteklenmiyor"))
	})
	return r
}

func health(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		start := time.Now()
		var migrated bool
		err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_tables WHERE tablename = 'tn-dk-users')`).Scan(&migrated)
		body := map[string]any{"status": "ok", "database": "ok", "db_latency_ms": time.Since(start).Milliseconds(), "time": time.Now().UTC()}
		status := http.StatusOK
		if err != nil || !migrated {
			body["status"], body["database"] = "degraded", "unavailable"
			status = http.StatusServiceUnavailable
		}
		httpx.JSON(w, status, body)
	}
}
