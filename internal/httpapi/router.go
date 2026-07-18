package httpapi

import (
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"rumarasa-backend/internal/config"
	"rumarasa-backend/internal/store"
)

type Server struct {
	cfg         *config.Config
	store       *store.Store
	authLimiter *rateLimiter
}

func NewServer(cfg *config.Config, st *store.Store) *Server {
	return &Server{
		cfg:         cfg,
		store:       st,
		authLimiter: newRateLimiter(10, time.Minute),
	}
}

// Handler wires all routes. Reads are public (the landing page consumes them
// anonymously); every mutation requires an admin access token.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		respondData(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// Auth — separately and aggressively rate-limited
	mux.Handle("POST /api/v1/auth/login", s.rateLimitAuth(http.HandlerFunc(s.handleLogin)))
	mux.Handle("POST /api/v1/auth/refresh", s.rateLimitAuth(http.HandlerFunc(s.handleRefresh)))
	mux.HandleFunc("POST /api/v1/auth/logout", s.handleLogout)

	// Content blocks (click-to-edit text)
	mux.HandleFunc("GET /api/v1/content", s.handleListContent)
	mux.Handle("PUT /api/v1/content/{key}", s.admin(s.handleUpsertContent))
	mux.Handle("DELETE /api/v1/content/{key}", s.admin(s.handleDeleteContent))

	// Collections (dishes, promos, happenings, facilities, member-benefits)
	mux.HandleFunc("GET /api/v1/{collection}", s.handleListItems)
	mux.Handle("POST /api/v1/{collection}", s.admin(s.handleCreateItem))
	mux.Handle("PATCH /api/v1/{collection}/{id}", s.admin(s.handleUpdateItem))
	mux.Handle("DELETE /api/v1/{collection}/{id}", s.admin(s.handleDeleteItem))

	// Images (stored in Postgres)
	mux.HandleFunc("GET /api/v1/images/{id}", s.handleGetImage)
	mux.Handle("GET /api/v1/images", s.admin(s.handleListImages))
	mux.Handle("POST /api/v1/images", s.admin(s.handleUploadImage))
	mux.Handle("DELETE /api/v1/images/{id}", s.admin(s.handleDeleteImage))

	var h http.Handler = mux
	h = s.logRequests(h)
	h = s.cors(h)
	h = s.securityHeaders(h)
	h = s.recoverPanics(h)
	return h
}

func (s *Server) admin(fn http.HandlerFunc) http.Handler {
	return s.requireAdmin(fn)
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		slog.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

func (s *Server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic", "err", rec, "stack", string(debug.Stack()))
				respondError(w, http.StatusInternalServerError, "internal", "Internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
