package presale

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

type approvalRuleRecord struct {
	ID                             uint64 `gorm:"primaryKey;autoIncrement"`
	TenantID, CreatedBy, UpdatedBy string
	CreatedAt, UpdatedAt           time.Time
	DeletedAt                      gorm.DeletedAt `gorm:"index"`
	Version                        uint64
	RuleKey, Name                  string
	Priority                       int
	Enabled                        bool
	ExpressionJSON, NodesJSON      []byte `gorm:"type:json"`
}

func (approvalRuleRecord) TableName() string { return "crm_presale_approval_rules" }

// ApprovalRuleStore 允许宿主在不改动现有售前 Repository 接口的情况下逐步接入规则中心。
type ApprovalRuleStore struct{ db *gorm.DB }

func NewApprovalRuleStore(db *gorm.DB) *ApprovalRuleStore { return &ApprovalRuleStore{db: db} }

func (s *ApprovalRuleStore) List(ctx context.Context, tenant string, enabledOnly bool) ([]ApprovalRule, error) {
	query := s.db.WithContext(ctx).Where("tenant_id=?", tenant)
	if enabledOnly {
		query = query.Where("enabled=?", true)
	}
	var rows []approvalRuleRecord
	if err := query.Order("priority DESC, id").Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]ApprovalRule, 0, len(rows))
	for _, row := range rows {
		var rule ApprovalRule
		if err := json.Unmarshal(row.ExpressionJSON, &rule.Expression); err != nil {
			return nil, fmt.Errorf("decode approval rule expression: %w", err)
		}
		if err := json.Unmarshal(row.NodesJSON, &rule.Nodes); err != nil {
			return nil, fmt.Errorf("decode approval rule nodes: %w", err)
		}
		rule.ID, rule.TenantID, rule.Name, rule.Priority, rule.Enabled, rule.Version = row.RuleKey, row.TenantID, row.Name, row.Priority, row.Enabled, row.Version
		result = append(result, rule)
	}
	return result, nil
}

func (s *ApprovalRuleStore) Create(ctx context.Context, actor Actor, rule ApprovalRule) (ApprovalRule, error) {
	if !actor.Can("presale.approval_rule.manage") {
		return ApprovalRule{}, ErrForbidden
	}
	if err := validateApprovalRule(rule); err != nil {
		return ApprovalRule{}, err
	}
	expression, _ := json.Marshal(rule.Expression)
	nodes, _ := json.Marshal(rule.Nodes)
	rule.ID, rule.TenantID, rule.Version = newApprovalRuleID(), actor.TenantID, 1
	row := approvalRuleRecord{TenantID: actor.TenantID, CreatedBy: actor.UserID, UpdatedBy: actor.UserID, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), RuleKey: rule.ID, Name: strings.TrimSpace(rule.Name), Priority: rule.Priority, Enabled: rule.Enabled, Version: 1, ExpressionJSON: expression, NodesJSON: nodes}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		return ApprovalRule{}, err
	}
	return rule, nil
}

func newApprovalRuleID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("rule-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func (s *ApprovalRuleStore) Update(ctx context.Context, actor Actor, rule ApprovalRule) (ApprovalRule, error) {
	if !actor.Can("presale.approval_rule.manage") || rule.ID == "" || rule.Version == 0 {
		return ApprovalRule{}, ErrForbidden
	}
	if err := validateApprovalRule(rule); err != nil {
		return ApprovalRule{}, err
	}
	expression, _ := json.Marshal(rule.Expression)
	nodes, _ := json.Marshal(rule.Nodes)
	result := s.db.WithContext(ctx).Model(&approvalRuleRecord{}).Where("tenant_id=? AND rule_key=? AND version=?", actor.TenantID, rule.ID, rule.Version).Updates(map[string]any{"name": strings.TrimSpace(rule.Name), "priority": rule.Priority, "enabled": rule.Enabled, "expression_json": expression, "nodes_json": nodes, "version": rule.Version + 1, "updated_by": actor.UserID})
	if result.Error != nil {
		return ApprovalRule{}, result.Error
	}
	if result.RowsAffected != 1 {
		return ApprovalRule{}, ErrVersionConflict
	}
	rule.TenantID, rule.Version = actor.TenantID, rule.Version+1
	return rule, nil
}

func (s *ApprovalRuleStore) Delete(ctx context.Context, actor Actor, id string, version uint64) error {
	if !actor.Can("presale.approval_rule.manage") || strings.TrimSpace(id) == "" || version == 0 {
		return ErrForbidden
	}
	result := s.db.WithContext(ctx).Where("tenant_id=? AND rule_key=? AND version=?", actor.TenantID, id, version).Delete(&approvalRuleRecord{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrVersionConflict
	}
	return nil
}

func validateApprovalRule(rule ApprovalRule) error {
	if strings.TrimSpace(rule.Name) == "" || len([]rune(rule.Name)) > 128 || len(rule.Nodes) == 0 || len(rule.Nodes) > 10 {
		return ErrInvalidInput
	}
	if _, err := rule.Expression.Match(ApprovalFacts{}); err != nil {
		return ErrInvalidInput
	}
	// 首审必须是审批节点且配置了角色；角色码由规则自定义，不再固定为销售总监。
	first := rule.Nodes[0]
	if first.Type != ApprovalNodeApproval || strings.TrimSpace(first.RoleCode) == "" {
		return ErrInvalidInput
	}
	seen := map[string]bool{}
	assignmentTypes := map[ApprovalNodeType]bool{}
	roleActions := map[string]map[ApprovalNodeType]bool{}
	for _, node := range rule.Nodes {
		if strings.TrimSpace(node.ID) == "" || strings.TrimSpace(node.Name) == "" || seen[node.ID] ||
			(node.Type != ApprovalNodeApproval && !IsAssignmentNodeType(node.Type)) {
			return ErrInvalidInput
		}
		if IsAssignmentNodeType(node.Type) {
			roleCode := strings.TrimSpace(node.RoleCode)
			if roleCode == "" {
				return ErrInvalidInput
			}
			// 同一角色可以同时承担部门和团队负责人两个动作，但同一动作类型
			// 只能配置一次，避免出现两个互相竞争的部门或执行人节点。
			if assignmentTypes[node.Type] {
				return ErrInvalidInput
			}
			assignmentTypes[node.Type] = true
			if roleActions[roleCode] == nil {
				roleActions[roleCode] = map[ApprovalNodeType]bool{}
			}
			roleActions[roleCode][node.Type] = true
		}
		seen[node.ID] = true
	}
	// 历史一级指派与两级执行指派不能混用在同一条规则里。
	if assignmentTypes[ApprovalNodePerson] &&
		(assignmentTypes[ApprovalNodeTeamLead] || assignmentTypes[ApprovalNodeExecutor]) {
		return ErrInvalidInput
	}
	// 两级执行指派必须完整配置：选部门 -> 选团队负责人 -> 选执行人。
	if assignmentTypes[ApprovalNodeTeamLead] != assignmentTypes[ApprovalNodeExecutor] ||
		(assignmentTypes[ApprovalNodeTeamLead] && !assignmentTypes[ApprovalNodeDepartment]) {
		return ErrInvalidInput
	}
	if assignmentTypes[ApprovalNodeTeamLead] {
		// 第①级是同一个人的原子操作；如果部门和团队负责人分属两个角色，
		// 任何单一主体都无法完成提交，规则不可用。
		combined := false
		for _, actions := range roleActions {
			if actions[ApprovalNodeDepartment] && actions[ApprovalNodeTeamLead] {
				combined = true
				break
			}
		}
		if !combined {
			return ErrInvalidInput
		}
	}
	return nil
}
