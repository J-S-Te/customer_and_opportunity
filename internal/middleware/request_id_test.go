package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
)

func TestRequestIDAcceptsOnlyBoundedLogSafeValues(t *testing.T) {
	tests := []struct {
		name, supplied string
		preserved      bool
	}{
		{name: "valid", supplied: "01J_request.safe-1", preserved: true},
		{name: "newline", supplied: "unsafe\nvalue"},
		{name: "space", supplied: "unsafe value"},
		{name: "too long", supplied: strings.Repeat("a", 129)},
	}
	for _, item := range tests {
		t.Run(item.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodGet, "/", nil)
			context.Request.Header.Set(RequestIDHeader, item.supplied)
			RequestID()(context)
			got := request.ID(context.Request.Context())
			if item.preserved && got != item.supplied {
				t.Fatalf("safe request ID changed: %q", got)
			}
			if !item.preserved && (got == item.supplied || !validRequestID(got)) {
				t.Fatalf("unsafe request ID was not replaced: %q", got)
			}
		})
	}
}
