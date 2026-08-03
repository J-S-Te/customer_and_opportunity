// Package workerruntime 持久化独立部署的 Portal worker 存活证据。
// 仅有服务端配置不能证明 worker 正在消费任务，因此新任务准入依赖最近心跳。
package workerruntime

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	ReportDeliveryWorker = "portal_report_delivery"
	ProjectExportWorker  = "portal_project_export"
	HeartbeatInterval    = 5 * time.Second
	HeartbeatMaxAge      = 30 * time.Second
)

var (
	ErrInvalidIdentity = errors.New("invalid Portal worker heartbeat identity")
	ErrIncarnationLost = errors.New("Portal worker heartbeat incarnation was replaced")
)

type Heartbeat struct {
	ID         uint64    `gorm:"primaryKey;autoIncrement"`
	WorkerType string    `gorm:"size:64;not null;uniqueIndex:uq_portal_worker_heartbeat"`
	InstanceID string    `gorm:"size:128;not null;uniqueIndex:uq_portal_worker_heartbeat"`
	StartedAt  time.Time `gorm:"precision:3;not null"`
	LastSeenAt time.Time `gorm:"precision:3;not null;index"`
	CreatedAt  time.Time `gorm:"precision:3;not null"`
	UpdatedAt  time.Time `gorm:"precision:3;not null"`
}

func (Heartbeat) TableName() string { return "portal_worker_heartbeats" }

type Readiness interface {
	HasFreshHeartbeat(context.Context, string, time.Time) (bool, error)
}

type Repository struct{ db *gorm.DB }

func NewRepository(db *gorm.DB) *Repository { return &Repository{db: db} }

func (r *Repository) HasFreshHeartbeat(ctx context.Context, workerType string, notBefore time.Time) (bool, error) {
	// 只要任一该类型实例心跳新鲜即可准入；实例明细不暴露给业务服务。
	if r == nil || r.db == nil || !validWorkerType(workerType) || notBefore.IsZero() {
		return false, ErrInvalidIdentity
	}
	var count int64
	err := database.FromContext(ctx, r.db).Model(&Heartbeat{}).
		Where("worker_type=? AND last_seen_at>=?", strings.TrimSpace(workerType), notBefore.UTC()).
		Limit(1).Count(&count).Error
	return count > 0, err
}

func (r *Repository) Start(ctx context.Context, workerType, instanceID string, startedAt time.Time) error {
	// 同一实例重启以 started_at 区分 incarnation，旧进程之后不能刷新新进程的心跳。
	workerType, instanceID = strings.TrimSpace(workerType), strings.TrimSpace(instanceID)
	if r == nil || r.db == nil || !validWorkerType(workerType) || instanceID == "" || len(instanceID) > 128 || startedAt.IsZero() {
		return ErrInvalidIdentity
	}
	startedAt = canonicalStartedAt(startedAt)
	value := Heartbeat{WorkerType: workerType, InstanceID: instanceID, StartedAt: startedAt, LastSeenAt: startedAt}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "worker_type"}, {Name: "instance_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"started_at": value.StartedAt, "last_seen_at": value.LastSeenAt, "updated_at": value.LastSeenAt,
		}),
	}).Create(&value).Error
}

func (r *Repository) Refresh(ctx context.Context, workerType, instanceID string, startedAt, now time.Time) error {
	// 更新条件包含规范化后的启动时间；零更新意味着实例身份已被替换，应停止继续声明存活。
	workerType, instanceID = strings.TrimSpace(workerType), strings.TrimSpace(instanceID)
	if r == nil || r.db == nil || !validWorkerType(workerType) || instanceID == "" || len(instanceID) > 128 || startedAt.IsZero() || now.IsZero() {
		return ErrInvalidIdentity
	}
	startedAt = canonicalStartedAt(startedAt)
	result := r.db.WithContext(ctx).Model(&Heartbeat{}).
		Where("worker_type=? AND instance_id=? AND started_at=?", workerType, instanceID, startedAt).
		Updates(map[string]any{"last_seen_at": now.UTC(), "updated_at": now.UTC()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrIncarnationLost
	}
	return nil
}

func (r *Repository) Remove(ctx context.Context, workerType, instanceID string, startedAt time.Time) error {
	workerType, instanceID = strings.TrimSpace(workerType), strings.TrimSpace(instanceID)
	if r == nil || r.db == nil || !validWorkerType(workerType) || instanceID == "" || len(instanceID) > 128 || startedAt.IsZero() {
		return ErrInvalidIdentity
	}
	result := r.db.WithContext(ctx).Where("worker_type=? AND instance_id=? AND started_at=?", workerType, instanceID, canonicalStartedAt(startedAt)).Delete(&Heartbeat{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrIncarnationLost
	}
	return nil
}

func validWorkerType(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= 64
}

func canonicalStartedAt(value time.Time) time.Time {
	return value.UTC().Truncate(time.Millisecond)
}

// RefreshLoop 周期刷新已创建的心跳。刷新失败会上报但不阻塞 worker 的处理循环；证据自然过期后准入自动关闭。
func RefreshLoop(ctx context.Context, repo *Repository, workerType, instanceID string, startedAt time.Time, onError func(error)) {
	ticker := time.NewTicker(HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if err := repo.Refresh(ctx, workerType, instanceID, startedAt, now.UTC()); err != nil {
				if onError != nil && !errors.Is(err, context.Canceled) {
					onError(err)
				}
				if errors.Is(err, ErrIncarnationLost) {
					return
				}
			}
		}
	}
}
