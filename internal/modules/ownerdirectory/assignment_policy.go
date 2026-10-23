package ownerdirectory

import (
	"sort"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/platformcatalog"
)

const (
	AssignmentDutyTeamLead = "TEAM_LEAD"
	AssignmentDutyExecutor = "EXECUTOR"
)

// AssignmentDutyPermissions 与服务端各职责的实际操作门禁一致，不授予新权限。
func AssignmentDutyPermissions(duty string) []string {
	switch duty {
	case AssignmentDutyTeamLead:
		return []string{"presale.assign"}
	case AssignmentDutyExecutor:
		return []string{"presale.progress", "presale.worklog", "presale.complete"}
	default:
		return nil
	}
}

// AssignmentDutyRoleCodes 从已发布目录推导完整履职角色，供候选查询和保存复核共用。
func AssignmentDutyRoleCodes(duty string) []string {
	required := AssignmentDutyPermissions(duty)
	if len(required) == 0 {
		return nil
	}
	roles := make([]string, 0)
	for _, role := range platformcatalog.CRMManifest().Roles {
		granted := make(map[string]bool, len(role.Permissions))
		for _, permission := range role.Permissions {
			granted[permission] = true
		}
		qualified := true
		for _, permission := range required {
			if !granted[permission] {
				qualified = false
				break
			}
		}
		if qualified {
			roles = append(roles, role.Code)
		}
	}
	sort.Strings(roles)
	return roles
}
