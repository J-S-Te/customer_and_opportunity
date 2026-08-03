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

// 时间线游标记录稳定的键集位置，排序元组依次为发生时间、类型优先级和源记录 ID。
// 三元组可以为同一时间戳下的不同事实建立确定顺序。
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

// TimelineRecord 是多个不可变过程表合并后的稀疏内部投影，不直接序列化，
// 对外字段由事件类型决定，避免暴露各来源表的内部列。
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

// 游标使用服务端密钥认证并携带格式版本，客户端不能篡改租户、申请或翻页位置；
// 独立版本字段为部署期间的密钥和格式轮换保留边界。
func (s *Service) UseTimelineCursorKey(key []byte) *Service {
	s.timelineCursorKey = append([]byte(nil), key...)
	return s
}

// 时间线先复用详情的父资源授权，再执行数据库键集查询；不会把所有子表加载到内存后合并。
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

// 可用动作由后端根据最新状态、版本、角色和真实审批待办共同计算。
// 前端可单独刷新操作区，但该结果仅用于展示，实际写接口仍会重新完整鉴权。
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
	// 拒绝游标载荷后的第二个 JSON 值，且不向调用者暴露具体解析细节。
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
