package httpapi

import (
	"context"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"rumarasa-backend/internal/auth"
)

type ctxKey int

const ctxAdminID ctxKey = iota

func adminIDFrom(ctx context.Context) (int, bool) {
	id, ok := ctx.Value(ctxAdminID).(int)
	return id, ok
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "default-src 'none'")
		if s.cfg.IsProd() {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

// cors implements an explicit-allowlist CORS policy; credentials are allowed
// because the refresh token travels in an httpOnly cookie.
func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && slices.Contains(s.cfg.CORSOrigins, origin) {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Credentials", "true")
			h.Add("Vary", "Origin")
			if r.Method == http.MethodOptions {
				h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
				h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
				h.Set("Access-Control-Max-Age", "86400")
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// requireAdmin validates the Bearer access token and stores the admin id in
// the request context.
func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		tokenString, ok := strings.CutPrefix(header, "Bearer ")
		if !ok || tokenString == "" {
			respondError(w, http.StatusUnauthorized, "unauthorized", "Missing bearer token")
			return
		}
		adminID, err := auth.ParseAccessToken(s.cfg.JWTSecret, tokenString)
		if err != nil {
			respondError(w, http.StatusUnauthorized, "unauthorized", "Invalid or expired token")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxAdminID, adminID)))
	})
}

// rateLimiter is a small fixed-window in-memory limiter, used only on auth
// endpoints. Good enough for a single-instance deployment.
type rateLimiter struct {
	mu      sync.Mutex
	max     int
	window  time.Duration
	buckets map[string][]time.Time
}

func newRateLimiter(max int, window time.Duration) *rateLimiter {
	return &rateLimiter{max: max, window: window, buckets: make(map[string][]time.Time)}
}

func (rl *rateLimiter) allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-rl.window)

	kept := rl.buckets[key][:0]
	for _, t := range rl.buckets[key] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= rl.max {
		rl.buckets[key] = kept
		return false
	}
	rl.buckets[key] = append(kept, now)

	// Opportunistic cleanup so idle IPs don't accumulate forever.
	if len(rl.buckets) > 10_000 {
		for k, ts := range rl.buckets {
			if len(ts) == 0 || !ts[len(ts)-1].After(cutoff) {
				delete(rl.buckets, k)
			}
		}
	}
	return true
}

func (s *Server) rateLimitAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			ip = r.RemoteAddr
		}
		if !s.authLimiter.allow(ip) {
			w.Header().Set("Retry-After", "60")
			respondError(w, http.StatusTooManyRequests, "rate_limited", "Too many attempts, try again in a minute")
			return
		}
		next.ServeHTTP(w, r)
	})
}
