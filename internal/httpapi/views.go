package httpapi

import (
	"github.com/vance1852/cutVideo/internal/domain"
)

type userView struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	Status      string `json:"status"`
	CreatedAt   string `json:"created_at"`
}

func newUserView(user *domain.User) userView {
	return userView{
		ID:          user.ID,
		Email:       user.Email,
		DisplayName: user.DisplayName,
		Role:        string(user.Role),
		Status:      string(user.Status),
		CreatedAt:   formatTime(user.CreatedAt),
	}
}

type projectView struct {
	ID             string `json:"id"`
	Code           string `json:"code"`
	Title          string `json:"title"`
	OwnerID        string `json:"owner_id"`
	Status         string `json:"status"`
	FrameRate      int    `json:"frame_rate"`
	Resolution     string `json:"resolution"`
	CurrentVersion int    `json:"current_version"`
	SealedVersion  int    `json:"sealed_version"`
	DeadlineAt     string `json:"deadline_at"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

func newProjectView(project *domain.Project) projectView {
	return projectView{
		ID:             project.ID,
		Code:           project.Code,
		Title:          project.Title,
		OwnerID:        project.OwnerID,
		Status:         string(project.Status),
		FrameRate:      project.FrameRate,
		Resolution:     project.Resolution,
		CurrentVersion: project.CurrentVersion,
		SealedVersion:  project.SealedVersion,
		DeadlineAt:     formatTime(project.DeadlineAt),
		CreatedAt:      formatTime(project.CreatedAt),
		UpdatedAt:      formatTime(project.UpdatedAt),
	}
}

type assetView struct {
	ID             string  `json:"id"`
	ProjectID      string  `json:"project_id"`
	Filename       string  `json:"filename"`
	Format         string  `json:"format"`
	Kind           string  `json:"kind"`
	Status         string  `json:"status"`
	Bytes          int64   `json:"bytes"`
	DurationMS     int64   `json:"duration_ms"`
	Checksum       string  `json:"checksum"`
	RejectReason   string  `json:"reject_reason,omitempty"`
	RetentionUntil string  `json:"retention_until"`
	IngestedAt     string  `json:"ingested_at"`
	VerifiedAt     *string `json:"verified_at"`
}

func newAssetView(asset *domain.MediaAsset) assetView {
	return assetView{
		ID:             asset.ID,
		ProjectID:      asset.ProjectID,
		Filename:       asset.Filename,
		Format:         asset.Format,
		Kind:           string(asset.Kind),
		Status:         string(asset.Status),
		Bytes:          asset.Bytes,
		DurationMS:     asset.DurationMS,
		Checksum:       asset.Checksum,
		RejectReason:   asset.RejectReason,
		RetentionUntil: formatTime(asset.RetentionUntil),
		IngestedAt:     formatTime(asset.IngestedAt),
		VerifiedAt:     formatTimePtr(asset.VerifiedAt),
	}
}

type clipView struct {
	ID           string `json:"id"`
	AssetID      string `json:"asset_id"`
	OrderIndex   int    `json:"order_index"`
	SourceInMS   int64  `json:"source_in_ms"`
	SourceOutMS  int64  `json:"source_out_ms"`
	Track        string `json:"track"`
	Transition   string `json:"transition,omitempty"`
	SpeedPercent int    `json:"speed_percent"`
	ProgramMS    int64  `json:"program_duration_ms"`
}

type timelineView struct {
	ID              string     `json:"id"`
	ProjectID       string     `json:"project_id"`
	Version         int        `json:"version"`
	Status          string     `json:"status"`
	Notes           string     `json:"notes,omitempty"`
	CreatedBy       string     `json:"created_by"`
	ClipCount       int        `json:"clip_count"`
	TotalDurationMS int64      `json:"total_duration_ms"`
	RowVersion      int        `json:"row_version"`
	SealedAt        *string    `json:"sealed_at"`
	CreatedAt       string     `json:"created_at"`
	Clips           []clipView `json:"clips"`
}

func newTimelineView(version *domain.TimelineVersion) timelineView {
	clips := version.Clips()
	views := make([]clipView, 0, len(clips))
	for _, clip := range clips {
		views = append(views, clipView{
			ID:           clip.ID,
			AssetID:      clip.AssetID,
			OrderIndex:   clip.OrderIndex,
			SourceInMS:   clip.SourceInMS,
			SourceOutMS:  clip.SourceOutMS,
			Track:        string(clip.Track),
			Transition:   clip.Transition,
			SpeedPercent: clip.SpeedPercent,
			ProgramMS:    clip.ProgramDurationMS(),
		})
	}
	return timelineView{
		ID:              version.ID,
		ProjectID:       version.ProjectID,
		Version:         version.Version,
		Status:          string(version.Status),
		Notes:           version.Notes,
		CreatedBy:       version.CreatedBy,
		ClipCount:       version.ClipCount,
		TotalDurationMS: version.TotalDurationMS,
		RowVersion:      version.RowVersion,
		SealedAt:        formatTimePtr(version.SealedAt),
		CreatedAt:       formatTime(version.CreatedAt),
		Clips:           views,
	}
}

type renderView struct {
	ID             string  `json:"id"`
	ProjectID      string  `json:"project_id"`
	TimelineID     string  `json:"timeline_id"`
	RequestedBy    string  `json:"requested_by"`
	Preset         string  `json:"preset"`
	Priority       int     `json:"priority"`
	Status         string  `json:"status"`
	Attempt        int     `json:"attempt"`
	MaxAttempts    int     `json:"max_attempts"`
	SlotID         string  `json:"slot_id,omitempty"`
	LeaseExpiresAt *string `json:"lease_expires_at"`
	NextAttemptAt  string  `json:"next_attempt_at"`
	QueuedAt       string  `json:"queued_at"`
	StartedAt      *string `json:"started_at"`
	FinishedAt     *string `json:"finished_at"`
	LastError      string  `json:"last_error,omitempty"`
	OutputURI      string  `json:"output_uri,omitempty"`
	OutputBytes    int64   `json:"output_bytes"`
}

func newRenderView(job *domain.RenderJob) renderView {
	return renderView{
		ID:             job.ID,
		ProjectID:      job.ProjectID,
		TimelineID:     job.TimelineID,
		RequestedBy:    job.RequestedBy,
		Preset:         job.Preset,
		Priority:       int(job.Priority),
		Status:         string(job.Status),
		Attempt:        job.Attempt,
		MaxAttempts:    job.MaxAttempts,
		SlotID:         job.SlotID,
		LeaseExpiresAt: formatTimePtr(job.LeaseExpiresAt),
		NextAttemptAt:  formatTime(job.NextAttemptAt),
		QueuedAt:       formatTime(job.QueuedAt),
		StartedAt:      formatTimePtr(job.StartedAt),
		FinishedAt:     formatTimePtr(job.FinishedAt),
		LastError:      job.LastError,
		OutputURI:      job.OutputURI,
		OutputBytes:    job.OutputBytes,
	}
}

type slotView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Pool        string `json:"pool"`
	Units       int    `json:"units"`
	Status      string `json:"status"`
	HeldByJobID string `json:"held_by_job_id,omitempty"`
}

type targetView struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Endpoint  string `json:"endpoint"`
	Status    string `json:"status"`
}

func newTargetView(target *domain.DeliveryTarget) targetView {
	return targetView{
		ID:        target.ID,
		ProjectID: target.ProjectID,
		Name:      target.Name,
		Kind:      string(target.Kind),
		Endpoint:  target.Endpoint,
		Status:    string(target.Status),
	}
}

type deliveryRecordView struct {
	ID           string  `json:"id"`
	JobID        string  `json:"job_id"`
	TargetID     string  `json:"target_id"`
	Status       string  `json:"status"`
	Attempt      int     `json:"attempt"`
	MaxAttempts  int     `json:"max_attempts"`
	OutputURI    string  `json:"output_uri"`
	Failure      string  `json:"failure,omitempty"`
	DispatchedAt *string `json:"dispatched_at"`
	ConfirmedAt  *string `json:"confirmed_at"`
}

func newDeliveryRecordView(record *domain.DeliveryRecord) deliveryRecordView {
	return deliveryRecordView{
		ID:           record.ID,
		JobID:        record.JobID,
		TargetID:     record.TargetID,
		Status:       string(record.Status),
		Attempt:      record.Attempt,
		MaxAttempts:  record.MaxAttempts,
		OutputURI:    record.OutputURI,
		Failure:      record.Failure,
		DispatchedAt: formatTimePtr(record.DispatchedAt),
		ConfirmedAt:  formatTimePtr(record.ConfirmedAt),
	}
}

type auditView struct {
	ID         string `json:"id"`
	ActorID    string `json:"actor_id"`
	ActorRole  string `json:"actor_role"`
	Action     string `json:"action"`
	ObjectKind string `json:"object_kind"`
	ObjectID   string `json:"object_id"`
	Result     string `json:"result"`
	RequestID  string `json:"request_id"`
	Detail     string `json:"detail,omitempty"`
	CreatedAt  string `json:"created_at"`
}

func newAuditView(event *domain.AuditEvent) auditView {
	return auditView{
		ID:         event.ID,
		ActorID:    event.ActorID,
		ActorRole:  string(event.ActorRole),
		Action:     event.Action,
		ObjectKind: event.ObjectKind,
		ObjectID:   event.ObjectID,
		Result:     string(event.Result),
		RequestID:  event.RequestID,
		Detail:     event.Detail,
		CreatedAt:  formatTime(event.CreatedAt),
	}
}
