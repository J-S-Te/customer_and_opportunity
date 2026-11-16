package opportunity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/audit"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/pagination"
)

// commercialUploadPDF 用途：经实际上传、内容校验和完成接口生成一份可信合成 PDF。
// 参数：t 为测试上下文，s 为本用例独立商机及本地文件网关场景。
// 返回值：真实持久化且达到 CLEAN 状态的可信附件。
func commercialUploadPDF(t *testing.T, s *sidebarScenario) AttachmentResponse {
	t.Helper()
	content := []byte("%PDF-1.4\n1 0 obj<</Type/Catalog>>endobj\n%%EOF")
	digest := sha256.Sum256(content)
	created := sidebarData[AttachmentUploadSessionResponse](t, s.call(t, http.MethodPost, "/attachments",
		sidebarJSON(t, AttachmentCreateRequest{FileName: "commercial-proof.pdf", SizeBytes: uint64(len(content)),
			MIMEType: "application/pdf", SHA256: hex.EncodeToString(digest[:])}), "sidebar-qa-session", http.StatusCreated))
	path := "/attachments/" + created.Attachment.ID
	s.call(t, http.MethodPut, path+"/content", content, "sidebar-qa-session", http.StatusOK)
	completed := sidebarData[AttachmentResponse](t, s.call(t, http.MethodPost, path+"/complete",
		sidebarJSON(t, AttachmentCompleteRequest{Version: created.Attachment.Version}), "sidebar-qa-session", http.StatusOK))
	if completed.ScanStatus != AttachmentClean {
		t.Fatalf("附件未通过真实校验：%+v", completed)
	}
	return completed
}

// commercialAssertParentUnchanged 用途：确认人工资料不会污染商机主数据、终态待办或可信快照。
// 参数：t 为测试上下文，s 为创建前保存父对象的独立场景。
// 返回值：无；任一父业务字段或外部投影变化时失败。
func commercialAssertParentUnchanged(t *testing.T, s *sidebarScenario) {
	t.Helper()
	var actual Opportunity
	if err := s.db.First(&actual, s.opportunity.ID).Error; err != nil {
		t.Fatal(err)
	}
	before := s.opportunity
	if actual.Version != before.Version || actual.CurrentStage != before.CurrentStage ||
		!actual.ExpectedAmount.Equal(before.ExpectedAmount) || actual.ContractRef != before.ContractRef ||
		actual.TerminalPendingType != before.TerminalPendingType || actual.Status != before.Status ||
		actual.UpdatedBy != before.UpdatedBy || actual.ExpectedSignDate.Format("2006-01-02") != before.ExpectedSignDate.Format("2006-01-02") {
		t.Fatalf("人工台账改变了父商机：before=%+v after=%+v", before, actual)
	}
	var external, logs int64
	if err := s.db.Model(&ExternalLink{}).Where("tenant_id=? AND opportunity_id=?", s.principal.TenantID, before.ID).Count(&external).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&StageLog{}).Where("tenant_id=? AND opportunity_id=?", s.principal.TenantID, before.ID).Count(&logs).Error; err != nil {
		t.Fatal(err)
	}
	if external != 0 || logs != 0 {
		t.Fatalf("人工资料生成了可信快照或阶段日志：external=%d logs=%d", external, logs)
	}
}

// TestCommercialRecordNativeCreateOnMySQL 用途：独立验证真实资料创建、可信附件关联与精确重放。
// 参数：t 为仅接受显式隔离 DSN 及实例 UUID 的测试上下文。
// 返回值：无；持久化、父边界、权限撤销或创建重放错误时失败。
func TestCommercialRecordNativeCreateOnMySQL(t *testing.T) {
	s := newSidebarScenario(t)
	attachment := commercialUploadPDF(t, s)
	input := commercialTestInput()
	input.AttachmentIDs = []string{attachment.ID}
	body := sidebarJSON(t, input)
	s.call(t, http.MethodPost, "/commercial-records", body, "sidebar-qa-read-only", http.StatusForbidden)
	s.call(t, http.MethodPost, "/commercial-records", body, "sidebar-qa-other-tenant", http.StatusNotFound)
	first := sidebarData[CommercialRecordResponse](t, s.call(t, http.MethodPost, "/commercial-records", body, "sidebar-qa-session", http.StatusCreated))
	if first.SourceKind != "MANUAL" || first.Version != 1 || len(first.Attachments) != 1 ||
		first.Attachments[0].ID != attachment.ID || first.Amount == nil || *first.Amount != "123.40" {
		t.Fatalf("实际创建回执不完整：%+v", first)
	}
	page := sidebarData[pagination.Page[CommercialRecordResponse]](t, s.call(t, http.MethodGet, "/commercial-records?page=1&page_size=20", nil, "sidebar-qa-session", http.StatusOK))
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != first.ID || len(page.Items[0].Attachments) != 1 {
		t.Fatalf("关联资料持久回读不一致：%+v", page)
	}
	replay := sidebarData[CommercialRecordResponse](t, s.call(t, http.MethodPost, "/commercial-records", body, "sidebar-qa-session", http.StatusCreated))
	firstJSON, replayJSON := sidebarJSON(t, first), sidebarJSON(t, replay)
	if !bytes.Equal(firstJSON, replayJSON) {
		t.Fatalf("创建重放不是原响应：first=%s replay=%s", firstJSON, replayJSON)
	}
	input.Title = "幂等键变载荷"
	s.call(t, http.MethodPost, "/commercial-records", sidebarJSON(t, input), "sidebar-qa-session", http.StatusConflict)
	var records, audits int64
	if err := s.db.Model(&CommercialRecord{}).Where("tenant_id=?", s.principal.TenantID).Count(&records).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&audit.Event{}).Where("tenant_id=? AND module=?", s.principal.TenantID, "opportunity_commercial_record").Count(&audits).Error; err != nil {
		t.Fatal(err)
	}
	if records != 1 || audits != 1 {
		t.Fatalf("重试重复创建或审计：records=%d audits=%d", records, audits)
	}
	delete(s.principal.Permissions, "opportunity.attachment.read")
	denied := s.call(t, http.MethodPost, "/commercial-records", body, "sidebar-qa-session", http.StatusForbidden)
	if bytes.Contains(denied, []byte(attachment.ID)) || bytes.Contains(denied, []byte(attachment.FileName)) {
		t.Fatal("撤销附件权限后的重放泄露文件信息")
	}
	hidden := sidebarData[pagination.Page[CommercialRecordResponse]](t, s.call(t, http.MethodGet, "/commercial-records", nil, "sidebar-qa-session", http.StatusOK))
	if len(hidden.Items) != 1 || len(hidden.Items[0].Attachments) != 0 {
		t.Fatalf("无文件权限的列表泄露附件：%+v", hidden)
	}
	commercialAssertParentUnchanged(t, s)
}

// TestCommercialRecordNativeUpdateOnMySQL 用途：独立验证更正持久化与台账自身乐观锁。
// 参数：t 为仅使用新建隔离商机的测试上下文。
// 返回值：无；更正未保存、旧版本通过或污染父对象时失败。
func TestCommercialRecordNativeUpdateOnMySQL(t *testing.T) {
	s := newSidebarScenario(t)
	input := commercialTestInput()
	first := sidebarData[CommercialRecordResponse](t, s.call(t, http.MethodPost, "/commercial-records", sidebarJSON(t, input), "sidebar-qa-session", http.StatusCreated))
	amount := "999.99"
	input.Amount = &amount
	input.Title = "人工更正报价"
	input.Status = "APPROVED"
	input.Version = first.Version
	path := "/commercial-records/" + first.ID
	changed := sidebarData[CommercialRecordResponse](t, s.call(t, http.MethodPut, path, sidebarJSON(t, input), "sidebar-qa-session", http.StatusOK))
	if changed.Version != 2 || changed.Title != "人工更正报价" || changed.Amount == nil || *changed.Amount != "999.99" || changed.UpdatedBy != s.principal.UserID {
		t.Fatalf("更正资料未持久保存：%+v", changed)
	}
	s.call(t, http.MethodPut, path, sidebarJSON(t, input), "sidebar-qa-session", http.StatusConflict)
	page := sidebarData[pagination.Page[CommercialRecordResponse]](t, s.call(t, http.MethodGet, "/commercial-records", nil, "sidebar-qa-session", http.StatusOK))
	if len(page.Items) != 1 || page.Items[0].Version != 2 || page.Items[0].Status != "APPROVED" {
		t.Fatalf("更正回读不一致：%+v", page)
	}
	commercialAssertParentUnchanged(t, s)
}

// TestCommercialRecordNativeVoidOnMySQL 用途：独立验证作废保留资料、真实审计及重复版本拒绝。
// 参数：t 为新建隔离商机及真实路由场景。
// 返回值：无；记录被删除、审计缺失或重复作废成功时失败。
func TestCommercialRecordNativeVoidOnMySQL(t *testing.T) {
	s := newSidebarScenario(t)
	input := commercialTestInput()
	input.Type = "BID"
	input.Status = "WON"
	first := sidebarData[CommercialRecordResponse](t, s.call(t, http.MethodPost, "/commercial-records", sidebarJSON(t, input), "sidebar-qa-session", http.StatusCreated))
	path := "/commercial-records/" + first.ID + "/void"
	body := sidebarJSON(t, CommercialRecordVoidRequest{Version: 1, Reason: "错误登记"})
	result := sidebarData[CommercialRecordResponse](t, s.call(t, http.MethodPost, path, body, "sidebar-qa-session", http.StatusOK))
	if !result.IsVoid || result.Version != 2 || result.Status != "WON" || result.VoidReason != "错误登记" {
		t.Fatalf("作废语义混淆了业务状态：%+v", result)
	}
	s.call(t, http.MethodPost, path, body, "sidebar-qa-session", http.StatusConflict)
	page := sidebarData[pagination.Page[CommercialRecordResponse]](t, s.call(t, http.MethodGet, "/commercial-records", nil, "sidebar-qa-session", http.StatusOK))
	if page.Total != 1 || len(page.Items) != 1 || !page.Items[0].IsVoid {
		t.Fatalf("作废资料未保留：%+v", page)
	}
	var event audit.Event
	if err := s.db.Where("tenant_id=? AND resource_id=? AND module=? AND operation=?",
		s.principal.TenantID, first.ID, "opportunity_commercial_record", "VOID").First(&event).Error; err != nil {
		t.Fatal(err)
	}
	var before, after CommercialRecordResponse
	if err := json.Unmarshal(event.BeforeJSON, &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(event.AfterJSON, &after); err != nil {
		t.Fatal(err)
	}
	if before.IsVoid || !after.IsVoid || event.ActorID != s.principal.UserID || event.Reason != "错误登记" {
		t.Fatalf("作废审计未保存真实前后状态：%+v", event)
	}
	commercialAssertParentUnchanged(t, s)
}

type commercialFailAfterAudit struct{ writer audit.Writer }

// TestCommercialRecordNativeHiddenAttachmentsOnMySQL 用途：验证受限人员更正和作废保留隐藏关联及真实审计。
// 参数：t 为新建隔离商机及可信文件的实际 HTTP 场景。
// 返回值：无；回执泄漏文件、隐藏关联丢失或审计脱敏失真时失败。
func TestCommercialRecordNativeHiddenAttachmentsOnMySQL(t *testing.T) {
	s := newSidebarScenario(t)
	attachment := commercialUploadPDF(t, s)
	input := commercialTestInput()
	input.AttachmentIDs = []string{attachment.ID}
	created := sidebarData[CommercialRecordResponse](t, s.call(t, http.MethodPost,
		"/commercial-records", sidebarJSON(t, input), "sidebar-qa-session", http.StatusCreated))
	delete(s.principal.Permissions, "opportunity.attachment.read")
	input.Version = 1
	s.call(t, http.MethodPut, "/commercial-records/"+created.ID,
		sidebarJSON(t, input), "sidebar-qa-session", http.StatusForbidden)
	input.Title, input.Version, input.AttachmentIDs = "仅更正文字", 1, []string{}
	path := "/commercial-records/" + created.ID
	updated := s.call(t, http.MethodPut, path, sidebarJSON(t, input), "sidebar-qa-session", http.StatusOK)
	if bytes.Contains(updated, []byte(attachment.ID)) || bytes.Contains(updated, []byte(attachment.FileName)) {
		t.Fatal("受限更正回执泄露文件信息")
	}
	voided := s.call(t, http.MethodPost, path+"/void",
		sidebarJSON(t, CommercialRecordVoidRequest{Version: 2, Reason: "错误录入"}),
		"sidebar-qa-session", http.StatusOK)
	if bytes.Contains(voided, []byte(attachment.ID)) || bytes.Contains(voided, []byte(attachment.FileName)) {
		t.Fatal("受限作废回执泄露文件信息")
	}
	s.principal.Permissions["opportunity.attachment.read"] = struct{}{}
	page := sidebarData[pagination.Page[CommercialRecordResponse]](t, s.call(t, http.MethodGet,
		"/commercial-records", nil, "sidebar-qa-session", http.StatusOK))
	if len(page.Items) != 1 || len(page.Items[0].Attachments) != 1 || page.Items[0].Attachments[0].ID != attachment.ID {
		t.Fatalf("受限更正静默丢失原附件关联：%+v", page)
	}
	for _, operation := range []string{"UPDATE", "VOID"} {
		var event audit.Event
		if err := s.db.Where("tenant_id=? AND resource_id=? AND module=? AND operation=?",
			s.principal.TenantID, created.ID, "opportunity_commercial_record", operation).First(&event).Error; err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(event.BeforeJSON, []byte(attachment.ID)) ||
			!bytes.Contains(event.AfterJSON, []byte(attachment.ID)) || event.ActorID != s.principal.UserID {
			t.Fatalf("%s审计未保留实际关联和操作者", operation)
		}
	}
	commercialAssertParentUnchanged(t, s)
}

// Write 用途：先执行真实审计写入再模拟后续失败，验证同事务审计也被回滚。
// 参数：ctx 为资料事务，event 为待写入的真实合成审计。
// 返回值：真实写入错误或固定的隔离故障错误。
func (w commercialFailAfterAudit) Write(ctx context.Context, event audit.Event) error {
	if err := w.writer.Write(ctx, event); err != nil {
		return err
	}
	return errors.New("隔离测试注入：审计写入后失败")
}

// TestCommercialRecordNativeAuditRollbackOnMySQL 用途：验证真实数据库中资料、关联、幂等及审计一起回滚。
// 参数：t 为新建隔离商机；审计故障仅注入本测试构造的服务。
// 返回值：无；失败命令留下任一部分数据时失败。
func TestCommercialRecordNativeAuditRollbackOnMySQL(t *testing.T) {
	s := newSidebarScenario(t)
	attachment := commercialUploadPDF(t, s)
	repo := NewGORMRepository(s.db)
	service := NewCommercialRecordService(NewGORMCommercialRecordRepository(s.db), repo,
		commercialFailAfterAudit{writer: audit.NewGORMWriter(s.db)})
	input := commercialTestInput()
	input.AttachmentIDs = []string{attachment.ID}
	if _, err := service.Create(auth.WithPrincipal(t.Context(), s.principal), s.opportunity.ID, input); err == nil {
		t.Fatal("注入审计错误却返回成功")
	}
	for _, table := range []string{"crm_opportunity_commercial_records", "crm_opportunity_commercial_record_attachments", "crm_opportunity_change_idempotency"} {
		var count int64
		if err := s.db.Table(table).Where("tenant_id=?", s.principal.TenantID).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("失败命令残留部分数据：%s=%d", table, count)
		}
	}
	var events int64
	if err := s.db.Model(&audit.Event{}).Where("tenant_id=? AND module=?", s.principal.TenantID, "opportunity_commercial_record").Count(&events).Error; err != nil {
		t.Fatal(err)
	}
	if events != 0 {
		t.Fatalf("失败资料残留已成功审计：%d", events)
	}
	commercialAssertParentUnchanged(t, s)
}
