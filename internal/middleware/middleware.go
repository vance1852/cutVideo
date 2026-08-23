// Package middleware carries the cross cutting HTTP concerns: request
// correlation, structured access logs, panic recovery, deadlines and bearer
// token authentication.
package middleware

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/vance1852/cutVideo/internal/apierr"
	"github.com/vance1852/cutVideo/internal/domain"
	"github.com/vance1852/cutVideo/internal/ids"
	"github.com/vance1852/cutVideo/internal/logging"
)

type principalKey struct{}

// RequestIDHeader carries the correlation identifier in responses.
const RequestIDHeader = "X-Request-Id"

// Authenticator resolves a bearer token into a principal.
type Authenticator interface {
	Authenticate(ctx context.Context, token string) (domain.Principal, error)
}

// WithPrincipal stores the authenticated principal on the context.
func WithPrincipal(ctx context.Context, principal domain.Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, principal)
}

// PrincipalFrom reads the authenticated principal from the context.
func PrincipalFrom(ctx context.Context) domain.Principal {
	if principal, ok := ctx.Value(principalKey{}).(domain.Principal); ok {
		return principal
	}
	return domain.Principal{}
}

// RequestID assigns or reuses a correlation identifier.
func RequestID(gen ids.Generator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestID := strings.TrimSpace(r.Header.Get(RequestIDHeader))
			if requestID == "" {
				requestID = gen.NewID("req")
			}
			w.Header().Set(RequestIDHeader, requestID)
			next.ServeHTTP(w, r.WithContext(logging.WithRequestID(r.Context(), requestID)))
		})
	}
}

// statusRecorder captures the response status for the access log.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(status int) {
	s.status = status
	s.ResponseWriter.WriteHeader(status)
}

func (s *statusRecorder) Write(payload []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	written, err := s.ResponseWriter.Write(payload)
	s.bytes += written
	return written, err
}

// AccessLog emits one structured record per request.
func AccessLog(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started := time.Now()
			recorder := &statusRecorder{ResponseWriter: w}
			next.ServeHTTP(recorder, r)
			status := recorder.status
			if status == 0 {
				status = http.StatusOK
			}
			logging.FromContext(r.Context(), logger).Info("http request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", status,
				"bytes", recorder.bytes,
				"duration_ms", time.Since(started).Milliseconds(),
			)
		})
	}
}

// Recover converts a panic into the standard error envelope.
func Recover(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if recovered := recover(); recovered != nil {
					logging.FromContext(r.Context(), logger).Error("panic recovered",
						"method", r.Method, "path", r.URL.Path, "panic", recovered)
					writeError(w, r, http.StatusInternalServerError, apierr.CodeInternal, "unexpected internal failure")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// Timeout bounds the lifetime of every request context.
func Timeout(limit time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if limit <= 0 {
				next.ServeHTTP(w, r)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), limit)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// Authenticate resolves the bearer token and rejects unusable sessions.
func Authenticate(auth Authenticator, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := bearerToken(r)
			if token == "" {
				writeError(w, r, http.StatusUnauthorized, apierr.CodeUnauthenticated, "a session token is required")
				return
			}
			principal, err := auth.Authenticate(r.Context(), token)
			if err != nil {
				public := apierr.From(err)
				logging.FromContext(r.Context(), logger).Debug("authentication rejected", "code", string(public.Code))
				writeError(w, r, public.HTTPStatus(), public.Code, public.Message)
				return
			}
			ctx := WithPrincipal(r.Context(), principal)
			ctx = logging.WithActor(ctx, principal.UserID, string(principal.Role))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireRole rejects principals whose role is not listed.
func RequireRole(roles ...domain.Role) func(http.Handler) http.Handler {
	allowed := make(map[domain.Role]struct{}, len(roles))
	for _, role := range roles {
		allowed[role] = struct{}{}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal := PrincipalFrom(r.Context())
			if principal.IsZero() {
				writeError(w, r, http.StatusUnauthorized, apierr.CodeUnauthenticated, "a session token is required")
				return
			}
			if _, ok := allowed[principal.Role]; !ok {
				writeError(w, r, http.StatusForbidden, apierr.CodeForbidden, "your role may not perform this action")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// Chain applies middleware in the given order.
func Chain(handler http.Handler, layers ...func(http.Handler) http.Handler) http.Handler {
	for index := len(layers) - 1; index >= 0; index-- {
		handler = layers[index](handler)
	}
	return handler
}

func bearerToken(r *http.Request) string {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if header == "" {
		return ""
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(strings.TrimSpace(parts[0]), "bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code apierr.Code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	payload := map[string]any{
		"error": map[string]any{
			"code":       string(code),
			"message":    message,
			"request_id": logging.RequestID(r.Context()),
		},
	}
	_ = json.NewEncoder(w).Encode(payload)
}
