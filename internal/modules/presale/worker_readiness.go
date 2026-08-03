package presale

import (
	"context"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"gorm.io/gorm"
)

const PresaleDeliveryWorkerType = "presale_delivery"

// WorkerHeartbeat 是独立部署 worker 写入的活性证据；仅有主进程配置开关不能证明投递链路可用。
type WorkerHeartbeat struct {
	ID          uint64    `gorm:"primaryKey;autoIncrement"`
	WorkerType  string    `gorm:"size:64;not null;uniqueIndex:uq_crm_worker_heartbeat"`
	WorkerID    string    `gorm:"size:128;not null;uniqueIndex:uq_crm_worker_heartbeat"`
	HeartbeatAt time.Time `gorm:"precision:3;not null;index"`
	CreatedAt   time.Time `gorm:"precision:3;not null"`
	UpdatedAt   time.Time `gorm:"precision:3;not null"`
}

func (WorkerHeartbeat) TableName() string { return "crm_worker_heartbeats" }

// 领域层只依赖这一最小准入接口：任一同类型实例存在新鲜持久化心跳才算可用，
// 查询错误不得解释为可用。
type WorkerReadiness interface {
	HasFreshHeartbeat(ctx context.Context, workerType string, notBefore time.Time) (bool, error)
}

type GORMWorkerReadinessRepository struct{ db *gorm.DB }

func NewGORMWorkerReadinessRepository(db *gorm.DB) *GORMWorkerReadinessRepository {
	return &GORMWorkerReadinessRepository{db: db}
}

func (r *GORMWorkerReadinessRepository) HasFreshHeartbeat(ctx context.Context, workerType string, notBefore time.Time) (bool, error) {
	if r == nil || r.db == nil || strings.TrimSpace(workerType) == "" || notBefore.IsZero() {
		return false, ErrDependencyUnavailable
	}
	var count int64
	err := database.FromContext(ctx, r.db).Model(&WorkerHeartbeat{}).
		Where("worker_type = ? AND heartbeat_at >= ?", workerType, notBefore.UTC()).
		Limit(1).Count(&count).Error
	return count > 0, err
}
