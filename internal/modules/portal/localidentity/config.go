// Package localidentity 实现门户自有客户目录与显式本地业务来源，不读取CRM数据。
package localidentity

import (
	"errors"
	"net/http"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
)

type Mode string

const (
	ModeCRM        Mode = "crm"
	ModeLocal      Mode = "local"
	LocalNamespace      = "portal-local"
)

var (
	ErrAuthorityConflict = apperror.New(http.StatusConflict, "PORTAL_CUSTOMER_AUTHORITY_CONFLICT", "客户目录权威已固定，不能直接切换来源")
	ErrNamespace         = apperror.New(http.StatusUnprocessableEntity, "PORTAL_CUSTOMER_NAMESPACE_INVALID", "本地客户输入必须明确portal-local命名空间")
	ErrInvalid           = apperror.New(http.StatusUnprocessableEntity, "PORTAL_LOCAL_INVALID", "本地门户参数无效")
	ErrConflict          = apperror.New(http.StatusConflict, "PORTAL_LOCAL_CONFLICT", "本地门户记录已变化或操作号载荷不一致")
	ErrNotFound          = apperror.New(http.StatusNotFound, "PORTAL_LOCAL_NOT_FOUND", "本地门户记录不存在或不可访问")
	ErrUnavailable       = apperror.New(http.StatusServiceUnavailable, "PORTAL_LOCAL_UNAVAILABLE", "本地门户依赖暂不可用，请按原操作号核对并重试")
)

// ParseMode 读取默认保留CRM的客户目录权威模式。
// 参数：raw 为受控运行配置，不接受浏览器临时声明。
// 返回值：crm或local；未知模式失败关闭。
func ParseMode(raw string) (Mode, error) {
	switch raw {
	case "", "crm":
		return ModeCRM, nil
	case "local":
		return ModeLocal, nil
	default:
		return "", errors.New("invalid PORTAL_CUSTOMER_DIRECTORY_MODE")
	}
}

// GuardCustomerInput 验证业务来源命名空间而不将数字碰撞当作客户匹配。
// 参数：mode 为配置权威，namespace 为可信协议来源字段，id 为真实客户主键。
// 返回值：合法来源返回空，local旧无来源输入返回错误。
func GuardCustomerInput(mode Mode, namespace string, id uint64) error {
	if id == 0 {
		return ErrInvalid
	}
	if mode == ModeLocal && namespace == LocalNamespace {
		return nil
	}
	if mode == ModeCRM && (namespace == "" || namespace == "crm") {
		return nil
	}
	return ErrNamespace
}
