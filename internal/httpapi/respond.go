// Package httpapi exposes the platform over HTTP. Handlers only translate
// between the wire format and the service layer; business rules live in the
// services and the domain.
package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/vance1852/cutVideo/internal/apierr"
	"github.com/vance1852/cutVideo/internal/domain"
	"github.com/vance1852/cutVideo/internal/logging"
)

const maxRequestBytes = 1 << 20

// errorBody is the stable error envelope.
type errorBody struct {
	Code      string            `json:"code"`
	Message   string            `json:"message"`
	RequestID string            `json:"request_id"`
	Details   map[string]string `json:"details,omitempty"`
}

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type pageEnvelope struct {
	Items      any `json:"items"`
	Total      int `json:"total"`
	Page       int `json:"page"`
	PageSize   int `json:"page_size"`
	TotalPages int `json:"total_pages"`
}

func writeJSON(w http.ResponseWriter, r *http.Request, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if payload == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		logging.RequestID(r.Context())
	}
}

func writeError(w http.ResponseWriter, r *http.Request, err error) {
	public := apierr.From(err)
	if public == nil {
		public = apierr.New(apierr.CodeInternal, "unexpected internal failure")
	}
	writeJSON(w, r, public.HTTPStatus(), errorEnvelope{Error: errorBody{
		Code:      string(public.Code),
		Message:   public.Message,
		RequestID: logging.RequestID(r.Context()),
		Details:   public.Details,
	}})
}

func decodeJSON(r *http.Request, target any) error {
	if r.Body == nil {
		return apierr.New(apierr.CodeInvalidRequest, "a JSON body is required")
	}
	limited := http.MaxBytesReader(nil, r.Body, maxRequestBytes)
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		if errors.Is(err, io.EOF) {
			return apierr.Wrap(apierr.CodeInvalidRequest, "a JSON body is required", err)
		}
		return apierr.Wrap(apierr.CodeInvalidRequest, "the JSON body could not be read", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return apierr.New(apierr.CodeInvalidRequest, "the body must contain exactly one JSON object")
	}
	return nil
}

func parsePage(r *http.Request) (domain.Page, error) {
	query := r.URL.Query()
	number, err := intParam(query.Get("page"), 1)
	if err != nil {
		return domain.Page{}, apierr.Wrap(apierr.CodeInvalidRequest, "page must be an integer", err)
	}
	size, err := intParam(query.Get("page_size"), domain.DefaultPageSize)
	if err != nil {
		return domain.Page{}, apierr.Wrap(apierr.CodeInvalidRequest, "page_size must be an integer", err)
	}
	direction := domain.SortDirection(strings.ToLower(strings.TrimSpace(query.Get("sort"))))
	page, err := domain.NewPage(number, size, query.Get("sort_by"), direction)
	if err != nil {
		return domain.Page{}, apierr.Wrap(apierr.CodeInvalidRequest, "the pagination request is not acceptable", err)
	}
	return page, nil
}

func intParam(raw string, fallback int) (int, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(trimmed)
	if err != nil {
		return 0, fmt.Errorf("httpapi: %q is not an integer: %w", raw, err)
	}
	return parsed, nil
}

func pathValue(r *http.Request, name string) (string, error) {
	value := strings.TrimSpace(r.PathValue(name))
	if value == "" {
		return "", apierr.New(apierr.CodeInvalidRequest, "the "+name+" path segment is required")
	}
	return value, nil
}

func idempotencyKey(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get("Idempotency-Key"))
}

func formatTime(at time.Time) string {
	return at.Format(time.RFC3339)
}

func formatTimePtr(at *time.Time) *string {
	if at == nil {
		return nil
	}
	formatted := at.Format(time.RFC3339)
	return &formatted
}
