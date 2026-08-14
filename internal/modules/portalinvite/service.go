package portalinvite

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/audit"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	requestctx "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
	"gorm.io/gorm"
)

const inviteTTL = 2 * time.Hour

const accessDisableRequestLease = time.Minute

type Service struct {
	repo      Repository
	customers CustomerReader
	platform  PlatformProvisioner
	portal    PortalProvisioner
	binding   PlatformBindingWriter
	audit     audit.Writer
	pepper    []byte
	publicURL string
	clock     Clock
	random    RandomSource
	protector OperationProtector
	// platformOnly 表示门户本地映射已退役（Phase 5）：平台客户绑定是唯一权威，
	// 不再调用门户 provision，绑定失败即进入重试等待。
	platformOnly bool
}

// ServiceOption 允许装配层在 Phase 2 双写过渡期注入平台绑定适配器；未注入时行为与旧版一致。
type ServiceOption func(*Service)

// WithPlatformBindingWriter 打开平台客户绑定双写（nil 或未配置时关闭）。
func WithPlatformBindingWriter(writer PlatformBindingWriter) ServiceOption {
	return func(service *Service) {
		if writer != nil {
			service.binding = writer
		}
	}
}

// WithPlatformBindingOnly 进入 Phase 5 单写模式：跳过门户映射调用，平台绑定成为唯一权威。
// 调用方必须同时注入 PlatformBindingWriter。
func WithPlatformBindingOnly() ServiceOption {
	return func(service *Service) { service.platformOnly = true }
}

type AccessDisableService struct {
	repo      AccessDisableRepository
	customers CustomerAccessChecker
	platform  PlatformRoleRevoker
	portal    PortalMappingDisabler
	binding   PlatformBindingDisabler
	audit     audit.Writer
	clock     Clock
	random    RandomSource
	// platformOnly 表示门户映射表已退役（Phase 5）：跳过门户禁用调用，平台绑定禁用
	// 成为唯一远程收敛点，失败即进入重试等待。
	platformOnly bool
}

// AccessDisableServiceOption 允许装配层在 Phase 2 双写过渡期注入平台绑定禁用适配器。
type AccessDisableServiceOption func(*AccessDisableService)

// WithPlatformBindingDisabler 打开禁用 saga 的平台绑定双写（nil 或未配置时关闭）。
func WithPlatformBindingDisabler(disabler PlatformBindingDisabler) AccessDisableServiceOption {
	return func(service *AccessDisableService) {
		if disabler != nil {
			service.binding = disabler
		}
	}
}

// WithPlatformOnlyDisable 进入 Phase 5 单写模式：跳过门户禁用调用，平台绑定禁用成为
// 唯一远程收敛点。调用方必须同时注入 PlatformBindingDisabler。
func WithPlatformOnlyDisable() AccessDisableServiceOption {
	return func(service *AccessDisableService) { service.platformOnly = true }
}

func NewAccessDisableService(repo AccessDisableRepository, customers CustomerAccessChecker, platform PlatformRoleRevoker, portal PortalMappingDisabler, auditWriter audit.Writer, clock Clock, random RandomSource, options ...AccessDisableServiceOption) *AccessDisableService {
	service := &AccessDisableService{repo: repo, customers: customers, platform: platform, portal: portal, audit: auditWriter, clock: clock, random: random}
	for _, apply := range options {
		apply(service)
	}
	return service
}

func (s *AccessDisableService) Disable(ctx context.Context, customerID uint64, command DisableAccessRequest) (*DisableAccessResult, error) {
	principal, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	reason := strings.TrimSpace(command.Reason)
	key := strings.TrimSpace(command.IdempotencyKey)
	if customerID == 0 || reason == "" || len([]rune(reason)) > 500 || !validPortalIntegrationString(key, 128) {
		return nil, ErrInvalidArgument
	}
	if !principal.HasPermission("portal_account.disable") {
		return nil, apperror.ErrForbidden
	}
	if s.customers == nil {
		return nil, dependency(errors.New("customer access checker is not configured"))
	}
	accessible, err := s.customers.CanAccessCustomer(ctx, principal, customerID)
	if err != nil {
		return nil, err
	}
	if !accessible {
		return nil, ErrNotFound
	}
	requestHash := accessDisableRequestHash(principal, customerID, reason)
	operation, err := s.repo.FindAccessDisableOperation(ctx, principal.TenantID, principal.UserID, key)
	if err == nil {
		if operation.CustomerID != customerID || operation.RequestHash != requestHash {
			return nil, ErrIdempotencyConflict
		}
		now := s.clock.Now().UTC()
		if operation.Status == DisableStatusRetryWait && operation.NextRetryAt != nil && operation.NextRetryAt.After(now) {
			return nil, dependency(errors.New("portal access disable retry is not due"))
		}
		if operation.Status == DisableStatusDeadLetter {
			return nil, dependency(errors.New("portal access disable requires reconciliation"))
		}
		owner := accessDisableRequestOwner(ctx)
		if operation.Status != DisableStatusCompleted {
			if operation.Status == DisableStatusProcessing && operation.LockedUntil != nil && operation.LockedUntil.After(now) && operation.LockedBy != owner {
				return nil, dependency(errors.New("portal access disable is already processing"))
			}
			if !accessDisableLeaseOwned(operation, owner, now) {
				if claimErr := s.repo.ClaimAccessDisableOperation(ctx, operation, owner, now, now.Add(accessDisableRequestLease)); claimErr != nil {
					return nil, dependency(errors.New("portal access disable is already processing"))
				}
			}
		}
		return s.resumeDisable(ctx, principal, operation, owner)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	randomBytes, err := s.random.Bytes(12)
	if err != nil {
		return nil, err
	}
	now := s.clock.Now().UTC()
	owner := accessDisableRequestOwner(ctx)
	lockedUntil := now.Add(accessDisableRequestLease)
	operation = &AccessDisableOperation{
		Model:       database.Model{TenantID: principal.TenantID, CreatedBy: principal.UserID, UpdatedBy: principal.UserID, CreatedAt: now, UpdatedAt: now, Version: 1},
		OperationNo: "PD" + strings.ToUpper(hex.EncodeToString(randomBytes)), ActorID: principal.UserID, IdempotencyKey: key,
		RequestHash: requestHash, CustomerID: customerID, Reason: reason, Stage: DisableStagePrepared, Status: DisableStatusProcessing,
		LockedBy: owner, LockedUntil: &lockedUntil,
	}
	err = s.repo.WithTransaction(ctx, func(txCtx context.Context) error {
		if lockErr := s.repo.LockCustomer(txCtx, principal.TenantID, customerID); lockErr != nil {
			return lockErr
		}
		if fences, ok := s.repo.(accessDisableFenceRepository); ok {
			blocked, fenceErr := fences.HasBlockingAccessDisable(txCtx, principal.TenantID, customerID)
			if fenceErr != nil {
				return fenceErr
			}
			if blocked {
				return ErrVersionConflict
			}
		}
		link, linkErr := s.repo.FindIdentityLinkForUpdate(txCtx, principal.TenantID, customerID)
		if linkErr != nil {
			return linkErr
		}
		operation.IdentityLinkID = link.ID
		operation.IdentityLinkVersion = link.Version
		operation.ContactID = link.ContactID
		operation.PlatformUserID = link.PlatformUserID
		operation.PortalAccountID = link.PortalAccountID
		return s.repo.CreateAccessDisableOperation(txCtx, operation)
	})
	if err != nil {
		winner, findErr := s.repo.FindAccessDisableOperation(ctx, principal.TenantID, principal.UserID, key)
		if findErr != nil || winner.RequestHash != requestHash {
			return nil, err
		}
		operation = winner
		if operation.Status == DisableStatusCompleted && operation.Stage == DisableStageCompleted {
			return publicDisable(operation), nil
		}
		if operation.Status == DisableStatusDeadLetter || !accessDisableLeaseOwned(operation, owner, now) {
			return nil, dependency(errors.New("portal access disable is already processing"))
		}
	}
	return s.resumeDisable(ctx, principal, operation, owner)
}

// ResumeClaimed 只续跑恢复任务已领取的一条操作。Saga 创建时已完成授权并冻结租户、操作者、客户和远程主体，
// 因此恢复路径不重新依赖用户会话；但租约所有权仍是继续执行的必要条件。
func (s *AccessDisableService) ResumeClaimed(ctx context.Context, operation *AccessDisableOperation, workerID string) (*DisableAccessResult, error) {
	workerID = strings.TrimSpace(workerID)
	if operation == nil || operation.ID == 0 || operation.TenantID == "" || operation.ActorID == "" || workerID == "" ||
		operation.Status != DisableStatusProcessing || operation.LockedBy != workerID || operation.LockedUntil == nil || !operation.LockedUntil.After(s.clock.Now().UTC()) {
		return nil, ErrVersionConflict
	}
	principal := auth.Principal{TenantID: operation.TenantID, UserID: operation.ActorID}
	return s.resumeDisable(ctx, principal, operation, workerID)
}

func (s *AccessDisableService) resumeDisable(ctx context.Context, principal auth.Principal, operation *AccessDisableOperation, leaseOwner string) (*DisableAccessResult, error) {
	if operation.Stage == DisableStageCompleted {
		return publicDisable(operation), nil
	}
	if (s.platform == nil) || (s.portal == nil && !s.platformOnly) {
		return nil, dependency(errors.New("portal access disable integrations are not configured"))
	}
	now := s.clock.Now().UTC()
	if operation.Stage == DisableStagePrepared {
		// Phase 5 单写：门户映射表退役后不再调用门户禁用；平台绑定禁用与本地会话
		// 冻结（本仓库 CRM 侧链接）已经关闭访问。
		if !s.platformOnly {
			if err := s.portal.DisableMapping(ctx, operation.TenantID, operation.CustomerID, operation.PlatformUserID, operation.Reason, disableRemoteKey(operation, "mapping")); err != nil {
				return nil, s.failDisable(ctx, operation, leaseOwner, "PORTAL_MAPPING_DISABLE_FAILED", err)
			}
		}
		if err := s.repo.WithTransaction(ctx, func(txCtx context.Context) error {
			locked, lockErr := s.repo.FindAccessDisableOperationForUpdate(txCtx, operation.TenantID, operation.ID)
			if lockErr != nil {
				return lockErr
			}
			if locked.Stage != DisableStagePrepared {
				*operation = *locked
				return nil
			}
			if !accessDisableLeaseOwned(locked, leaseOwner, now) {
				return ErrVersionConflict
			}
			if linkErr := s.repo.DisableIdentityLink(txCtx, operation, operation.ActorID, now); linkErr != nil {
				return linkErr
			}
			revokeReason := "Portal access disabled: " + operation.Reason
			if runes := []rune(revokeReason); len(runes) > 500 {
				revokeReason = string(runes[:500])
			}
			if revokeErr := s.repo.RevokePending(txCtx, operation.TenantID, operation.CustomerID, operation.ActorID, revokeReason, now); revokeErr != nil {
				return revokeErr
			}
			if advanceErr := s.repo.AdvanceAccessDisableOperation(txCtx, locked, DisableStagePrepared, map[string]any{
				"stage": DisableStageMappingDisabled, "status": DisableStatusProcessing, "last_error_code": "", "last_error_summary": "",
			}, now); advanceErr != nil {
				return advanceErr
			}
			*operation = *locked
			return nil
		}); err != nil {
			return nil, err
		}
	}
	if operation.Stage == DisableStageMappingDisabled {
		if err := s.platform.RevokePortalRole(ctx, operation.PlatformUserID, disableRemoteKey(operation, "role")); err != nil {
			return nil, s.failDisable(ctx, operation, leaseOwner, "PLATFORM_ROLE_REVOKE_FAILED", err)
		}
		// Phase 2 双写：门户本地冻结与角色回收已关闭访问，平台绑定禁用失败不阻断收敛；
		// 失败入队绑定禁用补偿任务。Phase 5 单写时绑定禁用是唯一远程收敛点，失败即重试等待。
		if s.binding != nil {
			customerRef := strconv.FormatUint(operation.CustomerID, 10)
			bindingResult := "SUCCESS"
			if disableErr := s.binding.DisableCustomerBindingIdempotent(ctx, operation.PlatformUserID, customerRef, disableRemoteKey(operation, "binding")); disableErr != nil {
				bindingResult = "PENDING"
				compensationErr := s.repo.CreateCompensation(ctx, &CompensationTask{
					Model:          database.Model{TenantID: operation.TenantID, CreatedBy: operation.ActorID, UpdatedBy: operation.ActorID, CreatedAt: s.clock.Now().UTC(), UpdatedAt: s.clock.Now().UTC(), Version: 1},
					TaskNo:         disableRemoteKey(operation, "binding"), TaskType: CompensationBindingDisable,
					CustomerID:     operation.CustomerID, ContactID: operation.ContactID,
					PlatformUserID: operation.PlatformUserID, AccountNo: "EXT-" + strings.ToUpper(operation.PlatformUserID),
					Status:         CompensationPending, LastErrorCode: "PLATFORM_BINDING_DISABLE_FAILED",
				})
				if s.platformOnly {
					return nil, s.failDisable(ctx, operation, leaseOwner, "PLATFORM_BINDING_DISABLE_FAILED", errors.Join(disableErr, compensationErr))
				}
			}
			_ = s.audit.Write(ctx, audit.Event{TenantID: operation.TenantID, Module: "portal_invite", Operation: "PLATFORM_BINDING_DISABLE", ResourceType: "customer", ResourceID: fmt.Sprint(operation.CustomerID), ActorID: "portal-machine", AfterJSON: audit.JSON(map[string]any{"platform_user_id": operation.PlatformUserID, "customer_ref": customerRef}), Result: bindingResult})
		}
		completedAt := s.clock.Now().UTC()
		if err := s.repo.WithTransaction(ctx, func(txCtx context.Context) error {
			locked, lockErr := s.repo.FindAccessDisableOperationForUpdate(txCtx, operation.TenantID, operation.ID)
			if lockErr != nil {
				return lockErr
			}
			if locked.Stage == DisableStageCompleted {
				*operation = *locked
				return nil
			}
			if !accessDisableLeaseOwned(locked, leaseOwner, completedAt) {
				return ErrVersionConflict
			}
			if locked.Stage != DisableStageMappingDisabled {
				return ErrVersionConflict
			}
			if advanceErr := s.repo.AdvanceAccessDisableOperation(txCtx, locked, DisableStageMappingDisabled, map[string]any{
				"stage": DisableStageCompleted, "status": DisableStatusCompleted, "completed_at": completedAt, "last_error_code": "", "last_error_summary": "",
				"next_retry_at": nil, "locked_by": "", "locked_until": nil,
			}, completedAt); advanceErr != nil {
				return advanceErr
			}
			*operation = *locked
			return s.audit.Write(txCtx, audit.Event{TenantID: operation.TenantID, Module: "portal_invite", Operation: "DISABLE_ACCESS", ResourceType: "customer", ResourceID: fmt.Sprint(operation.CustomerID), ActorID: operation.ActorID, Reason: operation.Reason, AfterJSON: audit.JSON(map[string]any{"status": "DISABLED", "operation_no": operation.OperationNo}), Result: "SUCCESS"})
		}); err != nil {
			return nil, err
		}
	}
	if operation.Stage != DisableStageCompleted {
		return nil, dependency(errors.New("portal access disable operation has an unsupported stage"))
	}
	return publicDisable(operation), nil
}

func accessDisableRequestOwner(ctx context.Context) string {
	id := requestctx.ID(ctx)
	if id == "" {
		id = requestctx.NewID()
	}
	return "request:" + id
}

func accessDisableLeaseOwned(operation *AccessDisableOperation, owner string, now time.Time) bool {
	return owner != "" && operation.LockedBy == owner && operation.LockedUntil != nil && operation.LockedUntil.After(now)
}

func (s *AccessDisableService) failDisable(ctx context.Context, operation *AccessDisableOperation, leaseOwner, code string, cause error) error {
	now := s.clock.Now().UTC()
	summary := "Portal mapping disable is temporarily unavailable"
	if code == "PLATFORM_ROLE_REVOKE_FAILED" {
		summary = "Platform Portal role revocation is temporarily unavailable"
	}
	advanceErr := s.repo.WithTransaction(ctx, func(txCtx context.Context) error {
		locked, lockErr := s.repo.FindAccessDisableOperationForUpdate(txCtx, operation.TenantID, operation.ID)
		if lockErr != nil {
			return lockErr
		}
		if locked.Stage == DisableStageCompleted {
			*operation = *locked
			return nil
		}
		if !accessDisableLeaseOwned(locked, leaseOwner, now) {
			return ErrVersionConflict
		}
		nextRetryAt := now.Add(accessDisableBackoff(locked.Attempts + 1))
		status := DisableStatusRetryWait
		var persistedRetryAt any = nextRetryAt
		if locked.Attempts+1 >= 8 {
			status = DisableStatusDeadLetter
			persistedRetryAt = nil
		}
		if advanceErr := s.repo.AdvanceAccessDisableOperation(txCtx, locked, locked.Stage, map[string]any{
			"status": status, "attempts": gorm.Expr("attempts+1"), "last_error_code": code,
			"last_error_summary": summary, "next_retry_at": persistedRetryAt, "locked_by": "", "locked_until": nil,
		}, now); advanceErr != nil {
			return advanceErr
		}
		*operation = *locked
		operation.NextRetryAt = nil
		if status == DisableStatusRetryWait {
			operation.NextRetryAt = &nextRetryAt
		}
		if status == DisableStatusDeadLetter {
			return s.audit.Write(txCtx, audit.Event{TenantID: operation.TenantID, Module: "portal_invite", Operation: "DISABLE_ACCESS_RECOVERY", ResourceType: "customer", ResourceID: fmt.Sprint(operation.CustomerID), ActorID: operation.ActorID, Reason: operation.Reason, AfterJSON: audit.JSON(map[string]any{"status": DisableStatusDeadLetter, "stage": operation.Stage, "operation_no": operation.OperationNo, "error_code": code}), Result: "FAILED"})
		}
		return nil
	})
	if advanceErr != nil {
		return dependency(advanceErr)
	}
	return dependency(errors.New("portal access disable step failed"))
}

func accessDisableBackoff(attempt uint16) time.Duration {
	if attempt > 6 {
		attempt = 6
	}
	return time.Duration(1<<attempt) * time.Minute
}

func disableRemoteKey(operation *AccessDisableOperation, step string) string {
	if step == "role" {
		return operation.OperationNo + "R"
	}
	if step == "binding" {
		return operation.OperationNo + "B"
	}
	return operation.OperationNo + "M"
}

func accessDisableRequestHash(principal auth.Principal, customerID uint64, reason string) string {
	sum := sha256.Sum256([]byte(principal.TenantID + "\x00" + principal.UserID + "\x00" + fmt.Sprint(customerID) + "\x00" + reason))
	return hex.EncodeToString(sum[:])
}

func publicDisable(operation *AccessDisableOperation) *DisableAccessResult {
	return &DisableAccessResult{CustomerID: operation.CustomerID, Status: "DISABLED", OperationNo: operation.OperationNo, CompletedAt: operation.CompletedAt}
}

func (s *AccessDisableService) Current(ctx context.Context, customerID uint64) (*AccessStatusResult, error) {
	principal, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	if customerID == 0 || (!principal.HasPermission("portal_account.provision") && !principal.HasPermission("portal_account.disable")) {
		return nil, apperror.ErrForbidden
	}
	accessible, err := s.customers.CanAccessCustomer(ctx, principal, customerID)
	if err != nil {
		return nil, err
	}
	if !accessible {
		return nil, ErrNotFound
	}
	result := &AccessStatusResult{CustomerID: customerID, AccessStatus: "NOT_PROVISIONED"}
	link, linkErr := s.repo.FindIdentityLink(ctx, principal.TenantID, customerID)
	if linkErr == nil {
		result.AccessStatus = link.Status
	} else if !errors.Is(linkErr, ErrNotFound) {
		return nil, linkErr
	}
	operation, operationErr := s.repo.FindLatestAccessDisableOperation(ctx, principal.TenantID, customerID)
	if operationErr == nil {
		// 后续显式创建的映射属于独立授权，历史停用元数据不能覆盖它的当前访问状态。
		if linkErr == nil && operation.IdentityLinkID != link.ID {
			return result, nil
		}
		result.OperationNo, result.OperationStatus, result.OperationStage = operation.OperationNo, operation.Status, operation.Stage
		result.LastErrorCode, result.LastErrorSummary = operation.LastErrorCode, operation.LastErrorSummary
		result.NextRetryAt, result.CompletedAt = operation.NextRetryAt, operation.CompletedAt
		if operation.Stage == DisableStageMappingDisabled || operation.Stage == DisableStageCompleted {
			result.AccessStatus = "DISABLED"
		}
	} else if !errors.Is(operationErr, gorm.ErrRecordNotFound) {
		return nil, operationErr
	}
	return result, nil
}

func NewService(repo Repository, customers CustomerReader, platform PlatformProvisioner, portal PortalProvisioner, auditWriter audit.Writer, pepper []byte, publicURL string, clock Clock, random RandomSource, protector OperationProtector, options ...ServiceOption) *Service {
	service := &Service{repo: repo, customers: customers, platform: platform, portal: portal, audit: auditWriter, pepper: append([]byte(nil), pepper...), publicURL: strings.TrimRight(publicURL, "/"), clock: clock, random: random, protector: protector}
	for _, apply := range options {
		apply(service)
	}
	return service
}

type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now().UTC() }

type CryptoRandom struct{}

func (CryptoRandom) Bytes(size int) ([]byte, error) {
	value := make([]byte, size)
	_, err := rand.Read(value)
	return value, err
}

func (s *Service) Create(ctx context.Context, customerID uint64, request CreateRequest) (*CreateResult, error) {
	principal, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	if request.IdempotencyKey == "" {
		return nil, ErrIdempotencyRequired
	}
	if len(request.IdempotencyKey) > 128 || strings.IndexFunc(request.IdempotencyKey, func(char rune) bool { return char < 0x20 || char == 0x7f }) >= 0 {
		return nil, ErrIdempotencyInvalid
	}
	if s.protector == nil {
		return nil, dependency(errors.New("portal provision operation protector is not configured"))
	}
	requestHash := provisionRequestHash(principal, customerID)
	// 幂等坐标先于外部调用持久化；如果同键操作已存在，续跑其阶段而不是重新创建平台用户或门户映射。
	operation, err := s.repo.FindProvisionOperation(ctx, principal.TenantID, principal.UserID, request.IdempotencyKey)
	if err == nil {
		if operation.RequestHash != requestHash || operation.CustomerID != customerID {
			return nil, ErrIdempotencyConflict
		}
		return s.resumeProvision(ctx, principal, operation)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	contact, err := s.customers.RegistrationContact(ctx, principal, customerID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(contact.Phone) == "" && strings.TrimSpace(contact.Email) == "" {
		return nil, ErrContactInvalid
	}
	snapshot, err := json.Marshal(contact)
	if err != nil {
		return nil, err
	}
	snapshotCipher, err := s.protector.Encrypt(ctx, snapshot)
	if err != nil {
		return nil, err
	}
	numberBytes, err := s.random.Bytes(12)
	if err != nil {
		return nil, err
	}
	now := s.clock.Now().UTC()
	operation = &ProvisionOperation{
		Model:       database.Model{TenantID: principal.TenantID, CreatedBy: principal.UserID, UpdatedBy: principal.UserID, CreatedAt: now, UpdatedAt: now, Version: 1},
		OperationNo: "PO" + strings.ToUpper(hex.EncodeToString(numberBytes)), ActorID: principal.UserID,
		IdempotencyKey: request.IdempotencyKey, RequestHash: requestHash, CustomerID: customerID,
		ContactID: contact.ContactID, ContactSnapshotCipher: snapshotCipher, Stage: OperationStagePrepared, Status: OperationStatusProcessing,
	}
	err = s.repo.WithTransaction(ctx, func(txCtx context.Context) error {
		if lockErr := s.repo.LockCustomer(txCtx, principal.TenantID, customerID); lockErr != nil {
			return lockErr
		}
		return s.repo.CreateProvisionOperation(txCtx, operation)
	})
	if err != nil {
		winner, findErr := s.repo.FindProvisionOperation(ctx, principal.TenantID, principal.UserID, request.IdempotencyKey)
		if findErr != nil || winner.RequestHash != requestHash {
			return nil, err
		}
		operation = winner
	}
	return s.resumeProvision(ctx, principal, operation)
}

func (s *Service) Current(ctx context.Context, customerID uint64) (*CurrentResult, error) {
	principal, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	contact, err := s.customers.RegistrationContact(ctx, principal, customerID)
	if err != nil {
		return nil, err
	}
	invite, err := s.repo.FindCurrent(ctx, principal.TenantID, customerID)
	if err != nil {
		return nil, err
	}
	if invite.Status == StatusPending && !invite.ExpiresAt.After(s.clock.Now().UTC()) {
		if err = s.repo.MarkExpired(ctx, invite.ID, invite.Version, principal.UserID, s.clock.Now().UTC()); err != nil {
			return nil, err
		}
		invite.Status, invite.Version = StatusExpired, invite.Version+1
	}
	return publicCurrent(invite, contactSummary(contact)), nil
}

func (s *Service) Revoke(ctx context.Context, inviteNo string, cmd RevokeRequest) (*CurrentResult, error) {
	principal, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	reason := strings.TrimSpace(cmd.Reason)
	if reason == "" || cmd.Version == 0 {
		return nil, ErrInvalidArgument
	}
	invite, err := s.repo.FindByInviteNo(ctx, principal.TenantID, strings.TrimSpace(inviteNo))
	if err != nil {
		return nil, err
	}
	contact, err := s.customers.RegistrationContact(ctx, principal, invite.CustomerID)
	if err != nil {
		return nil, err
	}
	if invite.Version != cmd.Version {
		return nil, ErrVersionConflict
	}
	now := s.clock.Now().UTC()
	err = s.repo.WithTransaction(ctx, func(txCtx context.Context) error {
		if updateErr := s.repo.Revoke(txCtx, invite.ID, invite.Version, principal.UserID, reason, now); updateErr != nil {
			return updateErr
		}
		return s.writeAudit(txCtx, principal, "REVOKE", invite, reason, map[string]any{"status": StatusRevoked})
	})
	if err != nil {
		return nil, err
	}
	invite.Status, invite.RevokedAt, invite.RevokedReason, invite.Version = StatusRevoked, &now, reason, invite.Version+1
	return publicCurrent(invite, contactSummary(contact)), nil
}

func (s *Service) Verify(ctx context.Context, token string) (*VerifiedResult, error) {
	if strings.TrimSpace(token) == "" {
		return nil, ErrInvalidArgument
	}
	invite, err := s.repo.FindByTokenHash(ctx, s.tokenHash(token))
	if err != nil {
		return nil, err
	}
	now := s.clock.Now().UTC()
	if invite.Status == StatusPending && !invite.ExpiresAt.After(now) {
		if expireErr := s.repo.MarkExpired(ctx, invite.ID, invite.Version, "portal-machine", now); expireErr != nil && !errors.Is(expireErr, ErrVersionConflict) {
			return nil, expireErr
		}
		return nil, ErrExpired
	}
	if err = statusError(invite.Status); err != nil {
		return nil, err
	}
	if err = s.audit.Write(ctx, audit.Event{TenantID: invite.TenantID, Module: "portal_invite", Operation: "VERIFY", ResourceType: "portal_invite", ResourceID: invite.InviteNo, ActorID: "portal-machine", AfterJSON: audit.JSON(map[string]any{"status": invite.Status}), Result: "SUCCESS"}); err != nil {
		return nil, err
	}
	contactID := invite.ContactID
	return &VerifiedResult{TenantID: invite.TenantID, CustomerID: invite.CustomerID, ContactID: &contactID, PlatformUserID: invite.PlatformUserID, PortalAccountID: invite.PortalAccountID, ExpireAt: invite.ExpiresAt}, nil
}

func (s *Service) Consume(ctx context.Context, cmd ConsumeRequest) error {
	if strings.TrimSpace(cmd.Token) == "" || strings.TrimSpace(cmd.PlatformUserID) == "" || strings.TrimSpace(cmd.RequestID) == "" {
		return ErrInvalidArgument
	}
	invite, err := s.repo.FindByTokenHash(ctx, s.tokenHash(cmd.Token))
	if err != nil {
		return err
	}
	now := s.clock.Now().UTC()
	if invite.Status == StatusPending && !invite.ExpiresAt.After(now) {
		if expireErr := s.repo.MarkExpired(ctx, invite.ID, invite.Version, "portal-machine", now); expireErr != nil && !errors.Is(expireErr, ErrVersionConflict) {
			return expireErr
		}
		return ErrExpired
	}
	if invite.Status != StatusUsed {
		if err = statusError(invite.Status); err != nil {
			return err
		}
	}
	if invite.PlatformUserID != cmd.PlatformUserID {
		_ = s.audit.Write(requestctx.WithID(ctx, cmd.RequestID), audit.Event{TenantID: invite.TenantID, Module: "portal_invite", Operation: "CONSUME", ResourceType: "portal_invite", ResourceID: invite.InviteNo, ActorID: "portal-machine", Reason: "OIDC subject mismatch", Result: "REJECTED"})
		return ErrSubjectMismatch
	}
	err = s.repo.WithTransaction(ctx, func(txCtx context.Context) error {
		// 激活与停用访问通过客户锁串行：激活先持锁时，停用随后观察 ACTIVE 并撤销；
		// 停用先持锁时，阻断栅栏防止旧邀请把访问复活。
		if lockErr := s.repo.LockCustomer(txCtx, invite.TenantID, invite.CustomerID); lockErr != nil {
			return lockErr
		}
		if fences, ok := s.repo.(accessDisableFenceRepository); ok {
			blocked, fenceErr := fences.HasBlockingAccessDisable(txCtx, invite.TenantID, invite.CustomerID)
			if fenceErr != nil {
				return fenceErr
			}
			if blocked {
				return ErrVersionConflict
			}
		}
		locked, lockErr := s.repo.FindByTokenHashForUpdate(txCtx, s.tokenHash(cmd.Token))
		if lockErr != nil {
			return lockErr
		}
		if locked.PlatformUserID != cmd.PlatformUserID {
			return ErrSubjectMismatch
		}
		link, linkErr := s.repo.FindIdentityLinkForInviteForUpdate(txCtx, locked)
		if linkErr != nil {
			return linkErr
		}
		// 首次提交后响应可能丢失；只有令牌、主体和映射完全匹配，且两处 CRM 权威状态均已收敛，
		// 才把重复消费视为成功。
		if locked.Status == StatusUsed {
			if link.Status == "ACTIVE" {
				return nil
			}
			return ErrVersionConflict
		}
		if stateErr := statusError(locked.Status); stateErr != nil {
			return stateErr
		}
		if !locked.ExpiresAt.After(now) {
			return ErrExpired
		}
		if link.Status != StatusPending {
			return ErrVersionConflict
		}
		if updateErr := s.repo.ActivateIdentityLink(txCtx, locked, link, "portal-machine", now); updateErr != nil {
			return updateErr
		}
		if updateErr := s.repo.Consume(txCtx, locked.ID, locked.Version, cmd.PlatformUserID, "portal-machine", now); updateErr != nil {
			return updateErr
		}
		return s.audit.Write(requestctx.WithID(txCtx, cmd.RequestID), audit.Event{TenantID: locked.TenantID, Module: "portal_invite", Operation: "CONSUME", ResourceType: "portal_invite", ResourceID: locked.InviteNo, ActorID: "portal-machine", AfterJSON: audit.JSON(map[string]any{"invite_status": StatusUsed, "identity_link_status": "ACTIVE"}), Result: "SUCCESS"})
	})
	return err
}

func (s *Service) tokenHash(token string) string {
	sum := sha256.Sum256(append([]byte(token), s.pepper...))
	return hex.EncodeToString(sum[:])
}

func provisionRequestHash(principal auth.Principal, customerID uint64) string {
	sum := sha256.Sum256([]byte(principal.TenantID + "\x00" + principal.UserID + "\x00" + fmt.Sprint(customerID)))
	return hex.EncodeToString(sum[:])
}

func operationRemoteKey(operation *ProvisionOperation, step string) string {
	suffix := "M"
	if step == "portal-role" {
		suffix = "R"
	}
	if step == "binding" {
		suffix = "B"
	}
	return operation.OperationNo + suffix
}

func (s *Service) resumeProvision(ctx context.Context, principal auth.Principal, operation *ProvisionOperation) (*CreateResult, error) {
	contact, err := s.operationContact(ctx, principal, operation)
	if err != nil {
		return nil, err
	}
	now := s.clock.Now().UTC()
	if operation.Stage == OperationStageCompleted {
		return s.completedProvisionResult(ctx, operation, contact)
	}
	if operation.Stage == OperationStagePrepared {
		// Saga 每完成一个远程步骤就持久化新阶段；崩溃后从已确认阶段继续，
		// 远端调用还必须使用稳定幂等键覆盖“远端成功、阶段落库失败”的窗口。
		identity, provisionErr := s.platform.ProvisionExternalUser(ctx, contact)
		if provisionErr != nil || identity.PlatformUserID == "" || identity.AccountNo == "" {
			return nil, s.failProvision(ctx, operation, "PLATFORM_USER_PROVISION_FAILED", errors.Join(provisionErr, errors.New("platform provision response is incomplete")))
		}
		if err = s.repo.AdvanceProvisionOperation(ctx, operation, OperationStagePrepared, map[string]any{
			"stage": OperationStageUserProvisioned, "status": OperationStatusProcessing,
			"platform_user_id": identity.PlatformUserID, "account_no": identity.AccountNo,
			"last_error_code": "", "last_error_summary": "",
		}, now); err != nil {
			return nil, dependency(err)
		}
	}
	identity := ProvisionedIdentity{PlatformUserID: operation.PlatformUserID, AccountNo: operation.AccountNo}
	if operation.Stage != OperationStagePrepared && (strings.TrimSpace(identity.PlatformUserID) == "" || strings.TrimSpace(identity.AccountNo) == "") {
		return nil, dependency(errors.New("portal provision operation identity is incomplete"))
	}
	if operation.Stage == OperationStageUserProvisioned {
		if err = s.platform.AssignPortalRoleIdempotent(ctx, identity.PlatformUserID, operationRemoteKey(operation, "portal-role")); err != nil {
			compensationErr := s.recordCompensationForOperation(ctx, principal, contact, identity, CompensationRole, "ROLE_ASSIGN_FAILED", operationRemoteKey(operation, "portal-role"))
			return nil, s.failProvision(ctx, operation, "ROLE_ASSIGN_FAILED", errors.Join(err, compensationErr))
		}
		if err = s.repo.AdvanceProvisionOperation(ctx, operation, OperationStageUserProvisioned, map[string]any{
			"stage": OperationStageRoleAssigned, "status": OperationStatusProcessing,
			"last_error_code": "", "last_error_summary": "",
		}, now); err != nil {
			return nil, dependency(err)
		}
	}
	if operation.Stage == OperationStageRoleAssigned {
		// Phase 2 双写：门户映射仍是权威，平台客户绑定失败不中断邀请开通；失败入队绑定
		// 补偿任务，由补偿 worker 按同一幂等键补齐，对账 worker 观察残余差异。
		if s.binding != nil {
			customerRef := strconv.FormatUint(contact.CustomerID, 10)
			bindingResult := "SUCCESS"
			if bindErr := s.binding.BindCustomerIdempotent(ctx, identity.PlatformUserID, customerRef, operationRemoteKey(operation, "binding")); bindErr != nil {
				bindingResult = "PENDING"
				compensationErr := s.recordCompensationForOperation(ctx, principal, contact, identity, CompensationBinding, "PLATFORM_BINDING_FAILED", operationRemoteKey(operation, "binding"))
				if s.platformOnly {
					// Phase 5 单写：平台绑定是唯一权威，失败即进入重试等待，不产生半开通状态。
					return nil, s.failProvision(ctx, operation, "PLATFORM_BINDING_FAILED", errors.Join(bindErr, compensationErr))
				}
			}
			// 审计采用 best-effort：绑定双写成败不影响开通主线，安全事件不可用也不阻断。
			_ = s.audit.Write(ctx, audit.Event{TenantID: operation.TenantID, Module: "portal_invite", Operation: "PLATFORM_BINDING", ResourceType: "customer", ResourceID: fmt.Sprint(operation.CustomerID), ActorID: "portal-machine", AfterJSON: audit.JSON(map[string]any{"platform_user_id": identity.PlatformUserID, "customer_ref": customerRef}), Result: bindingResult})
		}
		var mapping PortalMapping
		var mappingErr error
		if s.platformOnly {
			// 门户映射表退役：不再调用门户 provision，本地合成门户账号标识以完成状态机；
			// 门户侧以平台 customer_ref 作为客户边界。
			mapping = PortalMapping{PortalAccountID: "PA-" + strconv.FormatUint(contact.CustomerID, 10)}
		} else {
			mapping, mappingErr = s.portal.ProvisionMappingIdempotent(ctx, contact, identity, operationRemoteKey(operation, "portal-mapping"))
			if mappingErr != nil || strings.TrimSpace(mapping.PortalAccountID) == "" {
				compensationErr := s.recordCompensationForOperation(ctx, principal, contact, identity, CompensationMapping, "PORTAL_MAPPING_FAILED", operationRemoteKey(operation, "portal-mapping"))
				return nil, s.failProvision(ctx, operation, "PORTAL_MAPPING_FAILED", errors.Join(mappingErr, compensationErr))
			}
		}
		if err = s.repo.AdvanceProvisionOperation(ctx, operation, OperationStageRoleAssigned, map[string]any{
			"stage": OperationStageMappingReady, "status": OperationStatusProcessing,
			"portal_account_id": mapping.PortalAccountID, "last_error_code": "", "last_error_summary": "",
		}, now); err != nil {
			return nil, dependency(err)
		}
	}
	if operation.Stage != OperationStageMappingReady {
		return nil, dependency(errors.New("portal provision operation has an unsupported stage"))
	}
	if strings.TrimSpace(operation.PortalAccountID) == "" {
		return nil, dependency(errors.New("portal provision operation mapping is incomplete"))
	}
	return s.finalizeProvision(ctx, principal, operation, contact)
}

func (s *Service) operationContact(ctx context.Context, principal auth.Principal, operation *ProvisionOperation) (ContactIdentity, error) {
	if operation == nil || operation.TenantID != principal.TenantID || operation.ActorID != principal.UserID || operation.CustomerID == 0 || operation.ContactID == 0 {
		return ContactIdentity{}, ErrIdempotencyConflict
	}
	plaintext, err := s.protector.Decrypt(ctx, operation.ContactSnapshotCipher)
	if err != nil {
		return ContactIdentity{}, dependency(errors.New("portal provision recovery snapshot is invalid"))
	}
	var contact ContactIdentity
	decoder := json.NewDecoder(bytes.NewReader(plaintext))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&contact); err != nil || decoder.Decode(&struct{}{}) != io.EOF || contact.TenantID != operation.TenantID || contact.CustomerID != operation.CustomerID || contact.ContactID != operation.ContactID {
		return ContactIdentity{}, dependency(errors.New("portal provision recovery snapshot is invalid"))
	}
	return contact, nil
}

func (s *Service) finalizeProvision(ctx context.Context, principal auth.Principal, operation *ProvisionOperation, contact ContactIdentity) (*CreateResult, error) {
	rawToken, err := s.random.Bytes(32)
	if err != nil {
		return nil, err
	}
	numberBytes, err := s.random.Bytes(12)
	if err != nil {
		return nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(rawToken)
	tokenCipher, err := s.protector.Encrypt(ctx, []byte(token))
	if err != nil {
		return nil, err
	}
	now := s.clock.Now().UTC()
	invite := &Invite{
		Model:    database.Model{TenantID: principal.TenantID, CreatedBy: principal.UserID, UpdatedBy: principal.UserID, CreatedAt: now, UpdatedAt: now, Version: 1},
		InviteNo: "PI" + strings.ToUpper(hex.EncodeToString(numberBytes)), CustomerID: operation.CustomerID, ContactID: operation.ContactID,
		PlatformUserID: operation.PlatformUserID, AccountNo: operation.AccountNo, PortalAccountID: operation.PortalAccountID,
		TokenHash: s.tokenHash(token), Status: StatusPending, ExpiresAt: now.Add(inviteTTL),
	}
	link := &IdentityLink{
		Model:      database.Model{TenantID: principal.TenantID, CreatedBy: principal.UserID, UpdatedBy: principal.UserID, CreatedAt: now, UpdatedAt: now, Version: 1},
		CustomerID: operation.CustomerID, ContactID: operation.ContactID, PlatformUserID: operation.PlatformUserID,
		PortalAccountID: operation.PortalAccountID, Status: StatusPending, ProvisionedAt: now,
	}
	err = s.repo.WithTransaction(ctx, func(txCtx context.Context) error {
		locked, lockErr := s.repo.FindProvisionOperationForUpdate(txCtx, principal.TenantID, operation.ID)
		if lockErr != nil {
			return lockErr
		}
		if locked.Stage == OperationStageCompleted {
			*operation = *locked
			return nil
		}
		if locked.Stage != OperationStageMappingReady || locked.Version != operation.Version {
			return ErrVersionConflict
		}
		if lockErr = s.repo.LockCustomer(txCtx, principal.TenantID, operation.CustomerID); lockErr != nil {
			return lockErr
		}
		if fences, ok := s.repo.(accessDisableFenceRepository); ok {
			blocked, fenceErr := fences.HasBlockingAccessDisable(txCtx, principal.TenantID, operation.CustomerID)
			if fenceErr != nil {
				return fenceErr
			}
			if blocked {
				return ErrVersionConflict
			}
		}
		if revokeErr := s.repo.RevokePending(txCtx, principal.TenantID, operation.CustomerID, principal.UserID, "superseded by a new invite", now); revokeErr != nil {
			return revokeErr
		}
		if linkErr := s.repo.UpsertLink(txCtx, link); linkErr != nil {
			return linkErr
		}
		if createErr := s.repo.CreateInvite(txCtx, invite); createErr != nil {
			return createErr
		}
		completedAt := now
		if advanceErr := s.repo.AdvanceProvisionOperation(txCtx, locked, OperationStageMappingReady, map[string]any{
			"stage": OperationStageCompleted, "status": OperationStatusCompleted, "invite_id": invite.ID,
			"token_cipher": tokenCipher, "completed_at": completedAt, "last_error_code": "", "last_error_summary": "",
		}, now); advanceErr != nil {
			return advanceErr
		}
		*operation = *locked
		operation.InviteID, operation.TokenCipher, operation.CompletedAt = &invite.ID, tokenCipher, &completedAt
		return s.writeAudit(txCtx, principal, "GENERATE", invite, "", map[string]any{"status": StatusPending, "expires_at": invite.ExpiresAt})
	})
	if err != nil {
		return nil, err
	}
	if operation.InviteID == nil || *operation.InviteID != invite.ID {
		return s.completedProvisionResult(ctx, operation, contact)
	}
	return s.createResult(invite, token, contact), nil
}

func (s *Service) completedProvisionResult(ctx context.Context, operation *ProvisionOperation, contact ContactIdentity) (*CreateResult, error) {
	if operation.InviteID == nil || len(operation.TokenCipher) == 0 {
		return nil, dependency(errors.New("completed portal provision operation is incomplete"))
	}
	invite, err := s.repo.FindInviteByID(ctx, operation.TenantID, *operation.InviteID)
	if err != nil {
		return nil, err
	}
	plaintext, err := s.protector.Decrypt(ctx, operation.TokenCipher)
	if err != nil || s.tokenHash(string(plaintext)) != invite.TokenHash {
		return nil, dependency(errors.New("portal provision replay token is invalid"))
	}
	return s.createResult(invite, string(plaintext), contact), nil
}

func (s *Service) createResult(invite *Invite, token string, contact ContactIdentity) *CreateResult {
	return &CreateResult{InviteNo: invite.InviteNo, ActivationURL: s.activationURL(token), Status: invite.Status, ExpiresAt: invite.ExpiresAt, ContactSummary: contactSummary(contact), IdentitySummary: identitySummary(invite.PlatformUserID), LoginAccount: invite.AccountNo}
}

func (s *Service) failProvision(ctx context.Context, operation *ProvisionOperation, code string, cause error) error {
	if cause == nil {
		cause = errors.New("portal provisioning dependency failed")
	}
	now := s.clock.Now().UTC()
	summary := "portal provisioning dependency is unavailable"
	advanceErr := s.repo.WithTransaction(ctx, func(txCtx context.Context) error {
		locked, lockErr := s.repo.FindProvisionOperationForUpdate(txCtx, operation.TenantID, operation.ID)
		if lockErr != nil {
			return lockErr
		}
		if locked.Stage != operation.Stage || locked.Status == OperationStatusCompleted {
			return ErrVersionConflict
		}
		return s.repo.AdvanceProvisionOperation(txCtx, locked, locked.Stage, map[string]any{
			"stage": locked.Stage, "status": OperationStatusRetryWait, "attempts": gorm.Expr("attempts+1"),
			"last_error_code": code, "last_error_summary": summary,
		}, now)
	})
	return dependency(errors.Join(cause, advanceErr))
}

func (s *Service) activationURL(token string) string {
	parsed, _ := url.Parse(s.publicURL + "/activate")
	query := parsed.Query()
	query.Set("token", token)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func (s *Service) recordCompensationForOperation(ctx context.Context, principal auth.Principal, contact ContactIdentity, identity ProvisionedIdentity, taskType, code, taskNo string) error {
	now := s.clock.Now().UTC()
	return s.repo.CreateCompensation(ctx, &CompensationTask{
		Model:  database.Model{TenantID: principal.TenantID, CreatedBy: principal.UserID, UpdatedBy: principal.UserID, CreatedAt: now, UpdatedAt: now, Version: 1},
		TaskNo: taskNo, TaskType: taskType,
		CustomerID: contact.CustomerID, ContactID: contact.ContactID,
		PlatformUserID: identity.PlatformUserID, AccountNo: identity.AccountNo,
		Status: CompensationPending, LastErrorCode: code,
	})
}

func (s *Service) writeAudit(ctx context.Context, principal auth.Principal, operation string, invite *Invite, reason string, after any) error {
	return s.audit.Write(ctx, audit.Event{TenantID: principal.TenantID, Module: "portal_invite", Operation: operation, ResourceType: "portal_invite", ResourceID: invite.InviteNo, ActorID: principal.UserID, ActorNameSnapshot: principal.DisplayName, AfterJSON: audit.JSON(after), Reason: reason, Result: "SUCCESS"})
}

func principal(ctx context.Context) (auth.Principal, error) {
	value, ok := auth.FromContext(ctx)
	if !ok || value.TenantID == "" || value.UserID == "" {
		return auth.Principal{}, apperror.ErrUnauthenticated
	}
	return value, nil
}

func dependency(err error) error {
	return apperror.Wrap(err, ErrDependencyUnavailable.HTTPStatus, ErrDependencyUnavailable.Code, ErrDependencyUnavailable.Message)
}

func statusError(status string) error {
	switch status {
	case StatusPending:
		return nil
	case StatusUsed:
		return ErrUsed
	case StatusExpired:
		return ErrExpired
	case StatusRevoked:
		return ErrRevoked
	default:
		return ErrNotFound
	}
}

func contactSummary(contact ContactIdentity) string {
	values := make([]string, 0, 2)
	if contact.PhoneMasked != "" {
		values = append(values, contact.PhoneMasked)
	}
	if contact.EmailMasked != "" {
		values = append(values, contact.EmailMasked)
	}
	return strings.Join(values, " / ")
}

func identitySummary(subject string) string {
	value := []rune(strings.TrimSpace(subject))
	if len(value) <= 8 {
		return "********"
	}
	return string(value[:4]) + "…" + string(value[len(value)-4:])
}

func publicCurrent(invite *Invite, summary string) *CurrentResult {
	return &CurrentResult{InviteNo: invite.InviteNo, Status: invite.Status, ExpiresAt: invite.ExpiresAt, UsedAt: invite.UsedAt, RevokedAt: invite.RevokedAt, ContactSummary: summary, IdentitySummary: identitySummary(invite.PlatformUserID), LoginAccount: invite.AccountNo, Version: invite.Version}
}
