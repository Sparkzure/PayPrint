package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Sparkzure/PayPrint/server/internal/domain"
	"github.com/Sparkzure/PayPrint/server/internal/service"
)

type ctxKey string

const (
	ctxKeySession   ctxKey = "session"
	ctxKeyRequestID ctxKey = "request_id"
	ctxKeyAdmin     ctxKey = "admin_actor"
)

const (
	headerFingerprint = "X-Device-Fingerprint"
	headerTimestamp   = "X-Device-Timestamp"
	headerNonce       = "X-Device-Nonce"
	headerSignature   = "X-Device-Signature"
	headerRequestID   = "X-Request-Id"

	headerAdminToken = "X-Admin-Token"

	headerCollectorToken = "X-Collector-Token"
)

type AdminActor struct {
	ID       string
	Username string

	ViaToken bool
}

func withAdminActor(ctx context.Context, a AdminActor) context.Context {

	return context.WithValue(ctx, ctxKeyAdmin, &a)
}

func adminFrom(ctx context.Context) *AdminActor {
	if v, ok := ctx.Value(ctxKeyAdmin).(*AdminActor); ok {
		return v
	}
	return nil
}

func adminActorID(ctx context.Context) string {
	if a := adminFrom(ctx); a != nil && a.ID != "" {
		return a.ID
	}
	return "admin"
}

const maxBodyBytes = 1 << 20

type wrappedWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *wrappedWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *wrappedWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytes += n
	return n, err
}

func requestLogger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := &wrappedWriter{ResponseWriter: w}

			next.ServeHTTP(ww, r)

			if ww.status == 0 {
				ww.status = http.StatusOK
			}
			level := slog.LevelInfo
			if ww.status >= 500 {
				level = slog.LevelError
			}
			log.Log(r.Context(), level, "http",
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.status,
				"bytes", ww.bytes,
				"duration_ms", time.Since(start).Milliseconds(),
				"remote", clientIP(r),
			)
		})
	}
}

func recoverer(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					log.ErrorContext(r.Context(), "请求处理 panic",
						"path", r.URL.Path, "panic", rec)
					writeError(w, r, log, domain.ErrInternal)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

var allowedHeaders = strings.Join([]string{
	"Authorization",
	"Content-Type",
	headerAdminToken,
	headerCollectorToken,
	headerFingerprint,
	headerTimestamp,
	headerNonce,
	headerSignature,
	"X-Device-Public-Key",
	"X-Device-Platform",
	"X-Device-App-Version",
	"X-Device-Label",
}, ",")

func cors(allowed []string) func(http.Handler) http.Handler {
	allowedSet := make(map[string]bool, len(allowed))
	for _, origin := range allowed {
		allowedSet[strings.TrimSpace(origin)] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && (allowedSet[origin] || allowedSet["*"]) {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,PATCH,DELETE,OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", allowedHeaders)
				w.Header().Set("Access-Control-Max-Age", "600")
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (s *Server) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" {
			writeError(w, r, s.log, domain.ErrUnauthorized.WithMessage("缺少访问令牌"))
			return
		}
		identity, err := s.svc.Auth.AuthenticateSession(r.Context(), token)
		if err != nil {
			writeError(w, r, s.log, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKeySession, identity)))
	})
}

func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if expected := s.cfg.AdminToken; expected != "" {
			got := r.Header.Get(headerAdminToken)
			if got != "" && constantTimeEqual(got, expected) {
				next.ServeHTTP(w, r.WithContext(withAdminActor(r.Context(), AdminActor{
					ID:       "admin-token",
					ViaToken: true,
				})))
				return
			}
		}

		if token := bearerToken(r); token != "" {
			identity, err := s.svc.Auth.AuthenticateSession(r.Context(), token)
			if err == nil {
				if !identity.IsAdmin() {
					writeError(w, r, s.log, domain.ErrNotAdminAccount)
					return
				}
				ctx := withAdminActor(r.Context(), AdminActor{
					ID:       identity.User.ID,
					Username: identity.User.Username,
				})
				next.ServeHTTP(w, r.WithContext(
					context.WithValue(ctx, ctxKeySession, identity)))
				return
			}

		}

		writeError(w, r, s.log, domain.ErrForbidden.WithMessage(
			"需要管理员身份或运维令牌"))
	})
}

func (s *Server) requireConsumer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sessionFrom(r.Context()).IsAdmin() {
			writeError(w, r, s.log, domain.ErrAdminCannotConsume)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func sessionFrom(ctx context.Context) *service.SessionIdentity {
	if v, ok := ctx.Value(ctxKeySession).(*service.SessionIdentity); ok {
		return v
	}
	return nil
}

func decodeHeaderValue(raw string) string {
	if raw == "" || !strings.Contains(raw, "%") {
		return raw
	}
	if decoded, err := url.QueryUnescape(raw); err == nil {
		return decoded
	}
	return raw
}

func bearerToken(r *http.Request) string {
	header := r.Header.Get("Authorization")
	if header == "" {
		return ""
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

func readBodyLimited(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	var buf bytes.Buffer
	reader := http.MaxBytesReader(w, r.Body, limit)
	if _, err := buf.ReadFrom(reader); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return nil, domain.ErrValidation.WithMessage("请求体过大")
		}
		return nil, domain.ErrValidation.WithErr(err)
	}
	body := buf.Bytes()

	r.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	diff := 0
	for i := 0; i < len(a); i++ {
		diff |= int(a[i] ^ b[i])
	}
	return diff == 0
}
