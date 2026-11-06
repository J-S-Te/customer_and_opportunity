package localidentity

import "testing"

// TestCustomerBindingReferenceIsolation 验证本地引用按客户与主体隔离且保持平台长度边界。
// 参数：t为测试上下文。
// 返回值：无；引用碰撞、越界或接受不规范身份时失败。
func TestCustomerBindingReferenceIsolation(t *testing.T) {
	first, err := CustomerBindingRef(7, "01ARZ3NDEKTSV4RRFFQ69G5FAV")
	if err != nil || first != "portal-local:v1:7:01ARZ3NDEKTSV4RRFFQ69G5FAV" {
		t.Fatal(first, err)
	}
	second, err := CustomerBindingRef(7, "01ARZ3NDEKTSV4RRFFQ69G5FAW")
	if err != nil || first == second {
		t.Fatal("same-customer subjects collided", err)
	}
	max, err := CustomerBindingRef(^uint64(0), "01ARZ3NDEKTSV4RRFFQ69G5FAV")
	if err != nil || len(max) > 64 {
		t.Fatal("platform limit exceeded", len(max), err)
	}
	if got, err := ParseCustomerBindingRef(first, "01ARZ3NDEKTSV4RRFFQ69G5FAV"); err != nil || got != 7 {
		t.Fatal(got, err)
	}
	for _, raw := range []string{"7", "crm:7", "portal-local:v2:7:subject", "portal-local:v1:07:subject", "portal-local:v1:0:subject", "portal-local:v1:+7:subject", "portal-local:v1:7:other", "portal-local:v1:7:subject:extra", " portal-local:v1:7:subject"} {
		if _, err := ParseCustomerBindingRef(raw, "subject"); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
	for _, subject := range []string{"", "subject:other", "subject\n", "abcdefghijklmnopqrstuvwxyzA", "主体"} {
		if _, err := CustomerBindingRef(7, subject); err == nil {
			t.Errorf("accepted subject %q", subject)
		}
	}
}
