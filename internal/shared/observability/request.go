package observability

import "github.com/gin-gonic/gin"

const errorCodeContextKey = "observability_error_code"

// RecordErrorCode makes the stable public API error code available to the
// access logger without copying response bodies or exposing internal causes.
func RecordErrorCode(c *gin.Context, code string) {
	c.Set(errorCodeContextKey, code)
}

// ErrorCode returns the stable public API error code recorded for a request.
func ErrorCode(c *gin.Context) string {
	value, _ := c.Get(errorCodeContextKey)
	code, _ := value.(string)
	return code
}
