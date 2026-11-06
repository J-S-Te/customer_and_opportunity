package localidentity

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/account"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/project"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type LocalProject struct {
	Record
	ProjectID   string `gorm:"size:64;not null;uniqueIndex" json:"project_id"`
	CustomerID  uint64 `gorm:"not null;index" json:"customer_id"`
	Name        string `gorm:"size:200;not null" json:"name"`
	Status      string `gorm:"size:32;not null" json:"status"`
	ProgressPct uint8  `gorm:"not null" json:"progress_pct"`
	Stage       string `gorm:"size:64;not null" json:"stage"`
	Namespace   string `gorm:"size:32;not null" json:"namespace"`
}

// TableName 指定门户自身真实项目来源表，不冒充PMS投影回执。
// 参数：无。
// 返回值：固定表名。
func (LocalProject) TableName() string { return "portal_local_projects" }

type ProjectInput struct {
	CustomerID      uint64 `json:"customer_id"`
	Namespace       string `json:"namespace"`
	ProjectID       string `json:"project_id"`
	Name            string `json:"name"`
	Status          string `json:"status"`
	ProgressPct     uint8  `json:"progress_pct"`
	Stage           string `json:"stage"`
	ExpectedVersion uint64 `json:"expected_version"`
}
type ProjectAccess struct {
	CustomerID      uint64 `json:"customer_id"`
	Namespace       string `json:"namespace"`
	ProjectID       string `json:"project_id"`
	AccountID       string `json:"account_id"`
	Enabled         bool   `json:"enabled"`
	ExpectedVersion uint64 `json:"expected_version"`
}

var localProjectCode = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

// validateProject 限定本地业务来源及实际项目登记状态。
// 参数：input 为管理员登记材料。
// 返回值：命名空间、编号或状态校验错误。
func validateProject(input ProjectInput) error {
	if GuardCustomerInput(ModeLocal, input.Namespace, input.CustomerID) != nil || !localProjectCode.MatchString(input.ProjectID) || !validText(input.Name, 200, true) || !validText(input.Stage, 64, true) || input.ProgressPct > 100 {
		return ErrInvalid
	}
	switch input.Status {
	case "PENDING", "IN_PROGRESS", "COMPLETED", "CANCELLED":
		return nil
	default:
		return ErrInvalid
	}
}

// SaveProject 登记或乐观更新门户自己的项目并原子生成既有只读模型。
// 参数：ctx 为精确项目管理员，input 为显式LOCAL来源及版本。
// 返回值：本地真实项目；不存在、并发或事务失败返回错误。
func (s *Service) SaveProject(ctx context.Context, input ProjectInput) (LocalProject, error) {
	p, err := s.principal(ctx, PermissionProjectManage, true)
	if err != nil {
		return LocalProject{}, err
	}
	input.Name, input.Stage = strings.TrimSpace(input.Name), strings.TrimSpace(input.Stage)
	if err = validateProject(input); err != nil {
		return LocalProject{}, err
	}
	var value LocalProject
	err = s.Repo.WithTransaction(ctx, func(tx context.Context) error {
		if _, err := s.Repo.lockCustomer(tx, p.TenantID, input.CustomerID); err != nil {
			return err
		}
		if _, err := s.Repo.activeCustomer(tx, p.TenantID, input.CustomerID); err != nil {
			return err
		}
		lookup := s.Repo.tx(tx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND project_id=?", p.TenantID, input.ProjectID).Take(&value).Error
		if errors.Is(lookup, gorm.ErrRecordNotFound) {
			if input.ExpectedVersion != 0 {
				return ErrConflict
			}
			var collisions int64
			if err := s.Repo.tx(tx).Model(&project.Snapshot{}).Where("tenant_id=? AND project_id=?", p.TenantID, input.ProjectID).Count(&collisions).Error; err != nil {
				return err
			}
			if collisions != 0 {
				return ErrNamespace
			}
			value = LocalProject{Record: s.record(p), ProjectID: input.ProjectID, CustomerID: input.CustomerID, Name: input.Name, Status: input.Status, ProgressPct: input.ProgressPct, Stage: input.Stage, Namespace: LocalNamespace}
			if err := s.Repo.tx(tx).Create(&value).Error; err != nil {
				return err
			}
		} else {
			if lookup != nil {
				return lookup
			}
			if value.CustomerID != input.CustomerID || value.Namespace != LocalNamespace {
				return ErrNamespace
			}
			if value.Version != input.ExpectedVersion {
				return ErrConflict
			}
			fields := map[string]any{"name": input.Name, "status": input.Status, "progress_pct": input.ProgressPct, "stage": input.Stage, "version": gorm.Expr("version+1"), "updated_at": s.now(), "updated_by": p.UserID}
			if err := s.Repo.tx(tx).Model(&value).Updates(fields).Error; err != nil {
				return err
			}
			value.Name, value.Status, value.ProgressPct, value.Stage = input.Name, input.Status, input.ProgressPct, input.Stage
			value.Version++
		}
		snapshot := project.Snapshot{Model: database.Model{TenantID: p.TenantID, CreatedBy: "portal-local-admin", UpdatedBy: "portal-local-admin", CreatedAt: value.CreatedAt, UpdatedAt: s.now(), Version: value.Version}, ProjectID: value.ProjectID, CustomerID: value.CustomerID, ProjectName: value.Name, Status: value.Status, ProgressPct: value.ProgressPct, CurrentStage: value.Stage, SourceUpdatedAt: s.now(), SyncedAt: s.now(), RawVersion: fmt.Sprintf("portal-local:%d", value.Version)}
		if err := s.Repo.tx(tx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "tenant_id"}, {Name: "project_id"}}, DoUpdates: clause.AssignmentColumns([]string{"project_name", "status", "progress_pct", "current_stage", "source_updated_at", "synced_at", "raw_version", "version", "updated_at", "updated_by"})}).Create(&snapshot).Error; err != nil {
			return err
		}
		return s.audit(tx, p, "PORTAL_LOCAL_PROJECT_SAVED", "portal_local_project", value.ProjectID)
	})
	return value, err
}

// ListProjects 返回精确客户的真实本地项目目录。
// 参数：ctx 为项目管理员，customer 为本地客户，after 为键集游标。
// 返回值：最多一百项及存储或授权错误。
func (s *Service) ListProjects(ctx context.Context, customer, after uint64) ([]LocalProject, error) {
	p, err := s.principal(ctx, PermissionProjectManage, false)
	if err != nil {
		return nil, err
	}
	if _, err = s.Repo.activeCustomer(ctx, p.TenantID, customer); err != nil {
		return nil, err
	}
	rows := []LocalProject{}
	err = s.Repo.tx(ctx).Where("tenant_id=? AND customer_id=? AND id>?", p.TenantID, customer, after).Order("id").Limit(100).Find(&rows).Error
	return rows, err
}

// SetProjectAccess 仅为精确真实本地客户账号维护MANUAL项目授权。
// 参数：ctx 为项目管理员，input 为项目、平台主体及乐观版本。
// 返回值：授权记录；永不回退为客户下全部项目可见。
func (s *Service) SetProjectAccess(ctx context.Context, input ProjectAccess) (project.AccountBinding, error) {
	p, err := s.principal(ctx, PermissionProjectManage, true)
	if err != nil {
		return project.AccountBinding{}, err
	}
	if GuardCustomerInput(ModeLocal, input.Namespace, input.CustomerID) != nil || !localProjectCode.MatchString(input.ProjectID) || !validText(input.AccountID, 128, true) {
		return project.AccountBinding{}, ErrInvalid
	}
	var binding project.AccountBinding
	err = s.Repo.WithTransaction(ctx, func(tx context.Context) error {
		var local LocalProject
		if err := s.Repo.tx(tx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND project_id=? AND customer_id=? AND namespace=?", p.TenantID, input.ProjectID, input.CustomerID, LocalNamespace).Take(&local).Error; err != nil {
			return ErrNotFound
		}
		var link account.IdentityLink
		if err := s.Repo.tx(tx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND customer_id=? AND platform_user_id=? AND status='ACTIVE'", p.TenantID, input.CustomerID, input.AccountID).Take(&link).Error; err != nil {
			return ErrNotFound
		}
		adapter := &AccountAdapter{Repo: s.Repo, TenantID: s.TenantID, HMACKey: s.HMACKey, Now: s.now}
		if err := adapter.ValidateCustomer(tx, p.TenantID, input.CustomerID, input.AccountID, false); err != nil {
			return err
		}
		mode := project.AccountPermissionMode{Model: database.Model{TenantID: p.TenantID, CreatedBy: "portal-local-admin", UpdatedBy: "portal-local-admin", Version: 1}, CustomerID: input.CustomerID, AccountID: input.AccountID, Mode: project.PermissionModeIsolated}
		if err := s.Repo.tx(tx).Clauses(clause.OnConflict{DoUpdates: clause.Assignments(map[string]any{"mode": project.PermissionModeIsolated})}).Create(&mode).Error; err != nil {
			return err
		}
		status := "DISABLED"
		if input.Enabled {
			status = "ACTIVE"
		}
		q := s.Repo.tx(tx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND customer_id=? AND project_id=? AND account_id=? AND source=?", p.TenantID, input.CustomerID, input.ProjectID, input.AccountID, project.BindingSourceManual).Take(&binding).Error
		if errors.Is(q, gorm.ErrRecordNotFound) {
			if input.ExpectedVersion != 0 || !input.Enabled {
				return ErrConflict
			}
			binding = project.AccountBinding{Model: database.Model{TenantID: p.TenantID, CreatedBy: "portal-local-admin", UpdatedBy: "portal-local-admin", CreatedAt: s.now(), UpdatedAt: s.now(), Version: 1}, CustomerID: input.CustomerID, ProjectID: input.ProjectID, AccountID: input.AccountID, Source: project.BindingSourceManual, Status: status, SourceVersion: "portal-local-v1"}
			if err := s.Repo.tx(tx).Create(&binding).Error; err != nil {
				return err
			}
		} else {
			if q != nil {
				return q
			}
			if binding.Version != input.ExpectedVersion {
				return ErrConflict
			}
			if err := s.Repo.tx(tx).Model(&binding).Updates(map[string]any{"status": status, "version": gorm.Expr("version+1"), "updated_at": s.now(), "updated_by": "portal-local-admin"}).Error; err != nil {
				return err
			}
			binding.Status = status
			binding.Version++
		}
		return s.audit(tx, p, "PORTAL_LOCAL_PROJECT_ACCESS_CHANGED", "portal_local_project", input.ProjectID)
	})
	return binding, err
}
