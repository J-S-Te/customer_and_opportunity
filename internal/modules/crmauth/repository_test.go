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
