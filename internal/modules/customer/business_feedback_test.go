package customer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestCustomerFeedbackPreservesFieldAndProtocolFailures 用途：防止请求拒绝丢失字段或协议原因。
// 参数：t 为测试上下文；请求使用无业务数据的非法输入。
// 返回值：无；断言失败时报告实际 HTTP 回执。
func TestCustomerFeedbackPreservesFieldAndProtocolFailures(t *testing.T) {
	for _, item := range []struct{ name, body, field, rule, reason string }{
		{"missing name", `{"customer_type":"x","industry":"x","region":"x","contacts":[{"name":"x","phone":"1"}]}`, "name", "required", ""},
		{"nested contact", `{"name":"x","customer_type":"x","industry":"x","region":"x","contacts":[{"name":"x"}]}`, "contacts[0].phone", "required", ""},
		{"unknown field", `{"private_extra":"do-not-echo"}`, "", "", "unknown_field"},
		{"wrong type", `{"name":17}`, "", "", "invalid_type"},
		{"malformed", `{"name":`, "", "", "invalid_json"},
	} {
		t.Run(item.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/customers", strings.NewReader(item.body))
			(&Handler{}).Create(ctx)
			var body struct {
				Code    string `json:"code"`
				Details struct {
					Reason string                         `json:"reason"`
					Issues []struct{ Field, Rule string } `json:"issues"`
				} `json:"details"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if recorder.Code != 400 || body.Code != "COMMON_INVALID_ARGUMENT" {
				t.Fatalf("response=%d %s", recorder.Code, recorder.Body)
			}
			if item.field != "" {
				if len(body.Details.Issues) != 1 || body.Details.Issues[0].Field != item.field || body.Details.Issues[0].Rule != item.rule {
					t.Fatalf("missing field diagnosis: %s", recorder.Body)
				}
			} else if body.Details.Reason != item.reason {
				t.Fatalf("missing protocol reason: %s", recorder.Body)
			}
			if strings.Contains(recorder.Body.String(), "do-not-echo") || strings.Contains(recorder.Body.String(), "private_extra") {
				t.Fatalf("raw input leaked: %s", recorder.Body)
			}
		})
	}
}

// TestCustomerUpdateFeedbackLocatesOptionalContactEmail 用途：可选联系人列表也必须保留嵌套字段位置。
// 参数：t 为测试上下文；非法邮箱只触发既有绑定校验。
// 返回值：无；字段丢失或被拒绝值进入响应时失败。
func TestCustomerUpdateFeedbackLocatesOptionalContactEmail(t *testing.T) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "id", Value: "1"}}
	ctx.Request = httptest.NewRequest(http.MethodPut, "/customers/1", strings.NewReader(`{"name":"x","customer_type":"x","industry":"x","region":"x","owner_user_id":"x","version":1,"reason":"x","contacts":[{"id":1,"name":"x","email":"do-not-echo"}]}`))
	(&Handler{}).Update(ctx)
	if recorder.Code != 400 || !strings.Contains(recorder.Body.String(), `"field":"contacts[0].email"`) || strings.Contains(recorder.Body.String(), "do-not-echo") {
		t.Fatalf("optional nested field lost: %d %s", recorder.Code, recorder.Body)
	}
}
