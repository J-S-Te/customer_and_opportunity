package opportunity

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
)

// TestUnsubscribedManualRetryDoesNotTouchOutbox 验证HTTP重试入口同样执行订阅门禁。
// 参数：t 为当前测试上下文。
// 返回值：无；错误调用空数据库或声称重排成功时失败。
func TestUnsubscribedManualRetryDoesNotTouchOutbox(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Params = gin.Params{{Key: "id", Value: "7"}}
	c.Request = httptest.NewRequest(http.MethodPost, "/contract-transfer-outbox/failed/7/retry", nil)
	c.Request = c.Request.WithContext(auth.WithPrincipal(context.Background(), auth.Principal{TenantID: "tenant-a", UserID: "actor-a"}))
	handler := &Handler{service: (&Service{}).UseWinConfirmation(WinModeContract, false, nil), outbox: &GORMRepository{}}
	handler.RetryFailedContractTransfer(c)
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "CRM_CONTRACT_TRANSFER_NOT_SUBSCRIBED") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
