package credit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	drivermysql "github.com/go-sql-driver/mysql"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// financeCreditFixture 创建当前任务隔离数据库中的合成信用客户与正式服务。
// 参数：t为测试上下文，customerID为合成客户主键。
// 返回值：数据库、服务与可信租户；缺少显式测试DSN时跳过。
func financeCreditFixture(t *testing.T, customerID uint64) (*gorm.DB, *Service, string) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("CRM_TEST_DSN"))
	if dsn == "" {
		t.Skip("CRM_TEST_DSN is required")
	}
	cfg, err := drivermysql.ParseDSN(dsn)
	if err != nil || cfg.Net != "tcp" || !strings.HasPrefix(cfg.Addr, "127.0.0.1:") || !strings.HasPrefix(cfg.DBName, "crm_test_") {
		t.Fatal("CRM_TEST_DSN must identify explicit loopback crm_test_* schema (DSN withheld)")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("open isolated CRM database failed (DSN withheld)")
	}
	err = db.Exec(`CREATE TABLE IF NOT EXISTS crm_customers (
 id BIGINT UNSIGNED NOT NULL PRIMARY KEY, tenant_id VARCHAR(64) NOT NULL,
 status VARCHAR(16) NOT NULL, merged_into_id BIGINT UNSIGNED NULL, deleted_at DATETIME(3) NULL,
 credit_level CHAR(1) NOT NULL, credit_updated_at DATETIME(3) NULL,
 credit_change_source VARCHAR(16) NOT NULL DEFAULT 'INITIAL',
 consecutive_ontime_count INT UNSIGNED NOT NULL DEFAULT 0,
 consecutive_late_count INT UNSIGNED NOT NULL DEFAULT 0,
 last_payment_eval_at DATETIME(3) NULL, owner_user_id VARCHAR(64) NOT NULL DEFAULT ''
 ) ENGINE=InnoDB`).Error
	if err != nil {
		t.Fatal(err)
	}
	err = db.Exec(`CREATE TABLE IF NOT EXISTS crm_customer_change_logs (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY, tenant_id VARCHAR(64) NOT NULL,
 customer_id BIGINT UNSIGNED NOT NULL, field_name VARCHAR(64) NOT NULL,
 before_json TEXT NOT NULL, after_json TEXT NOT NULL, reason VARCHAR(500) NOT NULL,
 operator_id VARCHAR(128) NOT NULL, request_id VARCHAR(128) NOT NULL, occurred_at DATETIME(3) NOT NULL
 ) ENGINE=InnoDB`).Error
	if err != nil {
		t.Fatal(err)
	}
	if err = db.AutoMigrate(&creditPaymentRecord{}, &CreditLog{}, &RuleSettings{}); err != nil {
		t.Fatal(err)
	}
	tenant := fmt.Sprintf("finance-%d", time.Now().UnixNano())
	if err = db.Exec(`INSERT INTO crm_customers(id,tenant_id,status,credit_level) VALUES(?,?,'ACTIVE','A')`, customerID, tenant).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, table := range []string{"crm_customer_credit_payment_records", "crm_customer_credit_logs", "crm_credit_rule_settings", "crm_customer_change_logs", "crm_customers"} {
			if err := db.Table(table).Where("tenant_id=?", tenant).Delete(map[string]any{}).Error; err != nil {
				t.Error(err)
			}
		}
	})
	return db, NewService(db), tenant
}

// TestSettlementPaymentWireActualHandler 验证结算真实Publish字节经CRM严格Handler消费及重放。
// 参数：t为测试上下文，环境变量指定结算生成的原始及规范化报文文件。
// 返回值：契约、权限、租户隔离、幂等或领域状态不符时失败。
func TestSettlementPaymentWireActualHandler(t *testing.T) {
	path := os.Getenv("CRM_CREDIT_WIRE_FILE")
	if path == "" {
		t.Skip("CRM_CREDIT_WIRE_FILE is required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct{ Legacy, Body json.RawMessage }
	if err = json.Unmarshal(raw, &rows); err != nil || len(rows) != 2 {
		t.Fatalf("invalid Settlement wire fixture: %v", err)
	}
	db, svc, tenant := financeCreditFixture(t, 42)
	router := gin.New()
	principal := auth.Principal{TenantID: tenant, UserID: "machine", Permissions: map[string]struct{}{"customer.credit.payment.ingest": {}}}
	router.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), principal))
	})
	router.POST("/payment", NewHandler(svc).Payment)
	for _, row := range rows {
		for _, body := range []json.RawMessage{row.Legacy,
			append(append(json.RawMessage(nil), row.Body[:len(row.Body)-1]...), []byte(`,"unknown":true}`)...),
			append(append(json.RawMessage(nil), row.Body[:len(row.Body)-1]...), []byte(`,"payment_id":"duplicate"}`)...),
		} {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/payment", bytes.NewReader(body))
			router.ServeHTTP(recorder, request)
			if recorder.Code != 400 {
				t.Fatalf("strict Handler accepted unsupported fields: %d %s", recorder.Code, recorder.Body.String())
			}
		}
		for replay := 0; replay < 2; replay++ {
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/payment", bytes.NewReader(row.Body)))
			if recorder.Code != 200 {
				t.Fatalf("actual Handler rejected Settlement wire: %d %s", recorder.Code, recorder.Body.String())
			}
		}
	}
	var records []creditPaymentRecord
	if err = db.Where("tenant_id=?", tenant).Order("id").Find(&records).Error; err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].Evaluation != "ONTIME" || records[0].ReversedAt == nil || records[1].Evaluation != "REVERSED" {
		t.Fatalf("trusted tenant payment effects incorrect: %+v", records)
	}
	principal.Permissions = nil
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/payment", bytes.NewReader(rows[0].Body)))
	if recorder.Code != 403 {
		t.Fatalf("missing permission status=%d", recorder.Code)
	}
}

// TestSettlementIgnoredPaymentWireActualHandler 验证旧出站报文经过忽略、补偿与新结清实际Handler链。
// 参数：t为测试上下文，跨仓驱动提供结算实际Publish字节。
// 返回值：规则切换后补偿返回冲突、新结清阻塞或信用计数不符时失败。
func TestSettlementIgnoredPaymentWireActualHandler(t *testing.T) {
	path := os.Getenv("CRM_CREDIT_WIRE_FILE")
	if path == "" {
		t.Skip("CRM_CREDIT_WIRE_FILE is required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct{ Legacy, Body json.RawMessage }
	if err = json.Unmarshal(raw, &rows); err != nil || len(rows) != 2 {
		t.Fatalf("invalid wire: %v", err)
	}
	db, svc, tenant := financeCreditFixture(t, 42)
	rules := RuleSettings{TenantID: tenant, GraceDays: 7, OnTimeThreshold: 2, LateThreshold: 2, LevelStep: 1, Enabled: false, UpdatedAt: time.Now().UTC()}
	if err = db.Create(&rules).Error; err != nil {
		t.Fatal(err)
	}
	principal := auth.Principal{TenantID: tenant, UserID: "machine", Permissions: map[string]struct{}{"customer.credit.payment.ingest": {}}}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), principal))
	})
	router.POST("/payment", NewHandler(svc).Payment)
	for i, row := range rows {
		for replay := 0; replay < 2; replay++ {
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/payment", bytes.NewReader(row.Body)))
			if recorder.Code != 200 {
				t.Fatalf("ignored completion/reversal rejected: %d %s", recorder.Code, recorder.Body.String())
			}
		}
		if i == 0 {
			if err = db.Model(&RuleSettings{}).Where("tenant_id=?", tenant).Update("enabled", true).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	var original creditPaymentRecord
	if err = db.Where("tenant_id=? AND payment_id=?", tenant, "allocation-1").Take(&original).Error; err != nil {
		t.Fatal(err)
	}
	if original.Evaluation != "IGNORED_RULE_DISABLED" || original.ReversedAt == nil {
		t.Fatalf("ignored original not compensated: %+v", original)
	}
	var next map[string]json.RawMessage
	if err = json.Unmarshal(rows[0].Body, &next); err != nil {
		t.Fatal(err)
	}
	next["event_id"] = json.RawMessage(`"wire-completed-2"`)
	next["payment_id"] = json.RawMessage(`"allocation-2"`)
	body, err := json.Marshal(next)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/payment", bytes.NewReader(body)))
	if recorder.Code != 200 {
		t.Fatalf("new completion blocked: %d %s", recorder.Code, recorder.Body.String())
	}
	var count int
	if err = db.Raw(`SELECT consecutive_ontime_count FROM crm_customers WHERE tenant_id=?`, tenant).Scan(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("new completion count=%d", count)
	}
}
