package requestbody

import (
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"strings"

	"github.com/go-playground/validator/v10"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
)

var fieldIndex = regexp.MustCompile(`^\[[0-9]{1,6}\]$`)

// InvalidArgument 用途：把严格解码或绑定错误转换为不含输入值的安全诊断。
// 参数：err 为解码错误，target 为已绑定 DTO，仅用于解析已声明的 JSON 字段名。
// 返回值：保持原 400 错误码与消息的错误，附带字段规则或协议原因。
func InvalidArgument(err error, target any) error {
	base := apperror.New(400, "COMMON_INVALID_ARGUMENT", "invalid request")
	var fields validator.ValidationErrors
	if errors.As(err, &fields) {
		issues := make([]map[string]string, 0, len(fields))
		for _, field := range fields {
			if path := validationFieldPath(reflect.TypeOf(target), field.StructNamespace()); path != "" {
				issues = append(issues, map[string]string{"field": path, "rule": field.Tag()})
			}
			if len(issues) >= 20 {
				break
			}
		}
		return apperror.WithDetails(base, map[string]any{"issues": issues})
	}
	reason := "invalid_json"
	var typeError *json.UnmarshalTypeError
	if errors.As(err, &typeError) {
		reason = "invalid_type"
	} else if err != nil && strings.HasPrefix(err.Error(), "json: unknown field ") {
		reason = "unknown_field"
	}
	return apperror.WithDetails(base, map[string]string{"reason": reason})
}

// validationFieldPath 用途：将校验器的结构体路径映射为真实请求 JSON 路径。
// 参数：kind 为 DTO 类型，namespace 为校验器提供的结构体路径。
// 返回值：只含已声明字段和数组下标的路径；无法安全解析时返回空字符串。
func validationFieldPath(kind reflect.Type, namespace string) string {
	parts := strings.Split(namespace, ".")
	if len(parts) < 2 {
		return ""
	}
	result := make([]string, 0, len(parts)-1)
	for _, part := range parts[1:] {
		for kind != nil && kind.Kind() == reflect.Pointer {
			kind = kind.Elem()
		}
		if kind == nil || kind.Kind() != reflect.Struct {
			return ""
		}
		name, suffix, _ := strings.Cut(part, "[")
		field, ok := kind.FieldByName(name)
		if !ok {
			return ""
		}
		jsonName := strings.Split(field.Tag.Get("json"), ",")[0]
		if jsonName == "" {
			jsonName = field.Name
		}
		if jsonName == "-" {
			return ""
		}
		kind = field.Type
		for kind.Kind() == reflect.Pointer {
			kind = kind.Elem()
		}
		if suffix != "" {
			suffix = "[" + suffix
			if !fieldIndex.MatchString(suffix) || (kind.Kind() != reflect.Slice && kind.Kind() != reflect.Array) {
				return ""
			}
			kind = kind.Elem()
		}
		result = append(result, jsonName+suffix)
	}
	return strings.Join(result, ".")
}
