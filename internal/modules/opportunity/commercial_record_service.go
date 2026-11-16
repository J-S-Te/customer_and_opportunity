package opportunity

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shopspring/decimal"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/audit"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/pagination"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
)

const commercialRecordCreateOperation = "COMMERCIAL_RECORD_CREATE"

type CommercialRecordService struct {
	repo    CommercialRecordRepository
	parents Repository
	audit   audit.Writer
	now     func() time.Time
}

// NewCommercialRecordService 用途：组装人工台账服务，复用商机范围、事务和审计边界。
// 参数：repo 为台账仓储，parents 为现有商机仓储，writer 为现有审计写入器。
// 返回值：独立记录人工事实、不推进商机状态的台账服务。
func NewCommercialRecordService(repo CommercialRecordRepository, parents Repository, writer audit.Writer) *CommercialRecordService {
	return &CommercialRecordService{repo: repo, parents: parents, audit: writer, now: func() time.Time { return time.Now().UTC() }}
}

// authorize 用途：先确认会话权限和父商机可见，防止利用台账或重放探测其他商机。
// 参数：ctx 为可信身份上下文，id 为父商机，write 表示登记或更正操作。
// 返回值：已授权主体；缺权限、未登录或父商机不可见时返回既有错误。
func (s *CommercialRecordService) authorize(ctx context.Context, id uint64, write bool) (auth.Principal, error) {
	p, err := requirePrincipal(ctx)
	if err != nil {
		return p, err
	}
	permission := "opportunity.read"
	if write {
		permission = "opportunity.update"
	}
	if !p.HasPermission(permission) {
		return p, apperror.ErrForbidden
	}
	_, err = s.parents.FindByID(ctx, p, id)
	return p, err
}

// lockParent 用途：在写事务中锁定父商机，防止并发作废后继续登记资料。
// 参数：ctx 为事务上下文，p 为可信主体，id 为父商机。
// 返回值：成功为空；父对象不可见或已作废时返回业务错误。
func (s *CommercialRecordService) lockParent(ctx context.Context, p auth.Principal, id uint64) error {
	value, err := s.parents.FindByIDForUpdate(ctx, p, id)
	if err != nil {
		return err
	}
	if value.Status == StatusVoid {
		return ErrInactive
	}
	return nil
}

// Create 用途：创建人工报价或投标资料，并按父商机、操作者及请求载荷精确重放。
// 参数：ctx 为身份上下文，id 为父商机，input 为资料及必填幂等键。
// 返回值：初次创建时的资料 DTO；校验、范围、依赖或幂等冲突时返回错误。
func (s *CommercialRecordService) Create(
	ctx context.Context, id uint64, input CommercialRecordRequest,
) (*CommercialRecordResponse, error) {
	p, err := s.authorize(ctx, id, true)
	if err != nil {
		return nil, err
	}
	input, key, hash, err := prepareCommercialCreate(input)
	if err != nil {
		return nil, err
	}
	if len(input.AttachmentIDs) > 0 && !p.HasPermission("opportunity.attachment.read") {
		return nil, apperror.ErrForbidden
	}
	var result *CommercialRecordResponse
	err = s.repo.WithTransaction(ctx, func(tx context.Context) error {
		if err := s.lockParent(tx, p, id); err != nil {
			return err
		}
		prior, err := s.parents.FindChangeIdempotencyForUpdate(
			tx, p.TenantID, id, commercialRecordCreateOperation, p.UserID, key,
		)
		if err != nil {
			return err
		}
		if prior != nil {
			result, err = replayCommercialRecord(prior, p, id, key, hash)
		} else {
			result, err = s.persistCreate(tx, p, id, input, key, hash)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// prepareCommercialCreate 用途：校验幂等键并计算原始请求摘要，再归一化登记字段。
// 参数：input 为保留原始金额表示及文本字段的登记请求。
// 返回值：归一化请求、幂等键、请求摘要；无效字段或键返回错误。
func prepareCommercialCreate(input CommercialRecordRequest) (CommercialRecordRequest, string, string, error) {
	key := strings.TrimSpace(input.IdempotencyKey)
	if key == "" {
		return input, "", "", ErrIdempotencyRequired
	}
	if len(key) > 128 {
		return input, "", "", ErrIdempotencyKeyTooLong
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return input, "", "", err
	}
	normalized, err := normalizeCommercialRecord(input)
	return normalized, key, bytesHash(encoded), err
}

// persistCreate 用途：在父商机锁定后原子保存资料、关联、精确响应及业务审计。
// 参数：ctx 为事务，p 为主体，id 为父商机，input 为合法字段，key、hash 为幂等坐标。
// 返回值：创建时资料 DTO；任一持久化失败返回错误并由调用方回滚。
func (s *CommercialRecordService) persistCreate(
	ctx context.Context, p auth.Principal, id uint64,
	input CommercialRecordRequest, key, hash string,
) (*CommercialRecordResponse, error) {
	attachments, err := s.checkedAttachments(ctx, p, id, input.AttachmentIDs)
	if err != nil {
		return nil, err
	}
	value := &CommercialRecord{PublicID: request.NewID(), OpportunityID: id, SourceKind: "MANUAL"}
	applyCommercialRecord(value, input)
	value.TenantID, value.CreatedBy, value.UpdatedBy = p.TenantID, p.UserID, p.UserID
	value.CreatedByName, value.UpdatedByName = p.DisplayName, p.DisplayName
	now := s.now()
	value.CreatedAt, value.UpdatedAt, value.Version = now, now, 1
	if err = s.repo.CreateRecord(ctx, value); err != nil {
		return nil, err
	}
	if err = s.repo.ReplaceRecordAttachments(ctx, value, attachments); err != nil {
		return nil, err
	}
	result := toCommercialRecord(value, attachments)
	responseJSON, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if err = s.parents.CreateChangeIdempotency(ctx, &ChangeIdempotency{
		TenantID: p.TenantID, OpportunityID: id, Operation: commercialRecordCreateOperation,
		ActorID: p.UserID, Key: key, RequestHash: hash, ResponseJSON: responseJSON, CreatedAt: now,
	}); err != nil {
		return nil, err
	}
	return &result, s.writeAudit(ctx, p, value, "CREATE", nil, &result, "")
}

// replayCommercialRecord 用途：复核持久化快照的主体与父范围后还原创建时结果。
// 参数：prior 为幂等行，p 为当前主体，id、key、hash 为本次请求绑定。
// 返回值：精确历史响应；绑定不符或损坏快照返回冲突，不泄漏其他资料。
func replayCommercialRecord(prior *ChangeIdempotency, p auth.Principal, id uint64, key, hash string) (*CommercialRecordResponse, error) {
	if prior.TenantID != p.TenantID || prior.OpportunityID != id ||
		prior.Operation != commercialRecordCreateOperation || prior.ActorID != p.UserID ||
		prior.Key != key || prior.RequestHash != hash {
		return nil, ErrIdempotencyConflict
	}
	var result CommercialRecordResponse
	if err := json.Unmarshal(prior.ResponseJSON, &result); err != nil {
		return nil, err
	}
	if result.ID == "" || result.OpportunityID != id || result.SourceKind != "MANUAL" || result.CreatedBy != p.UserID {
		return nil, ErrIdempotencyConflict
	}
	if !p.HasPermission("opportunity.attachment.read") {
		result.Attachments = []AttachmentResponse{}
	}
	return &result, nil
}

// checkedAttachments 用途：只允许引用同租户、同商机且已通过安全校验的附件。
// 参数：ctx 为事务上下文，p 为可信主体，id 为父商机，ids 为附件公开编号。
// 返回值：按请求顺序排列的可信附件；无权、越界或未就绪时返回错误。
func (s *CommercialRecordService) checkedAttachments(ctx context.Context, p auth.Principal, id uint64, ids []string) ([]Attachment, error) {
	if len(ids) == 0 {
		return []Attachment{}, nil
	}
	if !p.HasPermission("opportunity.attachment.read") {
		return nil, apperror.ErrForbidden
	}
	values, err := s.repo.ResolveAttachments(ctx, p.TenantID, id, ids, true)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]Attachment, len(values))
	for _, value := range values {
		if value.TenantID != p.TenantID || value.OpportunityID != id {
			return nil, ErrAttachmentNotFound
		}
		byID[value.PublicID] = value
	}
	result := make([]Attachment, 0, len(ids))
	for _, attachmentID := range ids {
		value, ok := byID[attachmentID]
		if !ok {
			return nil, ErrAttachmentNotFound
		}
		if value.ScanStatus != AttachmentClean {
			return nil, ErrAttachmentNotReady
		}
		result = append(result, value)
	}
	return result, nil
}

// List 用途：分页读取人工资料与历史作废记录，按当前附件权限隐藏文件信息。
// 参数：ctx 为身份上下文，id 为父商机，page、size 为分页参数。
// 返回值：分页 DTO；无权、父不可见、分页或仓储错误时返回错误。
func (s *CommercialRecordService) List(ctx context.Context, id uint64, page, size int) (pagination.Page[CommercialRecordResponse], error) {
	result := pagination.Page[CommercialRecordResponse]{Items: []CommercialRecordResponse{}, Page: page, PageSize: size}
	p, err := s.authorize(ctx, id, false)
	if err != nil {
		return result, err
	}
	if err = validatePagination(page, size); err != nil {
		return result, err
	}
	values, total, err := s.repo.ListRecords(ctx, p.TenantID, id, page, size)
	if err != nil {
		return result, err
	}
	attachments := map[uint64][]Attachment{}
	if p.HasPermission("opportunity.attachment.read") {
		ids := make([]uint64, 0, len(values))
		for _, value := range values {
			ids = append(ids, value.ID)
		}
		attachments, err = s.repo.ListRecordAttachments(ctx, p.TenantID, id, ids)
		if err != nil {
			return result, err
		}
	}
	result.Total = total
	for i := range values {
		result.Items = append(result.Items, toCommercialRecord(&values[i], attachments[values[i].ID]))
	}
	return result, nil
}

// Update 用途：以资料自身版本更正人工事实，并原子保存前后审计快照。
// 参数：ctx 为身份上下文，id 为父商机，recordID 为公开编号，input 为更正及旧版本。
// 返回值：新版本 DTO；作废、版本冲突、关联或字段错误时返回错误。
func (s *CommercialRecordService) Update(ctx context.Context, id uint64, recordID string, input CommercialRecordRequest) (*CommercialRecordResponse, error) {
	return s.change(ctx, id, recordID, input.Version, func(tx context.Context, p auth.Principal, value *CommercialRecord) (*CommercialRecordResponse, error) {
		normalized, err := normalizeCommercialRecord(input)
		if err != nil {
			return nil, err
		}
		attachments, err := s.checkedAttachments(tx, p, id, normalized.AttachmentIDs)
		if err != nil {
			return nil, err
		}
		before, err := s.auditSnapshot(tx, value)
		if err != nil {
			return nil, err
		}
		applyCommercialRecord(value, normalized)
		value.UpdatedBy, value.UpdatedByName, value.UpdatedAt = p.UserID, p.DisplayName, s.now()
		if err = s.repo.SaveRecord(tx, value, input.Version); err != nil {
			return nil, err
		}
		if p.HasPermission("opportunity.attachment.read") {
			if err = s.repo.ReplaceRecordAttachments(tx, value, attachments); err != nil {
				return nil, err
			}
		}
		result := toCommercialRecord(value, attachments)
		// 受限人员看不到原关联，空数组只能表达更正文字，不能隐式解除隐藏文件。
		if !p.HasPermission("opportunity.attachment.read") {
			result.Attachments = before.Attachments
		}
		return &result, s.writeAudit(tx, p, value, "UPDATE", before, &result, "")
	})
}

// Void 用途：撤销错误录入并保存原因，保留原业务状态以区分离线取消和资料作废。
// 参数：ctx 为身份上下文，id 为父商机，recordID 为公开编号，input 为版本及原因。
// 返回值：已作废 DTO；重复命令、旧版本、缺原因或依赖错误时返回错误。
func (s *CommercialRecordService) Void(ctx context.Context, id uint64, recordID string, input CommercialRecordVoidRequest) (*CommercialRecordResponse, error) {
	return s.change(ctx, id, recordID, input.Version, func(tx context.Context, p auth.Principal, value *CommercialRecord) (*CommercialRecordResponse, error) {
		reason := strings.TrimSpace(input.Reason)
		if reason == "" || utf8.RuneCountInString(reason) > 500 {
			return nil, ErrCommercialRecordInvalid
		}
		before, err := s.auditSnapshot(tx, value)
		if err != nil {
			return nil, err
		}
		value.IsVoid, value.VoidReason = true, reason
		value.UpdatedBy, value.UpdatedByName, value.UpdatedAt = p.UserID, p.DisplayName, s.now()
		if err = s.repo.SaveRecord(tx, value, input.Version); err != nil {
			return nil, err
		}
		result := *before
		result.IsVoid, result.VoidReason, result.Version = true, reason, value.Version
		result.UpdatedBy, result.UpdatedByName, result.UpdatedAt = value.UpdatedBy, value.UpdatedByName, value.UpdatedAt
		return &result, s.writeAudit(tx, p, value, "VOID", before, &result, reason)
	})
}

// change 用途：统一更正和作废的父锁、租户范围及版本前置条件。
// 参数：ctx 为上下文，id 和 recordID 为父与资料，version 为旧版本，fn 为事务内变更。
// 返回值：变更结果；任何前置或回调错误均使事务回滚。
func (s *CommercialRecordService) change(ctx context.Context, id uint64, recordID string, version uint64, fn func(context.Context, auth.Principal, *CommercialRecord) (*CommercialRecordResponse, error)) (*CommercialRecordResponse, error) {
	p, err := s.authorize(ctx, id, true)
	if err != nil {
		return nil, err
	}
	var result *CommercialRecordResponse
	err = s.repo.WithTransaction(ctx, func(tx context.Context) error {
		if err := s.lockParent(tx, p, id); err != nil {
			return err
		}
		value, err := s.repo.FindRecord(tx, p.TenantID, id, recordID, true)
		if err != nil {
			return err
		}
		if version == 0 || value.Version != version {
			return ErrVersionConflict
		}
		if value.IsVoid {
			return ErrCommercialRecordVoid
		}
		result, err = fn(tx, p, value)
		return err
	})
	if err != nil {
		return nil, err
	}
	if !p.HasPermission("opportunity.attachment.read") {
		result.Attachments = []AttachmentResponse{}
	}
	return result, nil
}

// auditSnapshot 用途：在已授权并锁定的事务中读取完整关联，保存真实变更证据。
// 参数：ctx 为事务上下文，value 为已核对租户和父商机的当前资料。
// 返回值：内部审计快照；对外回执另按当前权限脱敏，读取失败则回滚。
func (s *CommercialRecordService) auditSnapshot(ctx context.Context, value *CommercialRecord) (*CommercialRecordResponse, error) {
	values, err := s.repo.ListRecordAttachments(
		ctx, value.TenantID, value.OpportunityID, []uint64{value.ID},
	)
	if err != nil {
		return nil, err
	}
	result := toCommercialRecord(value, values[value.ID])
	return &result, nil
}

// applyCommercialRecord 用途：将已验证的人工字段写入资料实体，不触碰父商机。
// 参数：value 为目标实体，input 为已归一化请求。
// 返回值：无。
func applyCommercialRecord(value *CommercialRecord, input CommercialRecordRequest) {
	value.Type, value.Title, value.DocumentNo = input.Type, input.Title, input.DocumentNo
	value.RecordDate, _ = time.Parse("2006-01-02", input.RecordDate)
	value.Status, value.Notes = input.Status, input.Notes
	value.Amount = nil
	if input.Amount != nil {
		amount := decimal.RequireFromString(*input.Amount)
		value.Amount = &amount
	}
}

// writeAudit 用途：在业务事务中保存人工台账的真实操作者、原因和前后快照。
// 参数：ctx 为事务，p 为主体，value 为资料，operation、before、after、reason 为变更证据。
// 返回值：审计写入结果；失败必须向上传播以回滚业务。
func (s *CommercialRecordService) writeAudit(ctx context.Context, p auth.Principal, value *CommercialRecord, operation string, before, after *CommercialRecordResponse, reason string) error {
	return s.audit.Write(ctx, audit.Event{
		TenantID: p.TenantID, Module: "opportunity_commercial_record", Operation: operation,
		ResourceType: "opportunity_commercial_record", ResourceID: value.PublicID,
		ActorID: p.UserID, ActorNameSnapshot: p.DisplayName, BeforeJSON: audit.JSON(before),
		AfterJSON: audit.JSON(after), Reason: reason, Result: "SUCCESS",
	})
}
