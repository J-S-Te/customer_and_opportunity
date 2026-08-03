package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
)

const RequestIDHeader = "X-Request-ID"

func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 仅接受长度受限的可打印标识字符，拒绝换行和分隔符注入；不可信值会被服务端新 ID 替换。
		id := strings.TrimSpace(c.GetHeader(RequestIDHeader))
		if !validRequestID(id) {
			id = request.NewID()
		}
		c.Header(RequestIDHeader, id)
		c.Request = c.Request.WithContext(request.WithID(c.Request.Context(), id))
		c.Next()
	}
}

func validRequestID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if !(r == '-' || r == '_' || r == '.' || r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z') {
			return false
		}
	}
	return true
}
