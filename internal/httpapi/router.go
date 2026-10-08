package httpapi

import (
	"log/slog"
	"net/http"
	"runtime/debug"
	"sync"
	"time"

	"rumarasa-backend/internal/config"
	"rumarasa-backend/internal/mail"
	"rumarasa-backend/internal/store"
)

type Server struct {
	cfg         *config.Config
	store       *store.Store
	mailer      mail.Sender // nil when email is not configured
	http        *http.Client
	bg          sync.WaitGroup
	authLimiter *rateLimiter
	formLimiter *rateLimiter
}

func NewServer(cfg *config.Config, st *store.Store, mailer mail.Sender) *Server {
	return &Server{
		cfg:         cfg,
		store:       st,
		mailer:      mailer,
		http:        &http.Client{Timeout: 20 * time.Second},
		authLimiter: newRateLimiter(10, time.Minute),
		formLimiter: newRateLimiter(5, 10*time.Minute),
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
	mux.Handle("POST /api/v1/auth/login", s.rateLimit(s.authLimiter, http.HandlerFunc(s.handleLogin)))
	mux.Handle("POST /api/v1/auth/refresh", s.rateLimit(s.authLimiter, http.HandlerFunc(s.handleRefresh)))
	mux.HandleFunc("POST /api/v1/auth/logout", s.handleLogout)

	// Content blocks (click-to-edit text)
	mux.HandleFunc("GET /api/v1/content", s.handleListContent)
	mux.Handle("PUT /api/v1/content/{key}", s.admin(s.handleUpsertContent))
	mux.Handle("DELETE /api/v1/content/{key}", s.admin(s.handleDeleteContent))

	// Public submissions from the site's forms — rate-limited per IP
	mux.Handle("POST /api/v1/members", s.rateLimit(s.formLimiter, http.HandlerFunc(s.handleCreateMember)))
	mux.Handle("POST /api/v1/reservations", s.rateLimit(s.formLimiter, http.HandlerFunc(s.handleCreateReservation)))

	// Admin back office for those submissions
	mux.Handle("GET /api/v1/admin/summary", s.admin(s.handleAdminSummary))
	mux.Handle("GET /api/v1/admin/members", s.admin(s.handleListMembers))
	mux.Handle("GET /api/v1/admin/members/export", s.admin(s.handleExportMembers))
	mux.Handle("PATCH /api/v1/admin/members/{id}", s.admin(s.handleUpdateMember))
	mux.Handle("DELETE /api/v1/admin/members/{id}", s.admin(s.handleDeleteMember))
	mux.Handle("POST /api/v1/admin/members/{id}/card", s.admin(s.handleCreateMemberCard))
	mux.Handle("DELETE /api/v1/admin/members/{id}/card", s.admin(s.handleDeleteMemberCard))
	mux.Handle("POST /api/v1/admin/members/{id}/card/sent", s.admin(s.handleSendMemberCard))
	mux.Handle("POST /api/v1/admin/members/{id}/card/email", s.admin(s.handleEmailMemberCard))
	mux.Handle("GET /api/v1/admin/reservations", s.admin(s.handleListReservations))
	mux.Handle("PATCH /api/v1/admin/reservations/{id}", s.admin(s.handleUpdateReservation))
	mux.Handle("DELETE /api/v1/admin/reservations/{id}", s.admin(s.handleDeleteReservation))

	// Gift vouchers: issue, assign to a member, send, redeem at the cashier
	mux.Handle("GET /api/v1/admin/vouchers", s.admin(s.handleListVouchers))
	mux.Handle("POST /api/v1/admin/vouchers", s.admin(s.handleCreateVoucher))
	mux.Handle("PATCH /api/v1/admin/vouchers/{id}", s.admin(s.handleUpdateVoucher))
	mux.Handle("DELETE /api/v1/admin/vouchers/{id}", s.admin(s.handleDeleteVoucher))
	mux.Handle("POST /api/v1/admin/vouchers/{id}/sent", s.admin(s.handleSendVoucher))
	mux.Handle("POST /api/v1/admin/vouchers/{id}/redeem", s.admin(s.handleRedeemVoucher))
	mux.Handle("POST /api/v1/admin/vouchers/{id}/link", s.admin(s.handleCreateVoucherLink))
	mux.Handle("DELETE /api/v1/admin/vouchers/{id}/link", s.admin(s.handleDeleteVoucherLink))

	// Link vouchers for non-members: the secret token is the only key
	mux.HandleFunc("GET /api/v1/vouchers/link/{token}", s.handleGetVoucherByLink)
	mux.HandleFunc("GET /api/v1/members/card/{token}", s.handleGetMemberCard)
	mux.Handle("POST /api/v1/vouchers/link/{token}/redeem", s.rateLimit(s.formLimiter, http.HandlerFunc(s.handleRedeemVoucherByLink)))

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
