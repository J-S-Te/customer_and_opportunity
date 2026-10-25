package presale

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	mysqlDriver "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"strconv"
	"strings"
	"time"
)

func sameCreateRequestReplay(old *PresaleRequest, actor Actor, opportunityID uint64, hash, legacyHash string) bool {
	if old == nil || old.OpportunityID != opportunityID || old.ApplicantID != actor.UserID || old.CreatedBy != actor.UserID {
		return false
	}
	return old.CreateRequestHash == hash || old.CreateRequestHash == legacyHash
}

func sameWorklogReplay(old *Worklog, actor Actor, requestID uint64, hash, legacyHash string) bool {
	if old == nil || old.RequestID != requestID || old.PersonID != actor.PersonID || old.CreatedBy != actor.UserID {
		return false
	}
	return old.RequestHash == hash || old.RequestHash == legacyHash
}

func requestDigest(value any) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func mutationDigest(actor Actor, requestID uint64, operation, action string, payload any) (string, error) {
	return requestDigest(struct {
		ActorID   string `json:"actor_id"`
		RequestID uint64 `json:"request_id"`
		Operation string `json:"operation"`
		Action    string `json:"action"`
		Payload   any    `json:"payload"`
	}{actor.UserID, requestID, operation, action, payload})
}

func newMutationReplay(actor Actor, requestID uint64, key, operation, action, hash string, responseVersion uint64, at time.Time) *MutationReplay {
	return &MutationReplay{
		TenantID: actor.TenantID, RequestID: requestID, Operation: operation, Action: action,
		ActorID: actor.UserID, IdempotencyKey: key, RequestHash: hash,
		ResponseVersion: responseVersion, RequestIDTrace: actor.RequestID, CreatedAt: at,
	}
}

func validateMutationReplay(value *MutationReplay, actor Actor, requestID uint64, operation, action, hash string) error {
	if value == nil || value.RequestID != requestID || value.ActorID != actor.UserID || value.Operation != operation || value.Action != action || value.RequestHash != hash {
		return ErrIdempotencyConflict
	}
	return nil
}

func approvalReplayAction(node uint8, action string) string {
	return fmt.Sprintf("NODE_%d_%s", node, action)
}

func validateApprovalReplay(value *MutationReplay, actor Actor, requestID uint64, action, hash string, instance *ApprovalInstance) error {
	if value == nil || value.RequestID != requestID || value.ActorID != actor.UserID || value.Operation != "APPROVAL_ACTION" || value.RequestHash != hash {
		return ErrIdempotencyConflict
	}
	node, ok := approvalReplayNode(value.Action, action)
	if !ok || node < 1 || node > 10 {
		return ErrIdempotencyConflict
	}
	return approvalReplayRoleAllowedForNode(actor, node, instance)
}

// approvalReplayRoleAllowed 复用 approvalNodeRoleAllowedForInstance 的动态节点角色判断：
// 有规则快照时按快照的 role_code 校验，历史无快照实例回退到旧的角色映射。

// approvalReplayRoleAllowed 复用 approvalNodeRoleAllowedForInstance 的动态节点角色判断：
// 有规则快照时按快照的 role_code 校验，历史无快照实例回退到旧的角色映射。
func approvalReplayNode(replayAction, action string) (uint8, bool) {
	prefix, suffix, ok := strings.Cut(replayAction, "_")
	if !ok || prefix != "NODE" {
		return 0, false
	}
	digits, rest, ok := strings.Cut(suffix, "_")
	if !ok || rest != action || digits == "" {
		return 0, false
	}
	value, err := strconv.Atoi(digits)
	if err != nil || value < 1 || value > 10 {
		return 0, false
	}
	return uint8(value), true
}

func approvalReplayRoleAllowedForNode(actor Actor, node uint8, instance *ApprovalInstance) error {
	if approvalNodeRoleAllowedForInstance(actor, node, instance) {
		return nil
	}
	return ErrForbidden
}

func isDuplicateMutationError(err error) bool {
	var mysqlErr *mysqlDriver.MySQLError
	return errors.Is(err, gorm.ErrDuplicatedKey) || (errors.As(err, &mysqlErr) && mysqlErr.Number == 1062)
}

func (s *Service) resolveApprovalMutationRace(ctx context.Context, actor Actor, requestID uint64, key, action, hash string) error {
	return s.repo.WithTransaction(ctx, func(tx context.Context) error {
		if _, err := s.repo.FindRequestForUpdate(tx, actor.TenantID, requestID); err != nil {
			return err
		}
		instance, err := s.repo.FindApprovalInstanceForUpdate(tx, actor.TenantID, requestID)
		if err != nil {
			return err
		}
		old, err := s.repo.FindMutationReplay(tx, actor.TenantID, requestID, actor.UserID, key)
		if errors.Is(err, ErrNotFound) {
			return ErrIdempotencyConflict
		}
		if err != nil {
			return err
		}
		return validateApprovalReplay(old, actor, requestID, action, hash, instance)
	})
}

// 这里仅处理首个事务失败后的存储层唯一键竞争：先锁定并授权请求中的父申请，再查询
// 租户级键，避免猜测键值泄露胜者资源；普通业务冲突在进入该恢复路径前已经返回。

// 这里仅处理首个事务失败后的存储层唯一键竞争：先锁定并授权请求中的父申请，再查询
// 租户级键，避免猜测键值泄露胜者资源；普通业务冲突在进入该恢复路径前已经返回。
func (s *Service) resolveMutationRace(
	ctx context.Context,
	actor Actor,
	requestID uint64,
	key, operation, action, hash string,
	original error,
	authorize func(*PresaleRequest) error,
	setResponse func(*PresaleRequest),
) error {
	var replayed bool
	replayErr := s.repo.WithTransaction(ctx, func(tx context.Context) error {
		requestValue, err := s.repo.FindRequestForUpdate(tx, actor.TenantID, requestID)
		if err != nil {
			return err
		}
		if authorize != nil {
			if err = authorize(requestValue); err != nil {
				return err
			}
		}
		old, err := s.repo.FindMutationReplay(tx, actor.TenantID, requestID, actor.UserID, key)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if err = validateMutationReplay(old, actor, requestID, operation, action, hash); err != nil {
			return err
		}
		replayed = true
		if setResponse != nil {
			setResponse(requestValue)
		}
		return nil
	})
	if replayErr != nil {
		return replayErr
	}
	if replayed {
		return nil
	}
	if isDuplicateMutationError(original) {
		return ErrIdempotencyConflict
	}
	return original
}
