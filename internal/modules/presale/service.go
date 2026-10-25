package presale

import (
	"context"
	"encoding/json"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/ownerdirectory"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/audit"
	"regexp"
	"strings"
	"time"
)

const completionPolicy = "ALL_CURRENT_ASSIGNEES_HAVE_WORKLOG"

var decimalPattern = regexp.MustCompile(`^(?:0|[1-9]\d{0,7})(?:\.\d{1,2})?$`)

type Service struct {
	repo              Repository
	opportunities     OpportunityReader
	phones            PhoneProtector
	clock             Clock
	ids               IDGenerator
	dayHours          string
	timelineCursorKey []byte
	approvalTasks     ApprovalTaskResolver
	auditWriter       audit.Writer
	workerReadiness   WorkerReadiness
	workerMaxAge      time.Duration
	ownerDirectory    ownerdirectory.Catalog
	approvalRules     *ApprovalRuleStore
	notifications     WorkflowNotificationWriter
	freeze            FreezeChecker
}

// 新建申请依赖独立投递 worker 的持久化心跳，而不是仅检查当前进程配置。
// 没有任何新鲜心跳时新写入失败关闭；已经提交的幂等重放仍可读取，避免重试语义被可用性检查破坏。
func (s *Service) UseWorkerReadiness(readiness WorkerReadiness, maxAge time.Duration) *Service {
	s.workerReadiness = readiness
	s.workerMaxAge = maxAge
	return s
}

// 联系电话明文返回前必须先写入隐私审计；审计存储不可用时敏感数据接口失败关闭。

// 联系电话明文返回前必须先写入隐私审计；审计存储不可用时敏感数据接口失败关闭。
func (s *Service) UseAuditWriter(writer audit.Writer) *Service {
	s.auditWriter = writer
	return s
}

// UseOwnerDirectory 使执行人指派与基础平台当前有效授权目录保持一致。
// 服务端会重新解析用户姓名和组织，不信任浏览器提交的快照字段。

// UseOwnerDirectory 使执行人指派与基础平台当前有效授权目录保持一致。
// 服务端会重新解析用户姓名和组织，不信任浏览器提交的快照字段。
func (s *Service) UseOwnerDirectory(catalog ownerdirectory.Catalog) *Service {
	s.ownerDirectory = catalog
	return s
}

func NewService(repo Repository, opportunities OpportunityReader, phones PhoneProtector, clock Clock, ids IDGenerator) *Service {
	if clock == nil {
		clock = SystemClock{}
	}
	return &Service{repo: repo, opportunities: opportunities, phones: phones, clock: clock, ids: ids, dayHours: "8.00"}
}

// 创建申请先校验商机的数据范围，再检查租户级幂等键，避免猜测幂等键泄露他人申请。
// 审批属于 CRM 内部能力，申请与本地审批实例在同一事务进入待审批状态。

func (s *Service) activeRoleUserIDs(ctx context.Context, tenant, role string) ([]string, error) {
	role = strings.TrimSpace(role)
	if tenant == "" || role == "" {
		return nil, ErrInvalidInput
	}
	if s.ownerDirectory == nil {
		return s.repo.ListActiveUserIDsByRole(ctx, tenant, role)
	}
	recipients := make([]string, 0)
	seen := make(map[string]struct{})
	for pageNumber := 1; pageNumber <= ownerDirectoryMaxPages; pageNumber++ {
		page, err := s.ownerDirectory.List(ctx, ownerdirectory.Query{RoleCodes: []string{role}, Page: pageNumber, PageSize: approvalRecipientPageSize})
		if err != nil {
			return nil, err
		}
		for _, user := range page.Items {
			userID := strings.TrimSpace(user.ID)
			if userID == "" {
				continue
			}
			if _, exists := seen[userID]; exists {
				continue
			}
			seen[userID] = struct{}{}
			recipients = append(recipients, userID)
		}
		if len(page.Items) == 0 || int64(len(recipients)) >= page.Total || len(page.Items) < approvalRecipientPageSize {
			break
		}
	}
	return recipients, nil
}

func (s *Service) completeIfReady(ctx context.Context, r *PresaleRequest, current []Assignment, operator, trace string) (bool, error) {
	if r.Status != StatusExecuting || len(current) == 0 {
		return false, nil
	}
	ids := make([]string, 0, len(current))
	for _, a := range current {
		ids = append(ids, a.AssigneeID)
	}
	have, e := s.repo.AssigneeIDsWithValidWorklogs(ctx, r.TenantID, r.ID, ids)
	if e != nil {
		return false, e
	}
	for _, id := range ids {
		if !have[id] {
			return false, nil
		}
	}
	now := s.clock.Now()
	from := r.Status
	if e = s.repo.UpdateRequestVersioned(ctx, r, r.Version, map[string]any{"status": StatusCompleted, "completed_at": now, "updated_by": operator}); e != nil {
		return false, e
	}
	if e = s.statusLog(ctx, r, from, StatusCompleted, completionPolicy, "", operator, trace); e != nil {
		return false, e
	}
	return true, nil
}

func (s *Service) statusLog(ctx context.Context, r *PresaleRequest, from, to RequestStatus, trigger, reason, operator, trace string) error {
	return s.repo.CreateStatusLog(ctx, &StatusLog{TenantID: r.TenantID, RequestID: r.ID, FromStatus: from, ToStatus: to, Trigger: trigger, Reason: reason, OperatorID: operator, OccurredAt: s.clock.Now(), RequestIDTrace: trace})
}

func (s *Service) event(tenant, typ, aggregate, id string, payload any) (*OutboxEvent, error) {
	b, e := json.Marshal(payload)
	if e != nil {
		return nil, e
	}
	return &OutboxEvent{EventID: s.ids.NewID(), TenantID: tenant, EventType: typ, AggregateType: aggregate, AggregateID: id, Payload: b, Status: "PENDING", CreatedAt: s.clock.Now()}, nil
}

// 详情读取与列表使用相同资源范围：管理角色可读租户内任务，销售只读本人申请，
// 被分派的基础平台用户可读其当前或历史分派。分派角色不从 OIDC 角色猜测，
// 因而猜中租户内申请 ID 也不能绕过真实业务关系。
func (s *Service) requireReadable(ctx context.Context, actor Actor, value *PresaleRequest) error {
	if actor.HasRole("sales_director") || actor.HasRole("technical_director") || actor.HasRole("team_lead") || actor.HasRole("crm_super_admin") || actor.HasRole("auditor") {
		return nil
	}
	if actor.HasRole("sales") && value.ApplicantID == actor.UserID {
		return nil
	}
	if actor.PersonID == "" {
		return ErrForbidden
	}
	if value.TeamLeadPersonID != "" && value.TeamLeadPersonID == actor.PersonID {
		return nil
	}
	assignments, err := s.repo.ListAssignments(ctx, actor.TenantID, value.ID)
	if err != nil {
		return err
	}
	for _, assignment := range assignments {
		if assignment.AssigneeID == actor.PersonID {
			return nil
		}
	}
	return ErrForbidden
}
