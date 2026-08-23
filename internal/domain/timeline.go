package domain

import (
	"sort"
	"strings"
	"time"
)

// TimelineStatus enumerates the lifecycle of one timeline version.
type TimelineStatus string

// Timeline statuses. A draft accepts clip changes, a sealed version is
// immutable and is the only thing the render farm accepts, and a superseded
// version is kept for history.
const (
	TimelineDraft      TimelineStatus = "draft"
	TimelineSealed     TimelineStatus = "sealed"
	TimelineSuperseded TimelineStatus = "superseded"
)

// Track enumerates the editorial tracks a clip can occupy.
type Track string

// Tracks.
const (
	TrackProgram Track = "program"
	TrackBRoll   Track = "broll"
	TrackAudio   Track = "audio"
	TrackTitles  Track = "titles"
)

// Valid reports whether the track is known.
func (t Track) Valid() bool {
	switch t {
	case TrackProgram, TrackBRoll, TrackAudio, TrackTitles:
		return true
	default:
		return false
	}
}

// Clip is one ordered segment of a timeline version.
type Clip struct {
	ID           string
	TimelineID   string
	AssetID      string
	OrderIndex   int
	SourceInMS   int64
	SourceOutMS  int64
	Track        Track
	Transition   string
	SpeedPercent int
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// NewClip validates and builds a clip.
func NewClip(id, timelineID, assetID string, orderIndex int, inMS, outMS int64, track Track, transition string, speedPercent int, now time.Time) (*Clip, error) {
	if strings.TrimSpace(id) == "" {
		return nil, NewValidationError("id", "must not be empty")
	}
	if strings.TrimSpace(timelineID) == "" {
		return nil, NewValidationError("timeline_id", "must not be empty")
	}
	if strings.TrimSpace(assetID) == "" {
		return nil, NewValidationError("asset_id", "must not be empty")
	}
	if orderIndex < 0 {
		return nil, NewValidationError("order_index", "must not be negative")
	}
	if inMS < 0 {
		return nil, NewValidationError("source_in_ms", "must not be negative")
	}
	if outMS <= inMS {
		return nil, NewValidationError("source_out_ms", "must be greater than source_in_ms")
	}
	if !track.Valid() {
		return nil, NewValidationError("track", "must be program, broll, audio or titles")
	}
	if speedPercent < 25 || speedPercent > 400 {
		return nil, NewValidationError("speed_percent", "must be between 25 and 400")
	}
	return &Clip{
		ID:           id,
		TimelineID:   timelineID,
		AssetID:      assetID,
		OrderIndex:   orderIndex,
		SourceInMS:   inMS,
		SourceOutMS:  outMS,
		Track:        track,
		Transition:   strings.TrimSpace(transition),
		SpeedPercent: speedPercent,
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
}

// SourceDurationMS is the untimewarped length of the clip.
func (c *Clip) SourceDurationMS() int64 { return c.SourceOutMS - c.SourceInMS }

// ProgramDurationMS is the length the clip occupies on the program timeline
// after the speed ramp is applied.
func (c *Clip) ProgramDurationMS() int64 {
	if c.SpeedPercent <= 0 {
		return c.SourceDurationMS()
	}
	return c.SourceDurationMS() * 100 / int64(c.SpeedPercent)
}

// FitsAsset reports whether the clip range stays inside the asset duration.
func (c *Clip) FitsAsset(asset *MediaAsset) error {
	if asset == nil {
		return ErrNotFound
	}
	if c.SourceOutMS > asset.DurationMS {
		return NewValidationError("source_out_ms", "exceeds the duration of the referenced asset")
	}
	return nil
}

// Clone returns an independent copy of the clip.
func (c *Clip) Clone() *Clip {
	if c == nil {
		return nil
	}
	copied := *c
	return &copied
}

// TimelineVersion is one revision of the edit decision list for a project.
type TimelineVersion struct {
	ID              string
	ProjectID       string
	Version         int
	Status          TimelineStatus
	Notes           string
	CreatedBy       string
	ClipCount       int
	TotalDurationMS int64
	RowVersion      int
	SealedAt        *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
	clips           []*Clip
}

// NewTimelineVersion validates and builds a draft timeline version.
func NewTimelineVersion(id, projectID string, version int, createdBy, notes string, now time.Time) (*TimelineVersion, error) {
	if strings.TrimSpace(id) == "" {
		return nil, NewValidationError("id", "must not be empty")
	}
	if strings.TrimSpace(projectID) == "" {
		return nil, NewValidationError("project_id", "must not be empty")
	}
	if version < 1 {
		return nil, NewValidationError("version", "must be at least 1")
	}
	if strings.TrimSpace(createdBy) == "" {
		return nil, NewValidationError("created_by", "must not be empty")
	}
	return &TimelineVersion{
		ID:         id,
		ProjectID:  projectID,
		Version:    version,
		Status:     TimelineDraft,
		Notes:      strings.TrimSpace(notes),
		CreatedBy:  strings.TrimSpace(createdBy),
		RowVersion: 1,
		CreatedAt:  now,
		UpdatedAt:  now,
	}, nil
}

// AttachClips replaces the in-memory clip collection. The slice is copied so a
// caller cannot mutate timeline state through the slice it passed in.
func (t *TimelineVersion) AttachClips(clips []*Clip) {
	t.clips = make([]*Clip, 0, len(clips))
	for _, clip := range clips {
		t.clips = append(t.clips, clip.Clone())
	}
	t.sortClips()
	t.recount()
}

// Clips returns copies of the attached clips in program order.
func (t *TimelineVersion) Clips() []*Clip {
	out := make([]*Clip, 0, len(t.clips))
	for _, clip := range t.clips {
		out = append(out, clip.Clone())
	}
	return out
}

// Editable reports whether clip changes are accepted.
func (t *TimelineVersion) Editable() bool { return t.Status == TimelineDraft }

// EnsureEditable returns a transition error when the version is frozen.
func (t *TimelineVersion) EnsureEditable() error {
	if t.Editable() {
		return nil
	}
	return NewTransitionError("timeline", string(t.Status), string(TimelineDraft), "only draft versions accept clip changes")
}

// AddClip inserts a clip, rejecting duplicate order slots on the same track.
func (t *TimelineVersion) AddClip(clip *Clip, now time.Time) error {
	if err := t.EnsureEditable(); err != nil {
		return err
	}
	if clip == nil {
		return NewValidationError("clip", "must not be nil")
	}
	if clip.TimelineID != t.ID {
		return NewValidationError("clip.timeline_id", "must match the timeline version")
	}
	for _, existing := range t.clips {
		if existing.Track == clip.Track && existing.OrderIndex == clip.OrderIndex {
			return NewValidationError("order_index", "is already occupied on that track")
		}
		if existing.ID == clip.ID {
			return ErrConflict
		}
	}
	t.clips = append(t.clips, clip.Clone())
	t.sortClips()
	t.recount()
	t.UpdatedAt = now
	return nil
}

// RemoveClip drops a clip from a draft version.
func (t *TimelineVersion) RemoveClip(clipID string, now time.Time) error {
	if err := t.EnsureEditable(); err != nil {
		return err
	}
	for index, existing := range t.clips {
		if existing.ID == clipID {
			t.clips = append(t.clips[:index:index], t.clips[index+1:]...)
			t.recount()
			t.UpdatedAt = now
			return nil
		}
	}
	return ErrNotFound
}

// Seal freezes the version after checking every editorial invariant. The caller
// supplies the assets referenced by the clips so the domain can verify that all
// footage is still usable.
func (t *TimelineVersion) Seal(assets map[string]*MediaAsset, now time.Time) error {
	if t.Status != TimelineDraft {
		return NewTransitionError("timeline", string(t.Status), string(TimelineSealed), "only draft versions can be sealed")
	}
	if len(t.clips) == 0 {
		return NewTransitionError("timeline", string(t.Status), string(TimelineSealed), "a sealed version needs at least one clip")
	}
	if !t.hasProgramTrack() {
		return NewTransitionError("timeline", string(t.Status), string(TimelineSealed), "a sealed version needs at least one program clip")
	}
	for _, clip := range t.clips {
		asset, ok := assets[clip.AssetID]
		if !ok {
			return NewValidationError("clips", "reference an asset that is not part of the project")
		}
		if err := asset.Usable(now); err != nil {
			return err
		}
		if err := clip.FitsAsset(asset); err != nil {
			return err
		}
	}
	stamp := now
	t.Status = TimelineSealed
	t.SealedAt = &stamp
	t.RowVersion++
	t.UpdatedAt = now
	t.recount()
	return nil
}

// Supersede marks a sealed version as replaced by a newer one.
func (t *TimelineVersion) Supersede(now time.Time) error {
	if t.Status != TimelineSealed {
		return NewTransitionError("timeline", string(t.Status), string(TimelineSuperseded), "only sealed versions can be superseded")
	}
	t.Status = TimelineSuperseded
	t.RowVersion++
	t.UpdatedAt = now
	return nil
}

// Renderable reports whether the render farm accepts this version.
func (t *TimelineVersion) Renderable() error {
	if t.Status != TimelineSealed {
		return NewTransitionError("timeline", string(t.Status), string(TimelineSealed), "render submission requires a sealed version")
	}
	if t.TotalDurationMS <= 0 {
		return NewValidationError("total_duration_ms", "must be positive before rendering")
	}
	return nil
}

// ProgramDurationMS recomputes the program duration from attached clips.
func (t *TimelineVersion) ProgramDurationMS() int64 {
	var total int64
	for _, clip := range t.clips {
		if clip.Track == TrackProgram || clip.Track == TrackBRoll {
			total += clip.ProgramDurationMS()
		}
	}
	return total
}

// Clone returns an independent copy including its clips.
func (t *TimelineVersion) Clone() *TimelineVersion {
	if t == nil {
		return nil
	}
	copied := *t
	if t.SealedAt != nil {
		stamp := *t.SealedAt
		copied.SealedAt = &stamp
	}
	copied.clips = make([]*Clip, 0, len(t.clips))
	for _, clip := range t.clips {
		copied.clips = append(copied.clips, clip.Clone())
	}
	return &copied
}

func (t *TimelineVersion) sortClips() {
	sort.SliceStable(t.clips, func(i, j int) bool {
		if t.clips[i].Track == t.clips[j].Track {
			return t.clips[i].OrderIndex < t.clips[j].OrderIndex
		}
		return t.clips[i].Track < t.clips[j].Track
	})
}

func (t *TimelineVersion) recount() {
	t.ClipCount = len(t.clips)
	t.TotalDurationMS = t.ProgramDurationMS()
}

func (t *TimelineVersion) hasProgramTrack() bool {
	for _, clip := range t.clips {
		if clip.Track == TrackProgram {
			return true
		}
	}
	return false
}

// TimelineFilter narrows a timeline listing.
type TimelineFilter struct {
	ProjectID string
	Status    TimelineStatus
}
