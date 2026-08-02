package presale

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

const (
	timelineCursorVersion = 1
	maxTimelineCursorSize = 1024
)

// TimelineCursor is the stable keyset position used by the repository. The
// tuple is ordered by occurred_at, type priority and source-row ID.
type TimelineCursor struct {
	OccurredAt   time.Time
	TypePriority uint8
	SourceID     uint64
}

type timelineCursorPayload struct {
	Version      uint8  `json:"v"`
	TenantID     string `json:"tenant"`
	RequestID    uint64 `json:"request"`
	OccurredUnix int64  `json:"occurred_us"`
	TypePriority uint8  `json:"priority"`
	SourceID     uint64 `json:"source_id"`
}

// TimelineRecord is an internal, deliberately sparse projection of one of the
// existing immutable TS process tables. It is never serialized directly.
type TimelineRecord struct {
	SourceID     uint64
	TypePriority uint8
	EventType    string
	OccurredAt   time.Time
	ActorID      string
	ActorName    string
	SubjectID    string
	SubjectName  string
	FromStatus   RequestStatus
	ToStatus     RequestStatus
	Result       string
	Content      string
	LinkURL      string
	ProgressPct  *uint8
	WorkHours    string
	WorkContent  string
}

type TimelineEventView struct {
	EventID     string        `json:"event_id"`
	Type        string        `json:"type"`
	OccurredAt  time.Time     `json:"occurred_at"`
	ActorID     string        `json:"actor_id,omitempty"`
	ActorName   string        `json:"actor_name,omitempty"`
	SubjectID   string        `json:"subject_id,omitempty"`
	SubjectName string        `json:"subject_name,omitempty"`
	FromStatus  RequestStatus `json:"from_status,omitempty"`
	ToStatus    RequestStatus `json:"to_status,omitempty"`
	Result      string        `json:"result,omitempty"`
	Content     string        `json:"content,omitempty"`
	LinkURL     string        `json:"link_url,omitempty"`
	ProgressPct *uint8        `json:"progress_pct,omitempty"`
	WorkHours   string        `json:"work_hours,omitempty"`
	WorkContent string        `json:"work_content,omitempty"`
}

type TimelinePage struct {
	Items      []TimelineEventView `json:"items"`
	NextCursor string              `json:"next_cursor,omitempty"`
}

type AvailableActionsView struct {
	Status  RequestStatus `json:"status"`
	Version uint64        `json:"version"`
	Actions []string      `json:"actions"`
}

// UseTimelineCursorKey attaches the server-only cursor authentication key.
// A distinct cursor format/version allows safe rotation through deployment.
func (s *Service) UseTimelineCursorKey(key []byte) *Service {
	s.timelineCursorKey = append([]byte(nil), key...)
	return s
}

// Timeline first applies the same parent-resource scope check as detail, then
// delegates to a SQL keyset query. It never loads all child tables in memory.
func (s *Service) Timeline(ctx context.Context, actor Actor, requestID uint64, cursorValue string, limit int) (TimelinePage, error) {
	if !actor.Can("presale.read") {
		return TimelinePage{}, ErrForbidden
	}
	if limit < 1 || limit > 100 || len(s.timelineCursorKey) < 32 {
		return TimelinePage{}, ErrInvalidInput
	}
	requestValue, err := s.repo.FindRequest(ctx, actor.TenantID, requestID)
	if err != nil {
		return TimelinePage{}, err
	}
	if err = s.requireReadable(ctx, actor, requestValue); err != nil {
		return TimelinePage{}, err
	}
	var cursor *TimelineCursor
	if cursorValue != "" {
		decoded, decodeErr := decodeTimelineCursor(s.timelineCursorKey, actor.TenantID, requestID, cursorValue)
		if decodeErr != nil {
			return TimelinePage{}, ErrInvalidInput
		}
		cursor = &decoded
	}
	records, err := s.repo.ListTimeline(ctx, actor.TenantID, requestID, cursor, limit+1)
	if err != nil {
		return TimelinePage{}, err
	}
	hasMore := len(records) > limit
	if hasMore {
		records = records[:limit]
	}
	items := make([]TimelineEventView, 0, len(records))
	for _, record := range records {
		items = append(items, timelineEventView(record))
	}
	page := TimelinePage{Items: items}
	if hasMore && len(records) > 0 {
		last := records[len(records)-1]
		page.NextCursor, err = encodeTimelineCursor(s.timelineCursorKey, actor.TenantID, requestID, TimelineCursor{
			OccurredAt: last.OccurredAt, TypePriority: last.TypePriority, SourceID: last.SourceID,
		})
		if err != nil {
			return TimelinePage{}, err
		}
	}
	return page, nil
}

// AvailableActions is a separate backend-computed operation boundary used by
// clients that refresh only the task action area.
func (s *Service) AvailableActions(ctx context.Context, actor Actor, requestID uint64) (AvailableActionsView, error) {
	detail, err := s.RequestDetail(ctx, actor, requestID)
	if err != nil {
		return AvailableActionsView{}, err
	}
	actions := append([]string(nil), detail.AvailableActions...)
	request := detail.Request
	if request.Status == StatusPendingApproval && actor.Can("presale.approve") && approvalNodeRoleAllowed(actor, request.CurrentApprovalNode) && s.approvalTasks != nil {
		instance, instanceErr := s.repo.FindApprovalInstance(ctx, actor.TenantID, requestID)
		if instanceErr == nil && instance.EngineInstanceID != "" && instance.Status == "PENDING" && instance.CurrentNode == request.CurrentApprovalNode &&
			instance.PendingTaskID == "" && instance.PendingApprover == "" && instance.PendingAction == "" {
			resolved, resolveErr := s.approvalTasks.ResolveCurrentTask(ctx, ApprovalTaskQuery{
				TenantID: actor.TenantID, EngineInstanceID: instance.EngineInstanceID,
				Node: request.CurrentApprovalNode, ApproverID: actor.UserID,
			})
			if resolveErr == nil && resolved.EngineTaskID != "" && resolved.EngineInstanceID == instance.EngineInstanceID && resolved.Node == request.CurrentApprovalNode && resolved.ApproverID == actor.UserID {
				actions = append(actions, "APPROVE", "REJECT")
			}
		}
	}
	return AvailableActionsView{Status: request.Status, Version: request.Version, Actions: actions}, nil
}

func timelineEventView(value TimelineRecord) TimelineEventView {
	return TimelineEventView{
		EventID: fmt.Sprintf("%s:%d", value.EventType, value.SourceID), Type: value.EventType,
		OccurredAt: value.OccurredAt, ActorID: value.ActorID, ActorName: value.ActorName,
		SubjectID: value.SubjectID, SubjectName: value.SubjectName,
		FromStatus: value.FromStatus, ToStatus: value.ToStatus, Result: value.Result,
		Content: value.Content, LinkURL: value.LinkURL, ProgressPct: value.ProgressPct,
		WorkHours: value.WorkHours, WorkContent: value.WorkContent,
	}
}

func encodeTimelineCursor(key []byte, tenant string, requestID uint64, cursor TimelineCursor) (string, error) {
	payload, err := json.Marshal(timelineCursorPayload{
		Version: timelineCursorVersion, TenantID: tenant, RequestID: requestID,
		OccurredUnix: cursor.OccurredAt.UTC().UnixMicro(), TypePriority: cursor.TypePriority, SourceID: cursor.SourceID,
	})
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	signature := timelineCursorMAC(key, encoded)
	return encoded + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func decodeTimelineCursor(key []byte, tenant string, requestID uint64, value string) (TimelineCursor, error) {
	if len(value) == 0 || len(value) > maxTimelineCursorSize || strings.Count(value, ".") != 1 {
		return TimelineCursor{}, errors.New("invalid timeline cursor")
	}
	parts := strings.SplitN(value, ".", 2)
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(signature) != sha256.Size || subtle.ConstantTimeCompare(signature, timelineCursorMAC(key, parts[0])) != 1 {
		return TimelineCursor{}, errors.New("invalid timeline cursor")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return TimelineCursor{}, errors.New("invalid timeline cursor")
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var payload timelineCursorPayload
	if err = decoder.Decode(&payload); err != nil {
		return TimelineCursor{}, errors.New("invalid timeline cursor")
	}
	if payload.Version != timelineCursorVersion || payload.TenantID != tenant || payload.RequestID != requestID || payload.OccurredUnix <= 0 || payload.TypePriority == 0 || payload.TypePriority > 50 || payload.SourceID == 0 {
		return TimelineCursor{}, errors.New("invalid timeline cursor")
	}
	// Reject a second JSON value without exposing parsing details.
	var trailing any
	if trailingErr := decoder.Decode(&trailing); !errors.Is(trailingErr, io.EOF) {
		return TimelineCursor{}, errors.New("invalid timeline cursor")
	}
	return TimelineCursor{OccurredAt: time.UnixMicro(payload.OccurredUnix).UTC(), TypePriority: payload.TypePriority, SourceID: payload.SourceID}, nil
}

func timelineCursorMAC(key []byte, encodedPayload string) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("presale-timeline-v" + strconv.Itoa(timelineCursorVersion) + ":" + encodedPayload))
	return mac.Sum(nil)
}
