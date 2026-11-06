package localidentity

import (
	"strconv"
	"strings"
)

const localCustomerRefPrefix = "portal-local:v1:"

// CustomerBindingRef 将真实本地客户和平台主体编码为平台不透明引用。
// 参数：customer为本地客户主键，subject为平台签发的有界ASCII主体。
// 返回值：最多63字节的版本化引用；空值或不能无歧义编码时拒绝。
func CustomerBindingRef(customer uint64, subject string) (string, error) {
	if customer == 0 || len(subject) == 0 || len(subject) > 26 {
		return "", ErrInvalid
	}
	for _, c := range subject {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return "", ErrInvalid
		}
	}
	return localCustomerRefPrefix + strconv.FormatUint(customer, 10) + ":" + subject, nil
}

// ParseCustomerBindingRef 只接受当前版本且精确绑定已认证主体的本地引用。
// 参数：ref为平台权威声明，subject为经认证的平台主体。
// 返回值：真实本地客户主键；旧CRM数字、未知版本和主体不符均拒绝。
func ParseCustomerBindingRef(ref, subject string) (uint64, error) {
	if !strings.HasPrefix(ref, localCustomerRefPrefix) {
		return 0, ErrInvalid
	}
	parts := strings.Split(strings.TrimPrefix(ref, localCustomerRefPrefix), ":")
	if len(parts) != 2 || parts[1] != subject {
		return 0, ErrInvalid
	}
	customer, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil {
		return 0, ErrInvalid
	}
	canonical, err := CustomerBindingRef(customer, subject)
	if err != nil || canonical != ref {
		return 0, ErrInvalid
	}
	return customer, nil
}
