package delivery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/vance1852/cutVideo/internal/domain"
)

// ErrRejectedDownstream reports that the destination answered with a failure.
var ErrRejectedDownstream = errors.New("delivery: destination rejected the payload")

// Payload is the body sent to a delivery destination.
type Payload struct {
	JobID      string `json:"job_id"`
	ProjectID  string `json:"project_id"`
	TimelineID string `json:"timeline_id"`
	Preset     string `json:"preset"`
	OutputURI  string `json:"output_uri"`
	Bytes      int64  `json:"output_bytes"`
	Attempt    int    `json:"attempt"`
	SentAt     string `json:"sent_at"`
}

// Transport pushes a finished render to one destination.
type Transport interface {
	Send(ctx context.Context, target *domain.DeliveryTarget, payload Payload) (string, error)
}

// HTTPTransport delivers over HTTP. The caller's context governs cancellation and
// deadlines, so a shutdown or a client disconnect stops the outbound call too.
type HTTPTransport struct {
	client  *http.Client
	timeout time.Duration
}

// NewHTTPTransport builds a transport with a bounded per request timeout.
func NewHTTPTransport(client *http.Client, timeout time.Duration) *HTTPTransport {
	if client == nil {
		client = &http.Client{Timeout: 0}
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &HTTPTransport{client: client, timeout: timeout}
}

// Send posts the payload to the destination endpoint and returns the trimmed
// response body for the audit trail.
func (t *HTTPTransport) Send(ctx context.Context, target *domain.DeliveryTarget, payload Payload) (string, error) {
	if target == nil {
		return "", domain.NewValidationError("target", "must not be nil")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("delivery: encode payload for %s: %w", target.Name, err)
	}
	callCtx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()

	request, err := http.NewRequestWithContext(callCtx, http.MethodPost, target.Endpoint, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("delivery: build request for %s: %w", target.Name, err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CutVideo-Target", target.Name)
	request.Header.Set("X-CutVideo-Credential-Ref", target.CredentialRef)

	response, err := t.client.Do(request)
	if err != nil {
		return "", fmt.Errorf("delivery: call %s: %w", target.Name, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
	}()

	answer, err := io.ReadAll(io.LimitReader(response.Body, 4<<10))
	if err != nil {
		return "", fmt.Errorf("delivery: read response from %s: %w", target.Name, err)
	}
	trimmed := strings.TrimSpace(string(answer))
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return trimmed, fmt.Errorf("%w: %s answered %d", ErrRejectedDownstream, target.Name, response.StatusCode)
	}
	return trimmed, nil
}

// StubTransport is a deterministic transport used by tests and local runs.
type StubTransport struct {
	Failures map[string]error
	Sent     []Payload
}

// Send records the payload and returns the configured outcome for the target.
func (s *StubTransport) Send(_ context.Context, target *domain.DeliveryTarget, payload Payload) (string, error) {
	if s.Failures != nil {
		if err, ok := s.Failures[target.Name]; ok && err != nil {
			return "", err
		}
	}
	s.Sent = append(s.Sent, payload)
	return "accepted", nil
}
