package opportunity

import (
	"strings"
	"testing"

	"github.com/gin-gonic/gin/binding"
)

// SystemCount 的 HTTP 入口由 requestbody.DecodeJSON 调用 Gin validator，
// 这里直接对 DTO 断言绑定标签，锁定“≤10000 放行、超限拒绝”的口径。
func TestSystemCountBindingEnforcesBusinessLimit(t *testing.T) {
	create := CreateRequest{
		Name: "商机", CustomerID: 1, Type: "渗透测试", ExpectedAmount: "1000.00",
		ExpectedSignDate: "2026-08-01", RequirementSummary: "需求", SystemCount: maxSystemCount,
	}
	if err := binding.Validator.ValidateStruct(&create); err != nil {
		t.Fatalf("system count at limit rejected: %v", err)
	}
	create.SystemCount = maxSystemCount + 1
	if err := binding.Validator.ValidateStruct(&create); err == nil {
		t.Fatal("create must reject system count above the business limit")
	}

	update := UpdateRequest{
		Name: "商机", Type: "渗透测试", ExpectedAmount: "1000.00",
		ExpectedSignDate: "2026-08-01", RequirementSummary: "需求", Version: 1, Reason: "变更", SystemCount: maxSystemCount,
	}
	if err := binding.Validator.ValidateStruct(&update); err != nil {
		t.Fatalf("update at limit rejected: %v", err)
	}
	update.SystemCount = maxSystemCount + 1
	if err := binding.Validator.ValidateStruct(&update); err == nil {
		t.Fatal("update must reject system count above the business limit")
	}
}

func TestValidateSystemCountUsesSameLimitAsBinding(t *testing.T) {
	if err := validateSystemCount(maxSystemCount); err != nil {
		t.Fatalf("limit rejected: %v", err)
	}
	if err := validateSystemCount(0); err != nil {
		t.Fatalf("zero must stay valid: %v", err)
	}
	err := validateSystemCount(maxSystemCount + 1)
	if err == nil || !strings.Contains(err.Error(), "system count") {
		t.Fatalf("oversized count err=%v", err)
	}
}
