package credit

import (
	"regexp"
	"strings"
	"testing"
)

// creditNotificationSourceID 是 crm_notifications 唯一键 (tenant_id, source_event_id)
// 的组成部分，必须做到“同一次变化重试得到同一个键，不同变化得到不同的键”。
func TestCreditNotificationSourceIDIsStableForRetriedChange(t *testing.T) {
	const (
		tenant        = "tenant-a"
		customerID    = uint64(42)
		applicationID = uint64(0)
		recipientID   = "owner-1"
		kind          = "CREDIT_RULE_CHANGED"
		title         = "客户信用等级已自动调整"
		body          = "客户信用等级已由规则从 B 调整为 C。"
	)
	first := creditNotificationSourceID(tenant, customerID, applicationID, recipientID, kind, title, body)
	for i := 0; i < 3; i++ {
		if retry := creditNotificationSourceID(tenant, customerID, applicationID, recipientID, kind, title, body); retry != first {
			t.Fatalf("retry %d produced a different source_event_id: %q != %q", i, retry, first)
		}
	}
}

func TestCreditNotificationSourceIDSeparatesConsecutiveLevelChanges(t *testing.T) {
	// BIZ-CRM-03：第一次变化（B→C）与第二次变化（C→D）正文不同，必须是两个键，
	// 否则第二次会被 ON DUPLICATE KEY UPDATE 吞掉。
	first := creditNotificationSourceID("tenant-a", 42, 0, "owner-1", "CREDIT_RULE_CHANGED", "客户信用等级已自动调整", "客户信用等级已由规则从 B 调整为 C。")
	second := creditNotificationSourceID("tenant-a", 42, 0, "owner-1", "CREDIT_RULE_CHANGED", "客户信用等级已自动调整", "客户信用等级已由规则从 C 调整为 D。")
	if first == second {
		t.Fatalf("consecutive level changes share one source_event_id: %q", first)
	}
}

func TestCreditNotificationSourceIDBindsFullNotificationSemantics(t *testing.T) {
	base := creditNotificationSourceID("tenant-a", 42, 0, "owner-1", "CREDIT_RULE_CHANGED", "客户信用等级已自动调整", "客户信用等级已由规则从 B 调整为 C。")
	cases := []struct {
		name string
		got  string
	}{
		{"different tenant", creditNotificationSourceID("tenant-b", 42, 0, "owner-1", "CREDIT_RULE_CHANGED", "客户信用等级已自动调整", "客户信用等级已由规则从 B 调整为 C。")},
		{"different customer", creditNotificationSourceID("tenant-a", 43, 0, "owner-1", "CREDIT_RULE_CHANGED", "客户信用等级已自动调整", "客户信用等级已由规则从 B 调整为 C。")},
		{"different application", creditNotificationSourceID("tenant-a", 42, 7, "owner-1", "CREDIT_RULE_CHANGED", "客户信用等级已自动调整", "客户信用等级已由规则从 B 调整为 C。")},
		{"different recipient", creditNotificationSourceID("tenant-a", 42, 0, "director-1", "CREDIT_RULE_CHANGED", "客户信用等级已自动调整", "客户信用等级已由规则从 B 调整为 C。")},
		{"different kind", creditNotificationSourceID("tenant-a", 42, 0, "owner-1", "CREDIT_APPLICATION_PENDING", "客户信用等级已自动调整", "客户信用等级已由规则从 B 调整为 C。")},
		{"different title", creditNotificationSourceID("tenant-a", 42, 0, "owner-1", "CREDIT_RULE_CHANGED", "信用等级调整待审批", "客户信用等级已由规则从 B 调整为 C。")},
		{"different body", creditNotificationSourceID("tenant-a", 42, 0, "owner-1", "CREDIT_RULE_CHANGED", "客户信用等级已自动调整", "客户信用等级已由规则从 C 调整为 D。")},
	}
	for _, c := range cases {
		if c.got == base {
			t.Fatalf("%s must produce a distinct source_event_id: %q", c.name, c.got)
		}
	}
	// 字段拼接不能因为分隔歧义而碰撞。
	joined := creditNotificationSourceID("tenant-a", 42, 0, "a:b", "CREDIT_RULE_CHANGED", "t", "body")
	shifted := creditNotificationSourceID("tenant-a", 42, 0, "a", "b:CREDIT_RULE_CHANGED", "t", "body")
	if joined == shifted {
		t.Fatal("field boundaries must be unambiguous in the notification digest")
	}
}

func TestCreditNotificationSourceIDFitsColumnAndPlatformEventCode(t *testing.T) {
	// 最长实际取值：64 位租户 ID + 长收件人 + 满长正文，键长度必须仍固定。
	got := creditNotificationSourceID(strings.Repeat("t", 64), ^uint64(0), ^uint64(0), strings.Repeat("r", 64), "CREDIT_RULE_CHANGED", strings.Repeat("标题", 100), strings.Repeat("等级变化", 100))
	if len(got) > 191 {
		t.Fatalf("source_event_id length %d exceeds the 191 safety bound: %q", len(got), got)
	}
	if want := len("credit:") + 64; len(got) != want {
		t.Fatalf("source_event_id length = %d, want fixed %d", len(got), want)
	}
	// 平台事件码只接受 ^[A-Za-z][A-Za-z0-9_:-]{0,127}$ 的原样透传，
	// 否则会退化为 CRM_<hash>；信用通知的键应保持原样可读、可幂等。
	if !regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_:-]{0,127}$`).MatchString(got) {
		t.Fatalf("source_event_id is not a valid platform event code: %q", got)
	}
	// 摘要不得回显租户、收件人等原始标识。
	for _, plain := range []string{strings.Repeat("t", 64), strings.Repeat("r", 64), "CREDIT_RULE_CHANGED"} {
		if strings.Contains(got, plain) {
			t.Fatalf("source_event_id leaks raw field value %q: %q", plain, got)
		}
	}
}
