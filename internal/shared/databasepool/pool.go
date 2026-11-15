// Package databasepool 为不同长期进程提供独立且有界的数据库连接预算。
package databasepool

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
)

// Profile 是对应进程连接池配置的环境变量前缀。
type Profile string

const (
	API    Profile = "CRM_DB_"
	Portal Profile = "PORTAL_DB_"
	Worker Profile = "WORKER_DB_"
)

// Settings 保存经校验的连接数量及回收期限。
type Settings struct {
	MaxOpen     int
	MaxIdle     int
	MaxLifetime time.Duration
	MaxIdleTime time.Duration
}

// Load 读取当前进程的池预算，空配置采用有限默认值。
// 参数：profile 区分CRM API、客户门户和长期Worker。
// 返回值：池配置；未知进程、非法整数或非正回收期限返回错误。
func Load(profile Profile) (Settings, error) {
	cfg := Settings{MaxLifetime: 15 * time.Minute, MaxIdleTime: 2 * time.Minute}
	switch profile {
	case API:
		cfg.MaxOpen, cfg.MaxIdle = 30, 10
	case Portal:
		cfg.MaxOpen, cfg.MaxIdle = 20, 5
	case Worker:
		cfg.MaxOpen, cfg.MaxIdle = 5, 2
	default:
		return Settings{}, fmt.Errorf("unknown database pool profile")
	}
	for key, target := range map[string]*int{"MAX_OPEN_CONNS": &cfg.MaxOpen, "MAX_IDLE_CONNS": &cfg.MaxIdle} {
		if raw := strings.TrimSpace(os.Getenv(string(profile) + key)); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil {
				return Settings{}, fmt.Errorf("%s%s must be an integer", profile, key)
			}
			*target = value
		}
	}
	for key, target := range map[string]*time.Duration{"CONN_MAX_LIFETIME": &cfg.MaxLifetime, "CONN_MAX_IDLE_TIME": &cfg.MaxIdleTime} {
		if raw := strings.TrimSpace(os.Getenv(string(profile) + key)); raw != "" {
			value, err := time.ParseDuration(raw)
			if err != nil {
				return Settings{}, fmt.Errorf("%s%s must be a positive duration", profile, key)
			}
			*target = value
		}
	}
	if cfg.MaxOpen <= 0 || cfg.MaxIdle < 0 || cfg.MaxIdle > cfg.MaxOpen {
		return Settings{}, fmt.Errorf("%sMAX_OPEN_CONNS must be positive; MAX_IDLE_CONNS must be between zero and max open", profile)
	}
	if cfg.MaxLifetime <= 0 || cfg.MaxIdleTime <= 0 {
		return Settings{}, fmt.Errorf("%sCONN_MAX_LIFETIME and CONN_MAX_IDLE_TIME must be positive", profile)
	}
	return cfg, nil
}

// Open 使用调用方既有方言和GORM选项创建句柄并设置连接预算。
// 参数：dialector 保留驱动初始化行为，config 保留事务、错误及自动Ping选项，profile 为进程预算。
// 返回值：已配置池的GORM句柄；配置、驱动或底层句柄不可用时返回错误。
func Open(dialector gorm.Dialector, config *gorm.Config, profile Profile) (*gorm.DB, error) {
	cfg, err := Load(profile)
	if err != nil {
		return nil, err
	}
	db, err := gorm.Open(dialector, config)
	if err != nil {
		return nil, err
	}
	pool, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get database connection pool: %w", err)
	}
	pool.SetMaxOpenConns(cfg.MaxOpen)
	pool.SetMaxIdleConns(cfg.MaxIdle)
	pool.SetConnMaxLifetime(cfg.MaxLifetime)
	pool.SetConnMaxIdleTime(cfg.MaxIdleTime)
	return db, nil
}
