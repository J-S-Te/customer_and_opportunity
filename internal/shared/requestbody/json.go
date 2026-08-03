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

// 在默认 1 MiB 限制内只解码一个 JSON 对象；空体、null、数组、标量、未知字段、畸形 JSON 和
// 首对象后的非空白内容都会被拒绝。解码后仍执行 Gin validator，使绑定标签保持原语义。
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
