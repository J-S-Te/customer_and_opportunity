//go:build portal_report_mysql

package reportsecurity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	mysqlDSN "github.com/go-sql-driver/mysql"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/report"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portalreportreference"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestMySQLTrustedIngestGrantWatermarkCompletionAndRiskFreeze(t *testing.T) {
	dsn := os.Getenv("UIP_P1_PORTAL_REPORT_TEST_DSN")
	parsed, err := mysqlDSN.ParseDSN(dsn)
	if err != nil || parsed.DBName != "portal_report_p1_test_20261013" {
		t.Fatal("only the explicitly owned disposable report schema is permitted")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("open isolated report database")
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	if err := db.AutoMigrate(&report.Request{}, &report.File{}, &report.IngestJob{}, &report.StatusEvent{}, &report.Notification{}, &report.RevisionEvent{}, &report.Grant{}, &report.DownloadEvent{}, &report.RiskAlert{}, &report.RiskReviewEvent{}); err != nil {
		t.Fatal("create isolated report fixture schema", err)
	}
	raw := pdfFixture(t)
	d := descriptor(raw)
	store, job := storeFixture(t)
	fixtureID := "qa-p1-" + time.Now().UTC().Format("150405.000000000")
	request := report.Request{CustomerID: job.CustomerID, AccountID: "subject-1", RequestNo: fixtureID, ProjectID: "project-1", ReportType: "QA", Reason: "isolated fixture", Status: report.StatusIngestPending, IdempotencyKey: fixtureID, RequestHash: strings.Repeat("a", 64), SubmittedAt: time.Now().UTC(), ReportValidityStatus: "ACTIVE"}
	request.TenantID = job.TenantID
	request.Version = 1
	request.CreatedBy = "subject-1"
	request.UpdatedBy = "subject-1"
	if err := db.Create(&request).Error; err != nil {
		t.Fatal(err)
	}
	job.RequestID = request.ID
	protected, err := store.protector.Encrypt(context.Background(), []byte("fixture"))
	if err != nil {
		t.Fatal(err)
	}
	serialized, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(serialized)
	job.DescriptorHash = base64.RawURLEncoding.EncodeToString(hash[:])
	job.DescriptorCipher = protected
	job.Status = report.IngestProcessing
	job.LockedBy = fixtureID
	job.EventID = fixtureID
	if err := db.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	source := &gatewayFixture{data: raw, version: "immutable-source-version"}
	ingestor, err := NewGatewayIngestor(source, store)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := ingestor.IngestForJob(context.Background(), job, d)
	if err != nil {
		t.Fatal(err)
	}
	repo := report.NewGORMRepository(db)
	service := report.NewService(repo, nil, nil, store.protector, testClock{}, nil)
	if err := service.CompleteIngest(context.Background(), job, d, receipt); err != nil {
		t.Fatal("trusted ingest projection", err)
	}
	if err := service.CompleteIngest(context.Background(), job, d, receipt); err != nil {
		t.Fatal("exact ingest replay", err)
	}
	var persisted report.Request
	if err := db.First(&persisted, request.ID).Error; err != nil || persisted.Status != report.StatusIssued {
		t.Fatal("report did not become issued", err)
	}
	var count int64
	if err := db.Model(&report.Notification{}).Where("request_id=?", request.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("duplicate/missing issued notification", count, err)
	}
	copied := cjkFontFixture(t)
	identityServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"code": "OK", "data": portalreportreference.Identity{TenantID: job.TenantID, CustomerID: job.CustomerID, AccountID: "subject-1", CustomerName: "客户测试单位", AccountMasked: "13*******90"}})
	}))
	defer identityServer.Close()
	identities := &IdentityHTTPClient{Endpoint: identityServer.URL, Client: identityServer.Client(), Token: func(context.Context) (string, error) { return "fixture-token", nil }}
	watermark, err := NewPDFWatermarker(t.TempDir(), copied, identities, GORMRequestNumbers{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	risk, err := NewAuditRiskPolicy(GORMRiskEvidence{DB: db}, 5*time.Minute, 5)
	if err != nil {
		t.Fatal(err)
	}
	downloads := report.NewDownloadService(repo, store, risk, testClock{}, testIDs{}, report.CryptoTokenGenerator{}, time.Hour).RequireProductionSecurity(watermark)
	actor := report.Actor{TenantID: job.TenantID, CustomerID: job.CustomerID, AccountID: "subject-1"}
	metadata := report.DownloadMetadata{IPHash: strings.Repeat("a", 64), DeviceHash: strings.Repeat("b", 64)}
	grant, err := downloads.CreateGrant(context.Background(), actor, job.RequestID, report.GrantCommand{IdempotencyKey: fixtureID, Metadata: metadata})
	if err != nil {
		t.Fatal("grant creation", err)
	}
	content, err := downloads.AuthorizeDownload(context.Background(), actor, job.RequestID, grant.DownloadToken, metadata)
	if err != nil {
		t.Fatal("actual secure download", err)
	}
	stamped, err := io.ReadAll(content.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := content.Reader.Close(); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(stamped, raw) {
		t.Fatal("watermark not applied")
	}
	if err := content.Complete(context.Background(), true, ""); err != nil {
		t.Fatal("successful audit completion", err)
	}
	var event report.DownloadEvent
	if err := db.Where("request_id=? AND event_type='DOWNLOAD_SUCCEEDED' AND result='SUCCESS'", job.RequestID).First(&event).Error; err != nil || len(event.TrackingDigest) != 64 {
		t.Fatal("missing immutable completion evidence", err)
	}
	changed := metadata
	changed.DeviceHash = strings.Repeat("c", 64)
	if _, err := downloads.AuthorizeDownload(context.Background(), actor, job.RequestID, grant.DownloadToken, changed); err == nil {
		t.Fatal("changed device bypassed risk freeze")
	}
	var alert report.RiskAlert
	if err := db.Where("request_id=? AND status='OPEN'", job.RequestID).First(&alert).Error; err != nil || alert.RiskCode != "DOWNLOAD_DEVICE_CHANGED" {
		t.Fatal("risk did not leave a scoped alert", err)
	}
	var persistedGrant report.Grant
	if err := db.Where("public_id=?", grant.GrantID).First(&persistedGrant).Error; err != nil || persistedGrant.Status != report.GrantFrozen || persistedGrant.DownloadCount != 1 {
		t.Fatal("risk freeze/download count corrupt", err)
	}
	next, err := downloads.CreateGrant(context.Background(), actor, job.RequestID, report.GrantCommand{IdempotencyKey: fixtureID + "-rate", Metadata: metadata})
	if err != nil {
		t.Fatal("renew download grant for rate regression", err)
	}
	for i := 0; i < 4; i++ {
		content, err := downloads.AuthorizeDownload(context.Background(), actor, job.RequestID, next.DownloadToken, metadata)
		if err != nil {
			t.Fatal("permitted download before rate limit", i, err)
		}
		if _, err := io.Copy(io.Discard, content.Reader); err != nil {
			t.Fatal(err)
		}
		if err := content.Reader.Close(); err != nil {
			t.Fatal(err)
		}
		if err := content.Complete(context.Background(), true, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := downloads.AuthorizeDownload(context.Background(), actor, job.RequestID, next.DownloadToken, metadata); err == nil {
		t.Fatal("real successful-download audit failed to enforce rate limit")
	}
	var rateAlert report.RiskAlert
	if err := db.Where("request_id=? AND risk_code='DOWNLOAD_RATE_EXCEEDED'", job.RequestID).First(&rateAlert).Error; err != nil {
		t.Fatal("rate freeze evidence missing", err)
	}
	// 结构和数据行位于 root 的一次性夹具中，不触碰既有数据库。
}

type testClock struct{}

func (testClock) Now() time.Time { return time.Now().UTC() }

type testIDs struct{}

func (testIDs) NewID() string { return "qa-" + time.Now().UTC().Format("150405.000000000") }
