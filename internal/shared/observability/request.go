package observability

import "github.com/gin-gonic/gin"

const errorCodeContextKey = "observability_error_code"

// 把稳定公开错误码交给访问日志，无需复制响应体或暴露内部 Cause。
func RecordErrorCode(c *gin.Context, code string) {
	c.Set(errorCodeContextKey, code)
}

// 返回当前请求已记录的稳定公开错误码；未发生受控错误时为空。
func ErrorCode(c *gin.Context) string {
	value, _ := c.Get(errorCodeContextKey)
	code, _ := value.(string)
	return code
}
