package httpapi

import (
	"net/http"

	"github.com/vance1852/cutVideo/internal/apierr"
	"github.com/vance1852/cutVideo/internal/domain"
	"github.com/vance1852/cutVideo/internal/middleware"
	"github.com/vance1852/cutVideo/internal/service/auth"
)

type signInRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type signInResponse struct {
	Token     string   `json:"token"`
	ExpiresAt string   `json:"expires_at"`
	User      userView `json:"user"`
}

func (rt *Router) handleSignIn(w http.ResponseWriter, r *http.Request) {
	var body signInRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	result, err := rt.auth.SignIn(r.Context(), auth.SignInInput{
		Email:     body.Email,
		Password:  body.Password,
		UserAgent: r.UserAgent(),
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, signInResponse{
		Token:     result.Token,
		ExpiresAt: formatTime(result.ExpiresAt),
		User:      newUserView(result.User),
	})
}

func (rt *Router) handleSignOut(w http.ResponseWriter, r *http.Request) {
	if err := rt.auth.SignOut(r.Context(), middleware.PrincipalFrom(r.Context())); err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusNoContent, nil)
}

type provisionRequest struct {
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	Password    string `json:"password"`
}

func (rt *Router) handleProvisionUser(w http.ResponseWriter, r *http.Request) {
	var body provisionRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	user, err := rt.auth.Provision(r.Context(), middleware.PrincipalFrom(r.Context()), auth.ProvisionInput{
		Email:       body.Email,
		DisplayName: body.DisplayName,
		Role:        domain.Role(body.Role),
		Password:    body.Password,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusCreated, newUserView(user))
}

func (rt *Router) handleListUsers(w http.ResponseWriter, r *http.Request) {
	page, err := parsePage(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	result, err := rt.auth.ListUsers(r.Context(), middleware.PrincipalFrom(r.Context()), page)
	if err != nil {
		writeError(w, r, err)
		return
	}
	views := make([]userView, 0, len(result.Items))
	for _, user := range result.Items {
		views = append(views, newUserView(user))
	}
	writeJSON(w, r, http.StatusOK, pageEnvelope{
		Items:      views,
		Total:      result.Total,
		Page:       result.Page,
		PageSize:   result.Size,
		TotalPages: result.TotalPages,
	})
}

func (rt *Router) handleRevokeSessions(w http.ResponseWriter, r *http.Request) {
	userID, err := pathValue(r, "userID")
	if err != nil {
		writeError(w, r, err)
		return
	}
	revoked, err := rt.auth.RevokeAllForUser(r.Context(), middleware.PrincipalFrom(r.Context()), userID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"revoked": revoked})
}

func (rt *Router) handleWhoAmI(w http.ResponseWriter, r *http.Request) {
	principal := middleware.PrincipalFrom(r.Context())
	if principal.IsZero() {
		writeError(w, r, apierr.New(apierr.CodeUnauthenticated, "a session token is required"))
		return
	}
	writeJSON(w, r, http.StatusOK, map[string]any{
		"user_id":    principal.UserID,
		"email":      principal.Email,
		"role":       string(principal.Role),
		"session_id": principal.SessionID,
	})
}

func (rt *Router) handleListAudit(w http.ResponseWriter, r *http.Request) {
	page, err := parsePage(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	query := r.URL.Query()
	filter := domain.AuditFilter{
		ActorID:    query.Get("actor_id"),
		Action:     query.Get("action"),
		ObjectKind: query.Get("object_kind"),
		ObjectID:   query.Get("object_id"),
		Result:     domain.AuditResult(query.Get("result")),
	}
	result, err := rt.recorder.Query(r.Context(), filter, page)
	if err != nil {
		writeError(w, r, apierr.Wrap(apierr.CodeInternal, "could not list audit events", err))
		return
	}
	views := make([]auditView, 0, len(result.Items))
	for _, event := range result.Items {
		views = append(views, newAuditView(event))
	}
	writeJSON(w, r, http.StatusOK, pageEnvelope{
		Items:      views,
		Total:      result.Total,
		Page:       result.Page,
		PageSize:   result.Size,
		TotalPages: result.TotalPages,
	})
}
