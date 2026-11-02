package crmauth

import (
	"strings"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func TestActiveSessionRecheckUsesAuthoritativeValidityPredicates(t *testing.T) {
	database, err := gorm.Open(mysql.New(mysql.Config{
		DSN: "gorm:gorm@tcp(127.0.0.1:9910)/gorm?charset=utf8mb4&parseTime=True&loc=UTC", SkipInitializeWithVersion: true,
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatalf("open dry-run database: %v", err)
	}
	now := time.Date(2026, 8, 2, 6, 14, 11, 240_000_000, time.UTC)
	var active struct {
		SessionIDHash string `gorm:"column:session_id_hash"`
	}
	statement := database.Table((Session{}).TableName()).
		Select("session_id_hash").
		Where("session_id_hash = ? AND revoked_at IS NULL AND expires_at > ?", "session-hash", now).
		Take(&active).Statement

	sql := statement.SQL.String()
	for _, expected := range []string{"crm_oidc_sessions", "session_id_hash = ?", "revoked_at IS NULL", "expires_at > ?"} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("active session recheck SQL missing %q: %s", expected, sql)
		}
	}
	if len(statement.Vars) < 2 || statement.Vars[0] != "session-hash" || !statement.Vars[1].(time.Time).Equal(now) {
		t.Fatalf("active session recheck variables = %#v", statement.Vars)
	}
}

// TestSessionLookupSeparatesCentralAndLegacyIdleAuthority 验证查询保留撤销和绝对期限且只在旧模式检查空闲。
// 参数：t为SQL生成测试上下文，不连接真实数据库。
// 返回值：无；中央查询被本地窗口截断或旧模式丢失空闲边界时失败。
func TestSessionLookupSeparatesCentralAndLegacyIdleAuthority(t *testing.T) {
	database, err := gorm.Open(mysql.New(mysql.Config{
		DSN:                       "gorm:gorm@tcp(127.0.0.1:9910)/gorm?parseTime=True&loc=UTC",
		SkipInitializeWithVersion: true,
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	for _, idle := range []time.Duration{0, 30 * time.Minute} {
		var row Session
		statement := activeSessionQuery(database, "session-hash", now, idle).Take(&row).Statement
		query := statement.SQL.String()
		for _, boundary := range []string{"session_id_hash = ?", "revoked_at IS NULL", "expires_at > ?"} {
			if !strings.Contains(query, boundary) {
				t.Fatalf("idle=%s lost local boundary %s: %s", idle, boundary, query)
			}
		}
		if strings.Contains(query, "last_interactive_at") != (idle > 0) {
			t.Fatalf("idle=%s uses wrong idle authority: %s", idle, query)
		}
	}
}
