package localidentity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/account"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/report"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type InvitationInput struct {
	CustomerID             uint64 `json:"customer_id"`
	ContactID              uint64 `json:"contact_id"`
	ExpectedContactVersion uint64 `json:"expected_contact_version"`
	IdempotencyKey         string `json:"-"`
}
type OperationResult struct {
	Operation     Operation      `json:"operation"`
	Invitation    *Invitation    `json:"invitation,omitempty"`
	ActivationURL string         `json:"activation_url,omitempty"`
	ReportSource  *ReportSource  `json:"report_source,omitempty"`
	ReportRequest *ReportSummary `json:"report_request,omitempty"`
}

// CreateInvitation 冻结真实联系人并建立独立操作号，再按真实回执逐步开户。
// 参数：ctx 为管理员会话，input 为本地客户、联系人版本及幂等键。
// 返回值：操作状态及可用邀请；依赖失败同时返回原操作号供精确重试。
func (s *Service) CreateInvitation(ctx context.Context, input InvitationInput) (OperationResult, error) {
	p, err := s.principal(ctx, PermissionInviteManage, true)
	if err != nil {
		return OperationResult{}, err
	}
	if input.CustomerID == 0 || input.ContactID == 0 || input.ExpectedContactVersion == 0 || !validText(input.IdempotencyKey, 128, true) {
		return OperationResult{}, ErrInvalid
	}
	var operation Operation
	hash := requestHash(input)
	err = s.Repo.WithTransaction(ctx, func(tx context.Context) error {
		if _, err := s.Repo.lockCustomer(tx, p.TenantID, input.CustomerID); err != nil {
			return err
		}
		err := s.Repo.tx(tx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND created_by=? AND idempotency_key=?", p.TenantID, p.UserID, input.IdempotencyKey).Take(&operation).Error
		if err == nil {
			if operation.RequestHash != hash {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if _, err := s.Repo.activeCustomer(tx, p.TenantID, input.CustomerID); err != nil {
			return err
		}
		var contact Contact
		if err := s.Repo.tx(tx).Where("tenant_id=? AND customer_id=? AND id=? AND version=? AND status='ACTIVE'", p.TenantID, input.CustomerID, input.ContactID, input.ExpectedContactVersion).Take(&contact).Error; err != nil {
			return ErrConflict
		}
		raw, err := s.Protector.Decrypt(tx, contact.PhoneCipher)
		if err != nil {
			return err
		}
		snapshot, err := s.Protector.Encrypt(tx, []byte(contact.DisplayName+"\x00"+string(raw)))
		if err != nil {
			return err
		}
		number, err := s.token(16)
		if err != nil {
			return err
		}
		operation = Operation{Record: s.record(p), OperationNo: number, IdempotencyKey: input.IdempotencyKey, RequestHash: hash, Kind: "INVITE", CustomerID: input.CustomerID, ContactID: input.ContactID, Stage: "PREPARED", Status: "PROCESSING", SnapshotCipher: snapshot}
		if err := s.Repo.tx(tx).Create(&operation).Error; err != nil {
			return err
		}
		return s.audit(tx, p, "PORTAL_LOCAL_INVITATION_PREPARED", "portal_local_operation", number)
	})
	if err != nil {
		return OperationResult{}, err
	}
	return s.ResumeOperation(ctx, operation.OperationNo)
}

// ResumeOperation 只按已持久化操作号恢复，不接受替换客户或联系人载荷。
// 参数：ctx 为当前管理员，number 为原操作号。
// 返回值：精确操作结果；失败保留阶段和记录，不能返回占位成功。
func (s *Service) ResumeOperation(ctx context.Context, number string) (OperationResult, error) {
	permission := PermissionInviteManage
	p0, ok := auth.FromContext(ctx)
	if !ok || p0.TenantID != s.TenantID || p0.UserID == "" || len(p0.Roles) != 1 || p0.Roles[0] != "portal_super_admin" {
		return OperationResult{}, apperror.ErrForbidden
	}
	var original Operation
	if err := s.Repo.tx(ctx).Where("tenant_id=? AND operation_no=?", s.TenantID, number).Take(&original).Error; err != nil {
		return OperationResult{}, ErrNotFound
	}
	if original.Kind == "DISABLE" {
		permission = PermissionAccessDisable
	} else if original.Kind == "REPORT" {
		permission = PermissionReportManage
	} else if original.Kind != "INVITE" {
		return OperationResult{}, ErrInvalid
	}
	p, err := s.principal(ctx, permission, true)
	if err != nil {
		return OperationResult{}, err
	}
	if original.Status == "COMPLETED" {
		return s.operationResult(ctx, original)
	}
	if original.Kind == "REPORT" {
		return s.queueReport(ctx, p, &original)
	}
	if s.Platform == nil || s.Accounts == nil {
		return OperationResult{Operation: original}, ErrUnavailable
	}
	owner, err := s.token(16)
	if err != nil {
		return OperationResult{}, err
	}
	now := s.now()
	until := now.Add(2 * time.Minute)
	claimed := s.Repo.tx(ctx).Model(&Operation{}).Where("tenant_id=? AND id=? AND status<>'COMPLETED' AND (lease_until IS NULL OR lease_until<=?)", p.TenantID, original.ID, now).Updates(map[string]any{"lease_owner": owner, "lease_until": until, "status": "PROCESSING"})
	if claimed.Error != nil {
		return OperationResult{Operation: original}, claimed.Error
	}
	if claimed.RowsAffected != 1 {
		return OperationResult{Operation: original}, ErrConflict
	}
	original.LeaseOwner = owner
	if original.Kind == "DISABLE" {
		err = s.resumeDisable(ctx, p, &original)
	} else {
		err = s.resumeInvitation(ctx, p, &original)
	}
	if err != nil {
		failure := ErrUnavailable
		summary := "依赖结果尚未核实，请按原操作号重试"
		if errors.Is(err, ErrConflict) || errors.Is(err, account.ErrIdentityDisabled) {
			failure = ErrConflict
			summary = "操作与现有身份或客户绑定冲突，请核查原操作"
		}
		updated := s.Repo.tx(ctx).Model(&Operation{}).Where("id=? AND tenant_id=? AND lease_owner=?", original.ID, p.TenantID, owner).Updates(map[string]any{"status": "RETRY_WAIT", "last_error": summary, "lease_owner": "", "lease_until": nil})
		if updated.Error != nil {
			return OperationResult{Operation: original}, apperror.Wrap(updated.Error, 503, ErrUnavailable.Code, ErrUnavailable.Message)
		}
		if reloadErr := s.Repo.tx(ctx).Where("id=? AND tenant_id=?", original.ID, p.TenantID).Take(&original).Error; reloadErr != nil {
			return OperationResult{Operation: original}, reloadErr
		}
		return OperationResult{Operation: original}, apperror.Wrap(errors.Join(failure, err), failure.HTTPStatus, failure.Code, failure.Message)
	}

	return s.operationResult(ctx, original)
}

// advanceOperation 只推进持有本轮租约的持久阶段，防止并发重试覆盖真实回执。
// 参数：ctx/p 为当前请求，operation 为原账本，stage/fields 为已核实结果。
// 返回值：更新或并发冲突错误。
func (s *Service) advanceOperation(ctx context.Context, p auth.Principal, operation *Operation, stage string, fields map[string]any) error {
	fields["stage"], fields["updated_at"], fields["updated_by"] = stage, s.now(), p.UserID
	result := s.Repo.tx(ctx).Model(&Operation{}).Where("id=? AND tenant_id=? AND lease_owner=? AND stage=?", operation.ID, p.TenantID, operation.LeaseOwner, operation.Stage).Updates(fields)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrConflict
	}
	operation.Stage = stage
	return nil
}

// resumeInvitation 依据冻结联系人执行真实平台开户、角色与客户绑定后生成本地邀请。
// 参数：ctx/p 为真实管理请求，op 为持租约的操作账本。
// 返回值：任一未确认的外部回执或本地事务错误。
func (s *Service) resumeInvitation(ctx context.Context, p auth.Principal, op *Operation) error {
	if _, err := s.Repo.activeCustomer(ctx, p.TenantID, op.CustomerID); err != nil {
		return err
	}
	snapshot, err := s.Protector.Decrypt(ctx, op.SnapshotCipher)
	if err != nil {
		return err
	}
	parts := strings.SplitN(string(snapshot), "\x00", 2)
	if len(parts) != 2 {
		return ErrInvalid
	}
	if op.Stage == "PREPARED" {
		identity, err := s.Platform.Provision(ctx, op.OperationNo, ContactInput{CustomerID: op.CustomerID, DisplayName: parts[0], Phone: parts[1]})
		if err != nil {
			return err
		}
		if !validText(identity.PlatformUserID, 128, true) || !validText(identity.AccountNo, 32, true) {
			return ErrUnavailable
		}
		op.PlatformUserID, op.AccountNo = identity.PlatformUserID, identity.AccountNo
	}
	if err := s.reserveInvitationIdentity(ctx, p, op, parts[0]); err != nil {
		return err
	}
	if op.Stage == "IDENTITY_READY" {
		if err := s.advanceReservedInvitation(ctx, p, op, "ROLE_READY", func(tx context.Context) error {
			return s.Platform.AssignRole(tx, op.PlatformUserID, op.OperationNo)
		}); err != nil {
			return err
		}
	}
	if op.Stage == "ROLE_READY" {
		if err := s.advanceReservedInvitation(ctx, p, op, "BINDING_READY", func(tx context.Context) error {
			return s.Platform.BindCustomer(tx, op.PlatformUserID, op.CustomerID, op.OperationNo)
		}); err != nil {
			return err
		}
	}
	if op.Stage != "BINDING_READY" {
		return ErrConflict
	}
	token, err := s.token(32)
	if err != nil {
		return err
	}
	cipher, err := s.Protector.Encrypt(ctx, []byte(token))
	if err != nil {
		return err
	}
	return s.withInvitationTransaction(ctx, p, op, func(tx context.Context) error {
		if err := s.checkInvitationLink(tx, p, op); err != nil {
			return err
		}
		if err := s.Repo.tx(tx).Model(&Invitation{}).Where("tenant_id=? AND customer_id=? AND contact_id=? AND status='PENDING'", p.TenantID, op.CustomerID, op.ContactID).Updates(map[string]any{"status": "REVOKED", "revoked_reason": "管理员重新签发邀请", "updated_at": s.now()}).Error; err != nil {
			return err
		}
		invite := Invitation{Record: s.record(p), InviteNo: op.OperationNo, CustomerID: op.CustomerID, ContactID: op.ContactID, PlatformUserID: op.PlatformUserID, AccountNo: op.AccountNo, TokenHash: s.digest("local-invite", token), TokenCipher: cipher, Status: "PENDING", ExpiresAt: s.now().Add(24 * time.Hour)}
		if err := s.Repo.tx(tx).Create(&invite).Error; err != nil {
			return err
		}
		if err := s.advanceOperation(tx, p, op, "INVITE_READY", map[string]any{"result_id": invite.ID, "status": "COMPLETED", "lease_owner": "", "lease_until": nil, "last_error": ""}); err != nil {
			return err
		}
		op.ResultID, op.Status = invite.ID, "COMPLETED"
		return s.audit(tx, p, "PORTAL_LOCAL_INVITATION_READY", "portal_local_invitation", fmt.Sprint(invite.ID))
	})
}

// operationResult 返回真实账本及仍可用的一次性邀请，不公开令牌密文。
// 参数：ctx 为请求，op 为已查询账本。
// 返回值：当前操作与可用邀请或安全材料错误。
func (s *Service) operationResult(ctx context.Context, op Operation) (OperationResult, error) {
	result := OperationResult{Operation: op}
	if op.Kind == "REPORT" {
		var source ReportSource
		if err := s.Repo.tx(ctx).Where("tenant_id=? AND operation_no=?", op.TenantID, op.OperationNo).Take(&source).Error; err != nil {
			return result, err
		}
		var request report.Request
		if err := s.Repo.tx(ctx).Where("tenant_id=? AND id=? AND customer_id=?", op.TenantID, source.RequestID, source.CustomerID).Take(&request).Error; err != nil {
			return result, err
		}
		result.ReportSource, result.ReportRequest = &source, &ReportSummary{RequestID: request.ID, RequestNo: request.RequestNo, Status: string(request.Status)}
		return result, nil
	}
	if op.Kind != "INVITE" || op.ResultID == 0 {
		return result, nil
	}
	var invite Invitation
	if err := s.Repo.tx(ctx).Where("tenant_id=? AND id=?", op.TenantID, op.ResultID).Take(&invite).Error; err != nil {
		return result, err
	}
	result.Invitation = &invite
	if invite.Status == "PENDING" && invite.ExpiresAt.After(s.now()) {
		token, err := s.Protector.Decrypt(ctx, invite.TokenCipher)
		if err != nil {
			return result, err
		}
		result.ActivationURL = strings.TrimRight(s.PublicURL, "/") + "/activate?token=" + string(token)
	}
	return result, nil
}

// ListOperations 返回有界的真实失败和完成记录，不隐藏半完成状态。
// 参数：ctx 为管理员会话，customer 为可选客户过滤。
// 返回值：最近一百个本租户操作或权限错误。
func (s *Service) ListOperations(ctx context.Context, customer uint64) ([]Operation, error) {
	current, ok := auth.FromContext(ctx)
	if !ok {
		return nil, apperror.ErrForbidden
	}
	permission := ""
	kinds := []string{}
	for _, pair := range [][2]string{{PermissionInviteManage, "INVITE"}, {PermissionAccessDisable, "DISABLE"}, {PermissionReportManage, "REPORT"}} {
		if current.HasPermission(pair[0]) {
			permission = pair[0]
			kinds = append(kinds, pair[1])
		}
	}
	if permission == "" {
		return nil, apperror.ErrForbidden
	}
	p, err := s.principal(ctx, permission, false)
	if err != nil {
		return nil, err
	}
	q := s.Repo.tx(ctx).Where("tenant_id=? AND kind IN ?", p.TenantID, kinds)
	if customer > 0 {
		q = q.Where("customer_id=?", customer)
	}
	rows := []Operation{}
	err = q.Order("id DESC").Limit(100).Find(&rows).Error
	return rows, err
}

// RevokeInvitation 撤销未消费邀请，不擅自撤销已激活客户的访问权限。
// 参数：ctx 为管理员，id/version 为邀请并发坐标，reason 为撤销原因。
// 返回值：事务或版本冲突错误。
func (s *Service) RevokeInvitation(ctx context.Context, id, version uint64, reason string) error {
	p, err := s.principal(ctx, PermissionInviteManage, true)
	if err != nil {
		return err
	}
	if !validText(reason, 500, true) {
		return ErrInvalid
	}
	return s.Repo.WithTransaction(ctx, func(tx context.Context) error {
		result := s.Repo.tx(tx).Model(&Invitation{}).Where("tenant_id=? AND id=? AND version=? AND status='PENDING'", p.TenantID, id, version).Updates(map[string]any{"status": "REVOKED", "version": gorm.Expr("version+1"), "revoked_reason": reason, "updated_at": s.now(), "updated_by": p.UserID})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrConflict
		}
		return s.audit(tx, p, "PORTAL_LOCAL_INVITATION_REVOKED", "portal_local_invitation", fmt.Sprint(id))
	})
}

// lockCustomer 在本地事务中串行客户停用和邀请激活。
// 参数：ctx 为事务，tenant/id 为可信客户范围。
// 返回值：锁定客户或存储错误。
func (r *Repository) lockCustomer(ctx context.Context, tenant string, id uint64) (Customer, error) {
	var row Customer
	err := r.tx(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND id=?", tenant, id).Take(&row).Error
	return row, err
}
