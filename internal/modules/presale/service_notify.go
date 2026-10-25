package presale

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/ownerdirectory"
	"strings"
	"time"
)

func (s *Service) UseWorkflowNotifications(writer WorkflowNotificationWriter) *Service {
	s.notifications = writer
	return s
}

// 审批任务解析器用于把本地审批节点与审批引擎当前待办进行实时绑定；
// 未配置时审批按钮和审批命令均按依赖不可用处理，而不是仅凭本地角色放行。

func (s *Service) notifyWorkflow(ctx context.Context, n WorkflowNotification) error {
	if s.notifications == nil {
		return nil
	}
	return s.notifications.Write(ctx, n)
}

// notifyApprovalNode 解析当前绑定节点角色的全部有效用户，并为每个
// 唯一接收者写入一条通知。角色为空、目录不可用或结果为空时，
// 外层事务失败，确保流程不能在没有可达审批人的情况下推进。
func (s *Service) notifyApprovalNode(ctx context.Context, tenant string, request *PresaleRequest, instance *ApprovalInstance, node uint8) error {
	role, ok := approvalRoleForNode(instance, node)
	if !ok || s.ownerDirectory == nil || request == nil {
		return ErrDependencyUnavailable
	}
	recipients := make([]ownerdirectory.User, 0)
	seen := make(map[string]struct{})
	for pageNumber := 1; pageNumber <= ownerDirectoryMaxPages; pageNumber++ {
		page, err := s.ownerDirectory.List(ctx, ownerdirectory.Query{RoleCodes: []string{role}, Page: pageNumber, PageSize: approvalRecipientPageSize})
		if err != nil {
			return ErrDependencyUnavailable
		}
		for _, user := range page.Items {
			user.ID = strings.TrimSpace(user.ID)
			if user.ID == "" {
				continue
			}
			if _, exists := seen[user.ID]; exists {
				continue
			}
			seen[user.ID] = struct{}{}
			recipients = append(recipients, user)
		}
		if len(page.Items) == 0 || int64(len(recipients)) >= page.Total || len(page.Items) < approvalRecipientPageSize {
			break
		}
	}
	if len(recipients) == 0 {
		return ErrDependencyUnavailable
	}
	for _, recipient := range recipients {
		body := "售前申请已流转到您当前审批节点，请及时处理。"
		if name := strings.TrimSpace(recipient.DisplayName); name != "" {
			body = name + "，" + body
		}
		if err := s.notifyWorkflow(ctx, WorkflowNotification{TenantID: tenant, RecipientID: recipient.ID, Type: "PRESALE_APPROVAL_PENDING", Title: "售前审批待处理", Body: body, RequestID: request.ID, RequestNo: request.RequestNo, ApprovalNode: node, SourceDiscriminator: approvalRoundDiscriminator(instance)}); err != nil {
			return err
		}
	}
	return nil
}

func workflowNotificationExcerpt(value string, max int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if max <= 0 || len(runes) <= max {
		return value
	}
	return string(runes[:max]) + "…"
}

// notifyRoleUsers 向某个 CRM 管理角色的当前有效用户投递请求级站内消息。
// API 进程优先使用基础平台授权目录；独立 Worker 没有目录客户端时回退到本地
// 有效 OIDC 会话投影。收件人只用于消息投递，不构成业务授权。

// notifyRoleUsers 向某个 CRM 管理角色的当前有效用户投递请求级站内消息。
// API 进程优先使用基础平台授权目录；独立 Worker 没有目录客户端时回退到本地
// 有效 OIDC 会话投影。收件人只用于消息投递，不构成业务授权。
func (s *Service) notifyRoleUsers(ctx context.Context, tenant, role string, n WorkflowNotification) error {
	if s.notifications == nil {
		return nil
	}
	recipients, err := s.activeRoleUserIDs(ctx, tenant, role)
	if err != nil {
		return err
	}
	for _, recipientID := range recipients {
		n.TenantID, n.RecipientID = tenant, recipientID
		if err = s.notifyWorkflow(ctx, n); err != nil {
			return err
		}
	}
	return nil
}

const assignmentNotificationEventType = "PRESALE_ASSIGNMENT_SITE_NOTIFICATION"

const ProgressNotificationOutboxEventType = "PRESALE_PROGRESS_SITE_NOTIFICATION"

func (s *Service) recordAssignmentNotification(ctx context.Context, actor Actor, request *PresaleRequest, assignment *Assignment, eventType, reason string, occurredAt time.Time) error {
	evidence, err := s.recordAssignmentEvent(ctx, actor, request, assignment, eventType, reason, occurredAt)
	if err != nil {
		return err
	}
	eventID := evidence.EventID
	payload, err := json.Marshal(struct {
		AssignmentEventID uint64 `json:"assignment_event_id"`
	}{AssignmentEventID: evidence.ID})
	if err != nil {
		return err
	}
	return s.repo.CreateOutbox(ctx, &OutboxEvent{
		EventID: eventID, TenantID: actor.TenantID, EventType: assignmentNotificationEventType,
		AggregateType: "presale_assignment_event", AggregateID: fmt.Sprint(evidence.ID), Payload: payload,
		Status: "PENDING", CreatedAt: occurredAt,
	})
}

// 事件 ID 由租户、申请、任职记录和变更类型共同决定，生产者与通知投影使用同一算法，
// 因此 worker 重试不会为同一次人员变更重复创建通知。

// 事件 ID 由租户、申请、任职记录和变更类型共同决定，生产者与通知投影使用同一算法，
// 因此 worker 重试不会为同一次人员变更重复创建通知。
func AssignmentNotificationEventID(tenantID string, requestID, assignmentID uint64, eventType string) string {
	value := tenantID + "\x00" + fmt.Sprint(requestID) + "\x00" + fmt.Sprint(assignmentID) + "\x00" + eventType
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

// 进展通知 ID 同时绑定不可变进展记录、分派记录、收件人命名空间和身份，
// 保证同一收件人的重试可去重，同时兼容存量事件的命名空间。

// 进展通知 ID 同时绑定不可变进展记录、分派记录、收件人命名空间和身份，
// 保证同一收件人的重试可去重，同时兼容存量事件的命名空间。
func ProgressNotificationEventID(tenantID string, requestID, progressID, assignmentID uint64, namespace, recipientID, kind string) string {
	value := strings.Join([]string{tenantID, fmt.Sprint(requestID), fmt.Sprint(progressID), fmt.Sprint(assignmentID), namespace, recipientID, kind}, "\x00")
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func (s *Service) recordProgressNotifications(ctx context.Context, actor Actor, request *PresaleRequest, progress *ProgressLog, assignments []Assignment) error {
	if request == nil || progress == nil || progress.ID == 0 || actor.UserID == "" || actor.PersonID == "" {
		return ErrInvalidInput
	}
	type recipient struct {
		id, namespace, kind string
		assignmentID        uint64
	}
	recipients := make([]recipient, 0, len(assignments)+1)
	seenPersonRecipients := make(map[string]struct{}, len(assignments))
	// 申请人属于 CRM 用户命名空间，只有作者的 USER 身份相等时才排除其自通知。
	if request.ApplicantID != "" && request.ApplicantID != actor.UserID {
		recipients = append(recipients, recipient{id: request.ApplicantID, namespace: ProgressRecipientUser, kind: ProgressRecipientApplicant})
	}
	for _, assignment := range assignments {
		// 新执行人使用基础平台 user_id；PersonID 字段名仅为数据库兼容保留。
		authorID, namespace := actor.PersonID, ProgressRecipientPerson
		if assignment.AssigneeNamespace == ProgressRecipientUser {
			authorID, namespace = actor.UserID, ProgressRecipientUser
		}
		if !assignment.IsCurrent || assignment.ID == 0 || assignment.AssigneeID == "" || assignment.AssigneeID == authorID {
			continue
		}
		if _, duplicate := seenPersonRecipients[assignment.AssigneeID]; duplicate {
			continue
		}
		seenPersonRecipients[assignment.AssigneeID] = struct{}{}
		recipients = append(recipients, recipient{id: assignment.AssigneeID, namespace: namespace, kind: ProgressRecipientAssignee, assignmentID: assignment.ID})
	}
	for _, value := range recipients {
		eventID := ProgressNotificationEventID(actor.TenantID, request.ID, progress.ID, value.assignmentID, value.namespace, value.id, value.kind)
		evidence := &ProgressNotificationEvent{
			EventID: eventID, TenantID: actor.TenantID, RequestID: request.ID, ProgressID: progress.ID,
			AssignmentID: value.assignmentID, RecipientID: value.id, RecipientNamespace: value.namespace,
			RecipientKind: value.kind, AuthorUserID: actor.UserID, AuthorPersonID: actor.PersonID,
			RequestIDTrace: actor.RequestID, OccurredAt: progress.CreatedAt,
		}
		if err := s.repo.CreateProgressNotificationEvent(ctx, evidence); err != nil {
			return err
		}
		payload, err := json.Marshal(struct {
			ProgressNotificationEventID uint64 `json:"progress_notification_event_id"`
		}{ProgressNotificationEventID: evidence.ID})
		if err != nil {
			return err
		}
		if err = s.repo.CreateOutbox(ctx, &OutboxEvent{
			EventID: eventID, TenantID: actor.TenantID, EventType: ProgressNotificationOutboxEventType,
			AggregateType: "presale_progress_notification_event", AggregateID: fmt.Sprint(evidence.ID),
			Payload: payload, Status: "PENDING", CreatedAt: progress.CreatedAt,
		}); err != nil {
			return err
		}
	}
	return nil
}

// 新增进展只允许当前执行人操作。进展事实、每个收件人的不可变通知证据和 Outbox
// 在同一事务提交；申请人按 USER 命名空间通知，其他当前执行人按 PERSON 命名空间通知，
// 作者自身不会收到重复提醒。
