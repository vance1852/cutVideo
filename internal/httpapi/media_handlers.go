package httpapi

import (
	"net/http"
	"time"

	"github.com/vance1852/cutVideo/internal/apierr"
	"github.com/vance1852/cutVideo/internal/domain"
	"github.com/vance1852/cutVideo/internal/middleware"
	"github.com/vance1852/cutVideo/internal/service/media"
)

type ingestRequest struct {
	Filename         string `json:"filename"`
	Format           string `json:"format"`
	Kind             string `json:"kind"`
	DeclaredChecksum string `json:"declared_checksum"`
	Bytes            int64  `json:"bytes"`
	DurationMS       int64  `json:"duration_ms"`
}

func (rt *Router) handleIngestAsset(w http.ResponseWriter, r *http.Request) {
	projectID, err := pathValue(r, "projectID")
	if err != nil {
		writeError(w, r, err)
		return
	}
	var body ingestRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	asset, err := rt.media.Ingest(r.Context(), middleware.PrincipalFrom(r.Context()), media.IngestInput{
		ProjectID:  projectID,
		Filename:   body.Filename,
		Format:     body.Format,
		Kind:       domain.AssetKind(body.Kind),
		DeclaredMD: body.DeclaredChecksum,
		Bytes:      body.Bytes,
		DurationMS: body.DurationMS,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusCreated, newAssetView(asset))
}

type verifyRequest struct {
	ObservedChecksum string `json:"observed_checksum"`
}

func (rt *Router) handleVerifyAsset(w http.ResponseWriter, r *http.Request) {
	assetID, err := pathValue(r, "assetID")
	if err != nil {
		writeError(w, r, err)
		return
	}
	var body verifyRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	asset, err := rt.media.Verify(r.Context(), middleware.PrincipalFrom(r.Context()), assetID, body.ObservedChecksum)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, newAssetView(asset))
}

type verifyBatchRequest struct {
	Items []struct {
		AssetID          string `json:"asset_id"`
		ObservedChecksum string `json:"observed_checksum"`
	} `json:"items"`
}

type verifyBatchResponse struct {
	Verified int `json:"verified"`
	Rejected int `json:"rejected"`
	Outcomes []struct {
		AssetID string `json:"asset_id"`
		Status  string `json:"status"`
		Message string `json:"message"`
	} `json:"outcomes"`
}

func (rt *Router) handleVerifyBatch(w http.ResponseWriter, r *http.Request) {
	var body verifyBatchRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	items := make([]media.VerifyBatchItem, 0, len(body.Items))
	for _, item := range body.Items {
		items = append(items, media.VerifyBatchItem{AssetID: item.AssetID, ObservedSum: item.ObservedChecksum})
	}
	outcomes, err := rt.media.VerifyBatch(r.Context(), middleware.PrincipalFrom(r.Context()), items)
	if err != nil {
		writeError(w, r, err)
		return
	}
	response := verifyBatchResponse{}
	for _, outcome := range outcomes {
		if outcome.Status == domain.AssetVerified {
			response.Verified++
		} else {
			response.Rejected++
		}
		response.Outcomes = append(response.Outcomes, struct {
			AssetID string `json:"asset_id"`
			Status  string `json:"status"`
			Message string `json:"message"`
		}{AssetID: outcome.AssetID, Status: string(outcome.Status), Message: outcome.Message})
	}
	status := http.StatusOK
	if response.Rejected > 0 && response.Verified > 0 {
		status = http.StatusMultiStatus
	}
	writeJSON(w, r, status, response)
}

type quarantineRequest struct {
	Reason string `json:"reason"`
}

func (rt *Router) handleQuarantineAsset(w http.ResponseWriter, r *http.Request) {
	assetID, err := pathValue(r, "assetID")
	if err != nil {
		writeError(w, r, err)
		return
	}
	var body quarantineRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	asset, err := rt.media.Quarantine(r.Context(), middleware.PrincipalFrom(r.Context()), assetID, body.Reason)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, newAssetView(asset))
}

type extendRetentionRequest struct {
	Extension string `json:"extension"`
}

func (rt *Router) handleExtendRetention(w http.ResponseWriter, r *http.Request) {
	assetID, err := pathValue(r, "assetID")
	if err != nil {
		writeError(w, r, err)
		return
	}
	var body extendRetentionRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	extension, err := time.ParseDuration(body.Extension)
	if err != nil {
		writeError(w, r, apierr.Wrap(apierr.CodeInvalidRequest, "extension must be a duration such as 72h", err))
		return
	}
	asset, err := rt.media.ExtendRetention(r.Context(), middleware.PrincipalFrom(r.Context()), assetID, extension)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, newAssetView(asset))
}

func (rt *Router) handleGetAsset(w http.ResponseWriter, r *http.Request) {
	assetID, err := pathValue(r, "assetID")
	if err != nil {
		writeError(w, r, err)
		return
	}
	asset, err := rt.media.Get(r.Context(), middleware.PrincipalFrom(r.Context()), assetID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, newAssetView(asset))
}

func (rt *Router) handleListAssets(w http.ResponseWriter, r *http.Request) {
	projectID, err := pathValue(r, "projectID")
	if err != nil {
		writeError(w, r, err)
		return
	}
	page, err := parsePage(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	query := r.URL.Query()
	filter := domain.AssetFilter{
		ProjectID: projectID,
		Status:    domain.AssetStatus(query.Get("status")),
		Kind:      domain.AssetKind(query.Get("kind")),
		Search:    query.Get("q"),
	}
	result, err := rt.media.List(r.Context(), middleware.PrincipalFrom(r.Context()), filter, page)
	if err != nil {
		writeError(w, r, err)
		return
	}
	views := make([]assetView, 0, len(result.Items))
	for _, asset := range result.Items {
		views = append(views, newAssetView(asset))
	}
	writeJSON(w, r, http.StatusOK, pageEnvelope{
		Items:      views,
		Total:      result.Total,
		Page:       result.Page,
		PageSize:   result.Size,
		TotalPages: result.TotalPages,
	})
}
