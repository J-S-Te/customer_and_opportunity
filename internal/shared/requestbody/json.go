// Package requestbody 为 HTTP 请求体提供失败关闭的严格 JSON 解码。
package requestbody

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
)

const DefaultJSONLimit int64 = 1 << 20

// ErrDuplicateJSONKey 表示同一 JSON 对象内出现重复键。Go 标准库默认 last-wins，
// 会让上游借重复键同时满足校验和落库的不同取值；严格解码必须显式拒绝。
var ErrDuplicateJSONKey = errors.New("request body must not repeat a JSON object key")

// 在默认 1 MiB 限制内只解码一个 JSON 对象；空体、null、数组、标量、未知字段、重复键、
// 畸形 JSON 和首对象后的非空白内容都会被拒绝。解码后仍执行 Gin validator，使绑定标签保持原语义。
func DecodeJSON(c *gin.Context, target any) error {
	return DecodeJSONWithLimit(c, target, DefaultJSONLimit)
}

// 允许端点指定更小的正数上限，但不改变严格对象与校验器语义。
func DecodeJSONWithLimit(c *gin.Context, target any, limit int64) error {
	if c == nil || c.Request == nil || c.Request.Body == nil || target == nil || limit <= 0 {
		return errors.New("invalid JSON request")
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
	decoder := json.NewDecoder(c.Request.Body)
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain one JSON object")
		}
		return err
	}

	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return errors.New("request body must contain one JSON object")
	}
	if err := rejectDuplicateKeys(trimmed); err != nil {
		return err
	}

	objectDecoder := json.NewDecoder(bytes.NewReader(trimmed))
	objectDecoder.DisallowUnknownFields()
	if err := objectDecoder.Decode(target); err != nil {
		return err
	}
	if err := binding.Validator.ValidateStruct(target); err != nil {
		return err
	}
	return nil
}

// rejectDuplicateKeys 用 token 游标递归扫描已读取的请求体字节，识别同一对象内的重复键。
// 扫描只消费未修改的字节副本，解码到 DTO 仍使用原始字节，因此不会改变大小上限、
// 单对象、未知字段拒绝和 validator 语义。
func rejectDuplicateKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := scanJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain one JSON object")
		}
		return err
	}
	return nil
}

// scanJSONValue 消费一个完整 JSON 值；对象按字符串键去重，数组逐元素递归。
// 不同对象（包括同一数组里的不同元素）出现相同键名不是重复键，不会被误报。
func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("request body must contain one JSON object")
			}
			if _, duplicate := seen[key]; duplicate {
				return ErrDuplicateJSONKey
			}
			seen[key] = struct{}{}
			if err = scanJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err = scanJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	default:
		return errors.New("request body must contain one JSON object")
	}
}
