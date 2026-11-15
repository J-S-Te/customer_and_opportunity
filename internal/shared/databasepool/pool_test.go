package databasepool

import (
	"database/sql"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// TestProfileDefaults 核验API、门户及后台任务使用不同的有限默认预算。
// 参数：t 为测试上下文。
// 返回值：无；预算或生命周期不符合约定时失败。
func TestProfileDefaults(t *testing.T) {
	for _, tc := range []struct {
		profile    Profile
		open, idle int
	}{
		{API, 30, 10}, {Portal, 20, 5}, {Worker, 5, 2},
	} {
		clearPoolEnv(t, tc.profile)
		cfg, err := Load(tc.profile)
		if err != nil || cfg.MaxOpen != tc.open || cfg.MaxIdle != tc.idle || cfg.MaxLifetime != 15*time.Minute || cfg.MaxIdleTime != 2*time.Minute {
			t.Fatalf("profile %s: cfg=%+v err=%v", tc.profile, cfg, err)
		}
	}
}

// TestOverridesAndValidation 核验显式池预算生效且非法设置失败关闭。
// 参数：t 为测试上下文。
// 返回值：无；允许无限连接或静默忽略无效配置时失败。
func TestOverridesAndValidation(t *testing.T) {
	clearPoolEnv(t, Worker)
	t.Setenv(string(Worker)+"MAX_OPEN_CONNS", "8")
	t.Setenv(string(Worker)+"MAX_IDLE_CONNS", "0")
	t.Setenv(string(Worker)+"CONN_MAX_LIFETIME", "10m")
	t.Setenv(string(Worker)+"CONN_MAX_IDLE_TIME", "45s")
	cfg, err := Load(Worker)
	if err != nil || cfg.MaxOpen != 8 || cfg.MaxIdle != 0 || cfg.MaxLifetime != 10*time.Minute || cfg.MaxIdleTime != 45*time.Second {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
	for _, tc := range []struct{ key, value string }{
		{"MAX_OPEN_CONNS", "0"}, {"MAX_OPEN_CONNS", "-1"}, {"MAX_OPEN_CONNS", "invalid"},
		{"MAX_IDLE_CONNS", "-1"}, {"MAX_IDLE_CONNS", "9"}, {"CONN_MAX_LIFETIME", "0s"},
		{"CONN_MAX_IDLE_TIME", "-1s"}, {"CONN_MAX_IDLE_TIME", "garbage"},
	} {
		t.Run(tc.key+"/"+tc.value, func(t *testing.T) {
			t.Setenv(string(Worker)+tc.key, tc.value)
			if _, err := Load(Worker); err == nil {
				t.Fatal("accepted invalid pool setting")
			}
		})
	}
}

// TestOpenPreservesGORMConfiguration 核验池限额作用真实SQL句柄且不替换既有GORM选项。
// 参数：t 为测试上下文。
// 返回值：无；本测试不建立网络连接。
func TestOpenPreservesGORMConfiguration(t *testing.T) {
	clearPoolEnv(t, Worker)
	raw, err := sql.Open("mysql", "test:test@tcp(127.0.0.1:1)/unused")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	db, err := Open(mysql.New(mysql.Config{Conn: raw, SkipInitializeWithVersion: true}),
		&gorm.Config{DisableAutomaticPing: true, SkipDefaultTransaction: true, TranslateError: true}, Worker)
	if err != nil {
		t.Fatal(err)
	}
	if raw.Stats().MaxOpenConnections != 5 || !db.SkipDefaultTransaction || !db.TranslateError || !db.DisableAutomaticPing {
		t.Fatalf("pool/options changed: max=%d", raw.Stats().MaxOpenConnections)
	}
	t.Setenv(string(Worker)+"MAX_OPEN_CONNS", "0")
	if _, err := Open(nil, &gorm.Config{}, Worker); err == nil {
		t.Fatal("invalid settings must fail before dialing")
	}
}

// clearPoolEnv 隔离开发机已有连接池环境变量。
// 参数：t 为测试上下文，profile 为当前进程预算前缀。
// 返回值：无；测试结束后自动恢复原环境。
func clearPoolEnv(t *testing.T, profile Profile) {
	t.Helper()
	for _, key := range []string{"MAX_OPEN_CONNS", "MAX_IDLE_CONNS", "CONN_MAX_LIFETIME", "CONN_MAX_IDLE_TIME"} {
		t.Setenv(string(profile)+key, "")
	}
}
