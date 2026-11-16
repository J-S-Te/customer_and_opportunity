package opportunity

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/audit"
)

// TestSidebarTeamSaveMinimumOnMySQL 验证一名成员通过实际HTTP保存并持久回读。
// 参数：t 为专用MySQL场景；前置拒绝无写权限与后续旧版本均不放宽。
// 返回值：无；成员、父版本或真实操作者未正确保存时失败。
func TestSidebarTeamSaveMinimumOnMySQL(t *testing.T) {
	s := newSidebarScenario(t)
	payload := sidebarMemberPayload(t)
	s.call(t, http.MethodPut, "/members", payload, "sidebar-qa-read-only", http.StatusForbidden)
	result := sidebarData[TeamResponse](t,
		s.call(t, http.MethodPut, "/members", payload, "sidebar-qa-session", http.StatusOK))
	if result.Version != 2 || len(result.Members) != 1 || result.Members[0].UserID != "qa-helper" {
		t.Fatalf("团队保存结果不完整：%+v", result)
	}
	read := sidebarData[TeamResponse](t,
		s.call(t, http.MethodGet, "/members", nil, "sidebar-qa-session", http.StatusOK))
	if len(read.Members) != 1 || read.Members[0].Role != MemberRoleTechnicalSupport {
		t.Fatalf("成员持久回读不一致：%+v", read)
	}
	var stored Opportunity
	if err := s.db.First(&stored, s.opportunity.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Version != 2 || stored.UpdatedBy != s.principal.UserID {
		t.Errorf("团队修改必须保存实际操作者：version=%d updated_by=%s", stored.Version, stored.UpdatedBy)
	}
	s.call(t, http.MethodPut, "/members", payload, "sidebar-qa-session", http.StatusConflict)
}

// TestSidebarMemberTermMinimumOnMySQL 验证一次团队加入原子生成一个真实任期。
// 参数：t 为独立新商机场景，不依赖团队保存用例留下的数据。
// 返回值：无；成员职责、起始时间、操作者、来源或租户边界不正确时失败。
func TestSidebarMemberTermMinimumOnMySQL(t *testing.T) {
	s := newSidebarScenario(t)
	s.call(t, http.MethodPut, "/members", sidebarMemberPayload(t), "sidebar-qa-session", http.StatusOK)
	page := sidebarData[struct {
		Items []MemberTermResponse `json:"items"`
		Total int64                `json:"total"`
	}](t, s.call(t, http.MethodGet, "/member-terms?active_only=true&page=1&page_size=20",
		nil, "sidebar-qa-session", http.StatusOK))
	if page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("一次加入应只生成一个任期：%+v", page)
	}
	term := page.Items[0]
	if term.UserID != "qa-helper" || term.Role != MemberRoleTechnicalSupport ||
		term.SourceKind != MemberTermSourceRecorded || term.StartedAt == nil ||
		!term.StartedAt.Equal(s.now) || term.EndedAt != nil || term.SnapshotAt != nil ||
		term.StartedBy == nil || *term.StartedBy != s.principal.UserID {
		t.Errorf("任期事实不一致：%+v", term)
	}
	s.call(t, http.MethodGet, "/member-terms", nil, "sidebar-qa-other-tenant", http.StatusNotFound)
}

// TestSidebarAttachmentMinimumOnMySQL 验证一份合成PDF经上传校验后受控下载。
// 参数：t 为新商机、真实MySQL与临时本地文件网关的隔离场景。
// 返回值：无；未校验下载、摘要、实际下载字节或持久状态不正确时失败。
func TestSidebarAttachmentMinimumOnMySQL(t *testing.T) {
	s := newSidebarScenario(t)
	content := []byte("%PDF-1.4\n1 0 obj<</Type/Catalog>>endobj\n%%EOF")
	digest := sha256.Sum256(content)
	created := sidebarData[AttachmentUploadSessionResponse](t,
		s.call(t, http.MethodPost, "/attachments", sidebarJSON(t, AttachmentCreateRequest{
			FileName: "qa-proof.pdf", SizeBytes: uint64(len(content)), MIMEType: "application/pdf",
			SHA256: hex.EncodeToString(digest[:]),
		}), "sidebar-qa-session", http.StatusCreated))
	path := "/attachments/" + created.Attachment.ID
	s.call(t, http.MethodGet, path+"/content", nil, "sidebar-qa-session", http.StatusConflict)
	s.call(t, http.MethodPut, path+"/content", content, "sidebar-qa-session", http.StatusOK)
	completed := sidebarData[AttachmentResponse](t,
		s.call(t, http.MethodPost, path+"/complete", sidebarJSON(t, AttachmentCompleteRequest{
			Version: created.Attachment.Version,
		}), "sidebar-qa-session", http.StatusOK))
	if completed.ScanStatus != AttachmentClean || completed.ScannedAt == nil {
		t.Fatalf("文件尚未真实校验完成：%+v", completed)
	}
	downloaded := s.call(t, http.MethodGet, path+"/content", nil, "sidebar-qa-session", http.StatusOK)
	if !bytes.Equal(downloaded, content) {
		t.Fatal("受控下载字节与上传原件不一致")
	}
	var stored Attachment
	if err := s.db.Where("tenant_id=? AND public_id=?", s.principal.TenantID, created.Attachment.ID).
		First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.ScanStatus != AttachmentClean || stored.SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("文件持久状态或摘要错误：%+v", stored)
	}
	var event audit.Event
	if err := s.db.Where("tenant_id=? AND resource_id=? AND operation=?",
		s.principal.TenantID, stored.PublicID, "SCAN_PASSED").First(&event).Error; err != nil {
		t.Fatal(err)
	}
	var snapshot AttachmentResponse
	if err := json.Unmarshal(event.AfterJSON, &snapshot); err != nil || snapshot.ScanStatus != AttachmentClean {
		t.Errorf("文件校验审计必须保留真实完成状态：%s，错误=%v", event.AfterJSON, err)
	}
	s.call(t, http.MethodGet, path+"/content", nil, "sidebar-qa-other-tenant", http.StatusNotFound)
}
