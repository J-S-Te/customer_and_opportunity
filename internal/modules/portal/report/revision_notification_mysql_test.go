package report

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	drivermysql "github.com/go-sql-driver/mysql"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/migrationplan"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const revisionNotificationMigration = "000132_portal_report_notification_revisions.up.sql"

// openDisposableReportMySQL 创建本任务专用的空库，拒绝非隔离测试入口。
// 参数：t为测试上下文。
// 返回值：独立MySQL连接及仅供子进程使用的DSN；隔离检查失败时终止。
func openDisposableReportMySQL(t *testing.T) (*gorm.DB, string) {
	t.Helper()
	raw := os.Getenv("PORTAL_REPORT_TEST_DSN")
	if raw == "" {
		t.Skip("PORTAL_REPORT_TEST_DSN required; no connection attempted")
	}
	cfg, err := drivermysql.ParseDSN(raw)
	if err != nil || cfg.Net != "tcp" || !strings.HasPrefix(cfg.DBName, "qa_flow02_portal_") ||
		!(strings.HasPrefix(cfg.Addr, "127.0.0.1:") || strings.HasPrefix(cfg.Addr, "localhost:")) {
		t.Fatal("requires localhost disposable qa_flow02_portal_ schema")
	}
	cfg.ParseTime = true
	cfg.MultiStatements = false
	cfg.DBName = fmt.Sprintf("qa_flow02_portal_%d", time.Now().UnixNano())
	schema := cfg.DBName
	cfg.DBName = ""
	admin, err := gorm.Open(mysql.Open(cfg.FormatDSN()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("open isolated MySQL server failed")
	}
	if err = admin.Exec("CREATE DATABASE `" + schema + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci").Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := admin.Exec("DROP DATABASE `" + schema + "`").Error; err != nil {
			t.Error(err)
		}
		sqlDB, _ := admin.DB()
		_ = sqlDB.Close()
	})
	cfg.DBName = schema
	db, err := gorm.Open(mysql.Open(cfg.FormatDSN()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("open disposable report schema failed")
	}
	t.Cleanup(func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() })
	return db, cfg.FormatDSN()
}

// openRevisionNotificationMySQL 准备实际通知旧DDL及最小相关表结构。
// 参数：t为测试上下文，upgrade标识是否应用本次迁移。
// 返回值：隔离MySQL连接；结构准备失败时终止。
func openRevisionNotificationMySQL(t *testing.T, upgrade bool) *gorm.DB {
	t.Helper()
	db, _ := openDisposableReportMySQL(t)
	if err := db.AutoMigrate(&Request{}, &File{}, &IngestJob{}, &Grant{}, &RevisionEvent{}, &Outbox{}); err != nil {
		t.Fatal(err)
	}
	runNotificationMigration(t, db, "000038_portal_report_status_events.up.sql")
	runNotificationMigration(t, db, "000043_portal_report_issued_notifications.up.sql")
	if err := db.Exec(`CREATE TABLE portal_customer_authorities (
  tenant_id VARCHAR(64) NOT NULL, application_code VARCHAR(64) NOT NULL,
  mode VARCHAR(16) NOT NULL, first_used_at DATETIME(3) NOT NULL,
  actor_id VARCHAR(128) NOT NULL, version BIGINT UNSIGNED NOT NULL,
  PRIMARY KEY(tenant_id,application_code))`).Error; err != nil {
		t.Fatal(err)
	}
	if upgrade {
		if _, err := os.Stat("../../../../migrations/" + revisionNotificationMigration); err == nil {
			runNotificationMigration(t, db, revisionNotificationMigration)
		}
	}
	return db
}

// runNotificationMigration 以同一数据库会话执行真实迁移语句。
// 参数：t为测试上下文，db为隔离库，name为迁移文件名。
// 返回值：无；迁移解析或执行失败时终止测试。
func runNotificationMigration(t *testing.T, db *gorm.DB, name string) {
	t.Helper()
	if err := executeNotificationMigration(db, name); err != nil {
		t.Fatal(err)
	}
}

// executeNotificationMigration 执行现有解析器拆分的SQL并保留会话变量。
// 参数：db为隔离库，name为迁移文件名。
// 返回值：真实文件读取、SQL解析或数据库错误。
func executeNotificationMigration(db *gorm.DB, name string) error {
	raw, err := os.ReadFile("../../../../migrations/" + name)
	if err != nil {
		return err
	}
	statements, err := migrationplan.SplitStatements(string(raw))
	if err != nil {
		return err
	}
	return db.Connection(func(conn *gorm.DB) error {
		for _, statement := range statements {
			if err := conn.Exec(statement).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// seedRevisionRequest 创建合法报告聚合，避免绕过业务服务推进修订。
// 参数：t为测试上下文，db为隔离库，编号为申请标识，状态为测试起点。
// 返回值：已持久化申请；存储失败时终止测试。
func seedRevisionRequest(t *testing.T, db *gorm.DB, id uint64, status Status) Request {
	t.Helper()
	now := time.Date(2026, 11, 8, 2, 0, 0, 0, time.UTC)
	value := Request{ActorModel: ActorModel{ID: id, TenantID: "flow02", CreatedBy: "account", UpdatedBy: "account", CreatedAt: now, UpdatedAt: now, Version: 1}, RequestNo: fmt.Sprintf("REPORT-%d", id), ProjectID: "project", CustomerID: 9, AccountID: "account", ReportType: "FINAL", Reason: "correction regression", Status: status, DownstreamRequestID: fmt.Sprintf("external-%d", id), SubmittedAt: now, IdempotencyKey: fmt.Sprintf("request-%d", id), RequestHash: strings.Repeat("a", 64)}
	if status == StatusSubmitted {
		value.DownstreamRequestID = ""
	}
	if err := db.Create(&value).Error; err != nil {
		t.Fatal(err)
	}
	return value
}

// issueRevisionThroughService 经过真实回调与摄取事务签发一个修订。
// 参数：t为测试上下文，db为隔离库，服务及申请为业务入口，revision为修订号。
// 返回值：用于精确重放的摄取任务、文件描述和可信摄取结果。
func issueRevisionThroughService(t *testing.T, db *gorm.DB, service *Service, value Request, revision uint64) (IngestJob, FileDescriptor, IngestResult) {
	t.Helper()
	cb := Callback{TenantID: value.TenantID, CustomerID: value.CustomerID, RequestID: value.ID, ProjectID: value.ProjectID, DownstreamRequestID: value.DownstreamRequestID, Version: revision + 1, ReportRevision: revision, IdempotencyKey: fmt.Sprintf("callback-%d-%d", value.ID, revision), Status: StatusIssued, ObjectRef: fmt.Sprintf("trusted/report-%d.pdf", revision), FileName: "report.pdf", MIME: "application/pdf", Size: 1024, FileHash: strings.Repeat("a", 64), VoidReason: "更正报告"}
	if err := service.ApplyCallback(context.Background(), cb); err != nil {
		t.Fatalf("revision %d callback: %v", revision, err)
	}
	var job IngestJob
	if err := db.Where("request_id=? AND report_revision=?", value.ID, revision).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&job).Updates(map[string]any{"status": IngestProcessing, "locked_by": "worker"}).Error; err != nil {
		t.Fatal(err)
	}
	job.Status, job.LockedBy = IngestProcessing, "worker"
	descriptor := FileDescriptor{ObjectRef: cb.ObjectRef, FileName: cb.FileName, MIME: cb.MIME, Size: cb.Size, FileHash: cb.FileHash}
	evidence := IngestResult{ObjectKeyCipher: []byte("opaque"), ObjectVersion: fmt.Sprintf("version-%d", revision), EncryptionKeyRef: "key", EncryptionAlgorithm: "AES-256-GCM", ScanStatus: "CLEAN", ScanReference: "scan", ScannedAt: time.Date(2026, 11, 8, 1, 59, 0, 0, time.UTC), WatermarkStatus: "DOWNLOAD_TIME"}
	if err := service.CompleteIngest(context.Background(), job, descriptor, evidence); err != nil {
		t.Fatalf("revision %d publication must succeed: %v", revision, err)
	}
	return job, descriptor, evidence
}

// TestReportCorrectionNotificationOnMySQL 验证首版已读后更正版仍能发布且重放不重复。
// 参数：t为测试上下文。
// 返回值：无；更正发布、旧版失效或已读证据不符合约定时失败。
func TestReportCorrectionNotificationOnMySQL(t *testing.T) {
	db := openRevisionNotificationMySQL(t, true)
	value := seedRevisionRequest(t, db, 1, StatusApprovedProcessing)
	service := NewService(NewGORMRepository(db), nil, nil, &ingestorStub{}, reportClock{now: time.Date(2026, 11, 8, 2, 1, 0, 0, time.UTC)}, nil)
	issueRevisionThroughService(t, db, service, value, 0)
	var original Notification
	if err := db.First(&original).Error; err != nil {
		t.Fatal(err)
	}
	actor := Actor{TenantID: value.TenantID, CustomerID: value.CustomerID, AccountID: value.AccountID}
	if err := service.ReadNotification(context.Background(), actor, original.ID); err != nil {
		t.Fatal(err)
	}
	var oldRead NotificationReadEvent
	if err := db.First(&oldRead).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&original, original.ID).Error; err != nil {
		t.Fatal(err)
	}
	grant := Grant{ActorModel: ActorModel{TenantID: value.TenantID, CreatedBy: "account", UpdatedBy: "account", Version: 1}, PublicID: "grant", CustomerID: value.CustomerID, RequestID: value.ID, AccountID: value.AccountID, TokenHash: "token", IssueKeyHash: "issue", Status: GrantActive, ExpiresAt: time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)}
	if err := db.Create(&grant).Error; err != nil {
		t.Fatal(err)
	}
	for revision := uint64(1); revision <= 2; revision++ {
		job, descriptor, evidence := issueRevisionThroughService(t, db, service, value, revision)
		for retry := 0; retry < 3; retry++ {
			if err := service.CompleteIngest(context.Background(), job, descriptor, evidence); err != nil {
				t.Fatalf("replay: %v", err)
			}
		}
	}
	var current Request
	if err := db.First(&current, value.ID).Error; err != nil {
		t.Fatal(err)
	}
	if current.Status != StatusIssued || current.CurrentReportRevision != 2 || current.ReportValidityStatus != "ACTIVE" {
		t.Fatalf("published request: %+v", current)
	}
	var files []File
	if err := db.Order("report_revision").Find(&files).Error; err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 || files[0].ValidityStatus != "VOID" || files[1].ValidityStatus != "VOID" || files[2].ValidityStatus != "ACTIVE" {
		t.Fatalf("files: %+v", files)
	}
	if err := db.First(&grant, grant.ID).Error; err != nil {
		t.Fatal(err)
	}
	if grant.Status != GrantRevoked {
		t.Fatalf("superseded grant: %s", grant.Status)
	}
	var notices []Notification
	if err := db.Order("id").Find(&notices).Error; err != nil {
		t.Fatal(err)
	}
	if len(notices) != 3 || notices[0].ReportRevision != 0 || notices[1].ReportRevision != 1 || notices[2].ReportRevision != 2 || notices[0].ID != original.ID || notices[0].Status != NotificationRead || notices[0].ReadAt == nil || !notices[0].ReadAt.Equal(*original.ReadAt) {
		t.Fatalf("notices: %+v", notices)
	}
	var reads []NotificationReadEvent
	if err := db.Find(&reads).Error; err != nil {
		t.Fatal(err)
	}
	if len(reads) != 1 || reads[0].ID != oldRead.ID || reads[0].NotificationID != original.ID {
		t.Fatalf("read audit: %+v", reads)
	}
	page, err := service.ListNotifications(context.Background(), actor, false, 1, 10)
	if err != nil || page.Total != 3 || len(page.Items) != 3 || page.Items[0].ReportRevision != 2 || page.Items[2].ReportRevision != 0 {
		t.Fatalf("notification view=%+v err=%v", page, err)
	}
	unread, err := service.UnreadNotificationCount(context.Background(), actor)
	if err != nil || unread != 2 {
		t.Fatalf("unread=%d err=%v", unread, err)
	}
}

// TestSubmissionFailureNotificationOnMySQL 验证真实通知约束接受投递失败且多次重放幂等。
// 参数：t为测试上下文。
// 返回值：无；通知约束或终态幂等不符合业务语义时失败。
func TestSubmissionFailureNotificationOnMySQL(t *testing.T) {
	db := openRevisionNotificationMySQL(t, true)
	value := seedRevisionRequest(t, db, 2, StatusSubmitted)
	event := Outbox{EventID: "delivery", TenantID: value.TenantID, AggregateID: value.ID, EventType: OutboxEventSubmitted, Payload: []byte("{}"), Status: OutboxProcessing, LockedBy: "worker", CreatedAt: time.Now().UTC()}
	if err := db.Create(&event).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(NewGORMRepository(db), nil, nil, nil, reportClock{now: time.Now()}, nil)
	for attempt := 0; attempt < 3; attempt++ {
		if err := service.MarkDeliveryDeadLetter(context.Background(), event); err != nil {
			t.Fatalf("delivery dead letter: %v", err)
		}
	}
	var notices []Notification
	if err := db.Find(&notices).Error; err != nil {
		t.Fatal(err)
	}
	if len(notices) != 1 || notices[0].Kind != NotificationKindSubmissionFailed || notices[0].ReportRevision != 0 {
		t.Fatalf("notices: %+v", notices)
	}
}

// seedLegacyNotice 保存与真实旧实现相同时间戳的文件和已读通知。
// 参数：t为测试上下文，db为隔离库，value为申请，revision为原签发修订，issued为签发时间。
// 返回值：旧通知和已读事件；写入失败时终止测试。
func seedLegacyNotice(t *testing.T, db *gorm.DB, value Request, revision uint64, issued time.Time) (Notification, NotificationReadEvent) {
	t.Helper()
	file := File{RequestID: value.ID, ReportRevision: revision, ValidityStatus: "VOID", ObjectKeyCipher: []byte("old-file"), ObjectVersion: "old", FileName: "old.pdf", MIME: "application/pdf", FileHash: strings.Repeat("a", 64), EncryptionKeyRef: "key", EncryptionAlgorithm: "AES-256-GCM", ScanStatus: "CLEAN", ScanReference: "scan", WatermarkStatus: "DOWNLOAD_TIME"}
	file.TenantID, file.CreatedBy, file.UpdatedBy = value.TenantID, "old-ingestor", "old-ingestor"
	file.CreatedAt, file.UpdatedAt, file.Version = issued, issued, 1
	if err := db.Create(&file).Error; err != nil {
		t.Fatal(err)
	}
	notice := Notification{TenantID: value.TenantID, CustomerID: value.CustomerID, RequestID: value.ID, AccountID: value.AccountID, Kind: NotificationKindIssued, Status: NotificationRead, CreatedAt: issued, ReadAt: &issued}
	// 使用旧表真实列，新增模型字段不得改变历史夹具。
	if err := db.Table("portal_report_notifications").Create(map[string]any{"tenant_id": notice.TenantID, "customer_id": notice.CustomerID, "request_id": notice.RequestID, "account_id": notice.AccountID, "kind": notice.Kind, "status": notice.Status, "created_at": notice.CreatedAt, "read_at": notice.ReadAt}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("request_id=?", value.ID).Take(&notice).Error; err != nil {
		t.Fatal(err)
	}
	read := NotificationReadEvent{TenantID: notice.TenantID, CustomerID: notice.CustomerID, RequestID: notice.RequestID, AccountID: notice.AccountID, NotificationID: notice.ID, RequestTrace: "original-trace", OccurredAt: issued}
	if err := db.Create(&read).Error; err != nil {
		t.Fatal(err)
	}
	return notice, read
}

// TestLegacyNotificationRevisionMigrationOnMySQL 验证迁移按原文件证据保留当前与旧版通知。
// 参数：t为测试上下文。
// 返回值：无；误用当前修订、改写已读证据或无法继续发布时失败。
func TestLegacyNotificationRevisionMigrationOnMySQL(t *testing.T) {
	db := openRevisionNotificationMySQL(t, false)
	originalTime := time.Date(2026, 11, 8, 1, 0, 0, 0, time.UTC)
	previous := seedRevisionRequest(t, db, 10, StatusIngestPending)
	current := seedRevisionRequest(t, db, 11, StatusIssued)
	if err := db.Model(&previous).Updates(map[string]any{"current_report_revision": 5, "report_validity_status": "VOID"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&current).Update("current_report_revision", 7).Error; err != nil {
		t.Fatal(err)
	}
	previousNotice, previousRead := seedLegacyNotice(t, db, previous, 4, originalTime)
	currentNotice, _ := seedLegacyNotice(t, db, current, 7, originalTime.Add(time.Hour))
	runNotificationMigration(t, db, revisionNotificationMigration)
	var notices []struct {
		ID             uint64
		ReportRevision uint64
		Status         string
		ReadAt         *time.Time
	}
	if err := db.Table("portal_report_notifications").Order("id").Find(&notices).Error; err != nil {
		t.Fatal(err)
	}
	if len(notices) != 2 || notices[0].ID != previousNotice.ID || notices[0].ReportRevision != 4 || notices[1].ID != currentNotice.ID || notices[1].ReportRevision != 7 || notices[0].Status != NotificationRead || notices[0].ReadAt == nil || !notices[0].ReadAt.Equal(*previousNotice.ReadAt) {
		t.Fatalf("migrated notices=%+v", notices)
	}
	var read NotificationReadEvent
	if err := db.First(&read, previousRead.ID).Error; err != nil {
		t.Fatal(err)
	}
	if read.NotificationID != previousNotice.ID || read.RequestTrace != "original-trace" || !read.OccurredAt.Equal(previousRead.OccurredAt) {
		t.Fatalf("changed original read=%+v", read)
	}
	// 错把旧通知归到聚合当前修订5，会让这里撞键。
	next := Notification{TenantID: previous.TenantID, CustomerID: previous.CustomerID, RequestID: previous.ID, AccountID: previous.AccountID, Kind: NotificationKindIssued, Status: NotificationUnread, CreatedAt: originalTime.Add(2 * time.Hour)}
	if err := db.Table("portal_report_notifications").Create(map[string]any{"tenant_id": next.TenantID, "customer_id": next.CustomerID, "request_id": next.RequestID, "account_id": next.AccountID, "kind": next.Kind, "status": next.Status, "created_at": next.CreatedAt, "report_revision": 5}).Error; err != nil {
		t.Fatalf("publish next revision after legacy backfill: %v", err)
	}
	if err := db.Table("portal_report_notifications").Create(map[string]any{"tenant_id": next.TenantID, "customer_id": next.CustomerID, "request_id": next.RequestID, "account_id": next.AccountID, "kind": next.Kind, "status": next.Status, "created_at": next.CreatedAt, "report_revision": 5}).Error; err == nil {
		t.Fatal("same revision duplicate must still conflict")
	}
}

// TestLegacyNotificationMigrationRejectsUnprovenRevisionOnMySQL 验证缺失或歧义证据时拒绝猜测。
// 参数：t为测试上下文。
// 返回值：无；迁移放行未知修订或损坏旧通知时失败。
func TestLegacyNotificationMigrationRejectsUnprovenRevisionOnMySQL(t *testing.T) {
	for _, scenario := range []string{"missing", "ambiguous"} {
		t.Run(scenario, func(t *testing.T) {
			db := openRevisionNotificationMySQL(t, false)
			value := seedRevisionRequest(t, db, 20, StatusIssued)
			issued := time.Date(2026, 11, 8, 1, 0, 0, 0, time.UTC)
			notice, _ := seedLegacyNotice(t, db, value, 3, issued)
			if scenario == "missing" {
				if err := db.Unscoped().Where("request_id=?", value.ID).Delete(&File{}).Error; err != nil {
					t.Fatal(err)
				}
			} else {
				var file File
				if err := db.First(&file).Error; err != nil {
					t.Fatal(err)
				}
				file.ID, file.ReportRevision = 0, 4
				if err := db.Create(&file).Error; err != nil {
					t.Fatal(err)
				}
			}
			err := executeNotificationMigration(db, revisionNotificationMigration)
			if err == nil {
				t.Fatal("migration must reject unproven report revision")
			}
			var mysqlErr *drivermysql.MySQLError
			if !errors.As(err, &mysqlErr) {
				t.Fatalf("expected explicit database evidence rejection, got %v", err)
			}
			if db.Migrator().HasColumn(&Notification{}, "report_revision") {
				t.Fatal("unproven legacy revision must be rejected before persistent DDL")
			}
			var retained Notification
			if err := db.First(&retained, notice.ID).Error; err != nil {
				t.Fatal(err)
			}
			if retained.ID != notice.ID || retained.Status != NotificationRead || retained.ReadAt == nil || !retained.ReadAt.Equal(*notice.ReadAt) {
				t.Fatalf("old notice changed=%+v", retained)
			}
		})
	}
}

// TestPortalMigrationRunnerReplayOnMySQL 验证真实迁移执行器二次运行只跳过账本记录。
// 参数：t为测试上下文。
// 返回值：无；首次迁移失败、二次写入DDL或新迁移未登记时失败。
func TestPortalMigrationRunnerReplayOnMySQL(t *testing.T) {
	db, dsn := openDisposableReportMySQL(t)
	root, err := filepath.Abs("../../../..")
	if err != nil {
		t.Fatal(err)
	}
	run := func() string {
		cmd := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "run", "./cmd/production-migrate", "-schema", "portal", "-dir", "migrations")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "PORTAL_MYSQL_DSN="+dsn, "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off")
		output, err := cmd.CombinedOutput()
		t.Log(string(output))
		if err != nil {
			t.Fatalf("real migration runner failed: %v", err)
		}
		return string(output)
	}
	first := run()
	if !strings.Contains(first, "apply "+revisionNotificationMigration) {
		t.Fatal("new notification migration was not applied")
	}
	var before, after int64
	if err := db.Table("app_schema_migration_statements").Count(&before).Error; err != nil {
		t.Fatal(err)
	}
	second := run()
	if strings.Contains(second, "apply ") || !strings.Contains(second, "skip "+revisionNotificationMigration) {
		t.Fatal("runner replay must only skip applied migration checkpoints")
	}
	if err := db.Table("app_schema_migration_statements").Count(&after).Error; err != nil {
		t.Fatal(err)
	}
	if before == 0 || before != after {
		t.Fatalf("checkpoint count before=%d after=%d", before, after)
	}
}
