// Command authz-catalog 输出编译进应用二进制的 Claims 兼容哈希；运维可据此配置 OIDC 客户端，
// 并在部署前与基础平台目录元数据核对。
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/platformcatalog"
)

func main() {
	action, application := parseArguments(os.Args[1:])
	if application == "" {
		fmt.Fprintln(os.Stderr, "usage: authz-catalog [print|publish] <crm|portal>")
		os.Exit(2)
	}
	manifest := platformcatalog.CRMManifest()
	if application == "portal" {
		manifest = platformcatalog.PortalManifest()
	}
	if action == "publish" {
		// CRM 部署契约使用 PLATFORM_* 前缀（compose 会覆写 PLATFORM_BASE_URL 为内部地址）；
		// Portal 独立部署契约使用 PORTAL_* 前缀，与 portal-server 启动时的配置读取保持一致。
		// 两者都只从容器环境变量读取，绝不通过命令行参数传递客户端 Secret。
		var options platformcatalog.Options
		options.Enabled = true
		if application == "portal" {
			options.BaseURL = os.Getenv("PORTAL_PLATFORM_BASE_URL")
			options.ApplicationID = os.Getenv("PORTAL_AUTHORIZATION_CATALOG_APPLICATION_ID")
			options.ClientID = os.Getenv("PORTAL_AUTHORIZATION_CATALOG_CLIENT_ID")
			options.ClientSecret = os.Getenv("PORTAL_AUTHORIZATION_CATALOG_CLIENT_SECRET")
		} else {
			options.BaseURL = os.Getenv("PLATFORM_BASE_URL")
			options.ApplicationID = os.Getenv("PLATFORM_AUTHORIZATION_CATALOG_APPLICATION_ID")
			options.ClientID = os.Getenv("PLATFORM_AUTHORIZATION_CATALOG_CLIENT_ID")
			options.ClientSecret = os.Getenv("PLATFORM_AUTHORIZATION_CATALOG_CLIENT_SECRET")
		}
		if err := platformcatalog.Publish(context.Background(), manifest, options); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("authorization catalog published: application=%s\n", application)
		return
	}
	hash, err := platformcatalog.ClaimsRoleConfigHash(manifest)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	checksum, err := platformcatalog.CatalogChecksum(manifest)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("application=%s\ncatalog_version=%s\ncatalog_checksum=%s\nclaims_role_config_hash=%s\nmax_effective_roles=%d\n", application, manifest.Version, checksum, hash, manifest.Policy.MaxEffectiveRoles)
}

func parseArguments(arguments []string) (string, string) {
	if len(arguments) == 1 {
		application := strings.ToLower(strings.TrimSpace(arguments[0]))
		if application == "crm" || application == "portal" {
			return "print", application
		}
	}
	if len(arguments) == 2 {
		action := strings.ToLower(strings.TrimSpace(arguments[0]))
		application := strings.ToLower(strings.TrimSpace(arguments[1]))
		if (action == "print" || action == "publish") && (application == "crm" || application == "portal") {
			return action, application
		}
	}
	return "", ""
}
