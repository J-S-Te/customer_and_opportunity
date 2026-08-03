package response

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/observability"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
)

type Envelope struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
	Data      any    `json:"data,omitempty"`
	Details   any    `json:"details,omitempty"`
}

func OK(c *gin.Context, data any) {
	c.JSON(http.StatusOK, Envelope{Code: "OK", Message: "success", RequestID: request.ID(c.Request.Context()), Data: data})
}

func Created(c *gin.Context, data any) {
	c.JSON(http.StatusCreated, Envelope{Code: "OK", Message: "success", RequestID: request.ID(c.Request.Context()), Data: data})
}

func Error(c *gin.Context, err error) {
	// 对外只暴露稳定错误码和安全消息，内部 Cause 不进入响应；同时把错误码留给访问日志关联。
	appErr := apperror.As(err)
	observability.RecordErrorCode(c, appErr.Code)
	c.JSON(appErr.HTTPStatus, Envelope{Code: appErr.Code, Message: appErr.Message, RequestID: request.ID(c.Request.Context()), Details: appErr.Details})
}
