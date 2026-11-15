package opportunity

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestOpportunityFeedbackUsesSafeStructuredIssues 用途：防止商机绑定错误退化为原始诊断字符串。
// 参数：t 为测试上下文；请求在调用业务服务之前被拒绝。
// 返回值：无；检查字段规则及协议分类，不回显被拒绝值。
func TestOpportunityFeedbackUsesSafeStructuredIssues(t *testing.T) {
	for _, item := range []struct{ body, field, reason string }{
		{`{"customer_id":1,"expected_amount":"1","expected_sign_date":"2026-12-01","requirement_summary":"x"}`, "name", ""},
		{`{"customer_id":"do-not-echo"}`, "", "invalid_type"},
		{`{"private_extra":"do-not-echo"}`, "", "unknown_field"},
	} {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodPost, "/opportunities", strings.NewReader(item.body))
		(&Handler{}).Create(ctx)
		var body struct {
			Details struct {
				Reason string
				Issues []struct{ Field, Rule string }
			}
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if recorder.Code != 400 {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body)
		}
		if item.field != "" {
			if len(body.Details.Issues) != 1 || body.Details.Issues[0].Field != item.field || body.Details.Issues[0].Rule != "required" {
				t.Fatalf("field lost: %s", recorder.Body)
			}
		} else if body.Details.Reason != item.reason {
			t.Fatalf("reason lost: %s", recorder.Body)
		}
		if strings.Contains(recorder.Body.String(), "do-not-echo") {
			t.Fatalf("input leaked: %s", recorder.Body)
		}
	}
}

// TestOpportunityInvalidIDFeedbackNeverEchoesInput 用途：路径标识错误不得返回解析器或被拒绝输入。
// 参数：t 为测试上下文；标识在进入业务服务前被拒绝。
// 返回值：无；原 HTTP 错误码保留且响应不包含原始值。
func TestOpportunityInvalidIDFeedbackNeverEchoesInput(t *testing.T) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "id", Value: "do-not-echo"}}
	ctx.Request = httptest.NewRequest(http.MethodPut, "/opportunities/do-not-echo", nil)
	(&Handler{}).Update(ctx)
	if recorder.Code != 400 || strings.Contains(recorder.Body.String(), "do-not-echo") || strings.Contains(recorder.Body.String(), "strconv") {
		t.Fatalf("path input leaked: %d %s", recorder.Code, recorder.Body)
	}
}
