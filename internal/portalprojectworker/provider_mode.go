package portalprojectworker

import "fmt"

// validateDirectoryMode 禁止旧无命名空间项目同步进入本地权威目录。
// 参数：mode为受控启动配置，缺省保持CRM。
// 返回值：本地模式和非法模式均明确拒绝。
func validateDirectoryMode(mode string) error {
	switch mode {
	case "", "crm":
		return nil
	case "local":
		return fmt.Errorf("portal project source worker is not applicable to local directory mode")
	default:
		return fmt.Errorf("invalid PORTAL_CUSTOMER_DIRECTORY_MODE")
	}
}
