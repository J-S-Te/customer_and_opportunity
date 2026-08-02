package presale

import (
	"context"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"gorm.io/gorm"
)

const PresaleDeliveryWorkerType = "presale_delivery"

// WorkerHeartbeat is liveness evidence written by an independently deployed
// worker. A main-process configuration flag is deliberately not sufficient to
// establish delivery readiness.
type WorkerHeartbeat struct {
	ID          uint64    `gorm:"primaryKey;autoIncrement"`
	WorkerType  string    `gorm:"size:64;not null;uniqueIndex:uq_crm_worker_heartbeat"`
	WorkerID    string    `gorm:"size:128;not null;uniqueIndex:uq_crm_worker_heartbeat"`
	HeartbeatAt time.Time `gorm:"precision:3;not null;index"`
	CreatedAt   time.Time `gorm:"precision:3;not null"`
	UpdatedAt   time.Time `gorm:"precision:3;not null"`
}

func (WorkerHeartbeat) TableName() string { return "crm_worker_heartbeats" }

// WorkerReadiness is the small admission-control boundary used by the domain.
// Implementations must return true when any instance of workerType has fresh
// persisted evidence; errors must never be interpreted as available.
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
