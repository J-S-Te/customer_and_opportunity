package opportunity

import (
	"strings"
	"testing"
)

// TestCommercialRecordValidation 用途：验证人工资料的金额、业务日期、状态及附件边界。
// 参数：t 为当前测试上下文。
// 返回值：无；错误接受或归一化不正确时使测试失败。
func TestCommercialRecordValidation(t *testing.T) {
	amount := "123.4"
	input := CommercialRecordRequest{Type: "QUOTATION", Title: " 人工报价 ", RecordDate: "2026-11-16", Amount: &amount}
	got, err := normalizeCommercialRecord(input)
	if err != nil || got.Title != "人工报价" || got.Status != "DRAFT" || got.Amount == nil || *got.Amount != "123.40" {
		t.Fatalf("normalized=%+v err=%v", got, err)
	}
	cases := []struct {
		name   string
		change func(*CommercialRecordRequest)
	}{
		{"negative", func(v *CommercialRecordRequest) { a := "-0.01"; v.Amount = &a }},
		{"precision", func(v *CommercialRecordRequest) { a := "1.001"; v.Amount = &a }},
		{"overflow", func(v *CommercialRecordRequest) { a := "10000000000000000"; v.Amount = &a }},
		{"exponent", func(v *CommercialRecordRequest) { a := "1e2"; v.Amount = &a }},
		{"invalid date", func(v *CommercialRecordRequest) { v.RecordDate = "2026-02-30" }},
		{"missing title", func(v *CommercialRecordRequest) { v.Title = " " }},
		{"long title", func(v *CommercialRecordRequest) { v.Title = strings.Repeat("中", 201) }},
		{"unknown type", func(v *CommercialRecordRequest) { v.Type = "CONTRACT" }},
		{"wrong status", func(v *CommercialRecordRequest) { v.Status = "WON" }},
		{"duplicate attachment", func(v *CommercialRecordRequest) { v.AttachmentIDs = []string{"a", "a"} }},
		{"too many attachments", func(v *CommercialRecordRequest) { v.AttachmentIDs = make([]string, 21) }},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			bad := input
			tt.change(&bad)
			if _, err := normalizeCommercialRecord(bad); err == nil {
				t.Fatal("invalid input accepted")
			}
		})
	}
	bid, err := normalizeCommercialRecord(CommercialRecordRequest{Type: "BID", Title: "投标", RecordDate: "2027-01-01"})
	if err != nil || bid.Status != "PREPARING" || bid.Amount != nil {
		t.Fatalf("bid=%+v err=%v", bid, err)
	}
}
