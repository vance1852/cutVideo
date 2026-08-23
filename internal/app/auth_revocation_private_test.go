package app_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/vance1852/cutVideo/internal/domain"
)

// Revoking an operator's sessions, and signing out, must both survive the next
// request made with the old bearer token.
func TestRevokedSessionStaysRevokedAcrossRequests(t *testing.T) {
	const (
		leaverEmail    = "leaver@cutvideo.test"
		leaverPassword = "leaver-secret-4711"
	)

	h := newHarness(t, harnessOptions{})
	supervisorToken := h.signIn(supervisorEmail, supervisorPassword)

	created := h.call(http.MethodPost, "/api/v1/users", supervisorToken, map[string]string{
		"email":        leaverEmail,
		"display_name": "Leaving Editor",
		"role":         string(domain.RoleEditor),
		"password":     leaverPassword,
	}, nil)
	if created.status != http.StatusCreated {
		t.Fatalf("provisioning the operator failed: %d %v", created.status, created.body)
	}
	leaverID := h.stringField(created, "id")
	leaverToken := h.signIn(leaverEmail, leaverPassword)

	working := h.call(http.MethodGet, "/api/v1/sessions/current", leaverToken, nil, nil)
	if working.status != http.StatusOK {
		t.Fatalf("the operator must be able to work before revocation: %d %v", working.status, working.body)
	}

	revoked := h.call(http.MethodDelete, "/api/v1/users/"+leaverID+"/sessions", supervisorToken, nil, nil)
	if revoked.status != http.StatusOK {
		t.Fatalf("revoking the sessions failed: %d %v", revoked.status, revoked.body)
	}
	if revoked.body["revoked"].(float64) < 1 {
		t.Fatalf("at least one session must be revoked: %v", revoked.body)
	}

	h.clk.Advance(time.Minute)
	after := h.call(http.MethodGet, "/api/v1/sessions/current", leaverToken, nil, nil)
	if after.status != http.StatusUnauthorized {
		t.Fatalf("a revoked credential must stop authenticating, got %d %v", after.status, after.body)
	}
	h.clk.Advance(time.Minute)
	stillOut := h.call(http.MethodGet, "/api/v1/projects", leaverToken, nil, nil)
	if stillOut.status != http.StatusUnauthorized {
		t.Fatalf("the revocation must not wear off on later requests, got %d %v", stillOut.status, stillOut.body)
	}

	freshToken := h.signIn(leaverEmail, leaverPassword)
	back := h.call(http.MethodGet, "/api/v1/projects", freshToken, nil, nil)
	if back.status != http.StatusOK {
		t.Fatalf("a fresh sign in must restore access: %d %v", back.status, back.body)
	}

	signedOut := h.call(http.MethodDelete, "/api/v1/sessions/current", freshToken, nil, nil)
	if signedOut.status != http.StatusNoContent {
		t.Fatalf("signing out failed: %d %v", signedOut.status, signedOut.body)
	}
	h.clk.Advance(time.Minute)
	afterSignOut := h.call(http.MethodGet, "/api/v1/sessions/current", freshToken, nil, nil)
	if afterSignOut.status != http.StatusUnauthorized {
		t.Fatalf("a signed out credential must stop authenticating, got %d %v", afterSignOut.status, afterSignOut.body)
	}
}
