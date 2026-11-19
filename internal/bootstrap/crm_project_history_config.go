package bootstrap

import (
	"fmt"
	"os"
	"strconv"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/customer"
)

// loadCRMProjectHistory 加载 CRM 直连项目管理的可选配置。
// 参数：无，读取部署配置。
// 返回值：缺少必要凭据或配置不合法时返回错误。
func (c *Config) loadCRMProjectHistory() error {
	enabled, err := strconv.ParseBool(valueOrDefault("CRM_PROJECT_HISTORY_ENABLED", "false"))
	if err != nil {
		return fmt.Errorf("CRM_PROJECT_HISTORY_ENABLED: %w", err)
	}
	c.CRMProjectHistoryEnabled = enabled
	if !enabled {
		return nil
	}
	c.CRMProjectHistory = customer.ProjectHistoryReaderOptions{
		Endpoint: os.Getenv("CRM_PROJECT_HISTORY_URL"), TokenURL: os.Getenv("CRM_PROJECT_HISTORY_TOKEN_URL"),
		ClientID: os.Getenv("CRM_PROJECT_HISTORY_CLIENT_ID"), ClientSecret: os.Getenv("CRM_PROJECT_HISTORY_CLIENT_SECRET"),
		Scope: valueOrDefault("CRM_PROJECT_HISTORY_SCOPE", customer.ProjectManagementHistoryScope),
	}
	return customer.ValidateProjectManagementHistoryOptions(c.CRMProjectHistory)
}
