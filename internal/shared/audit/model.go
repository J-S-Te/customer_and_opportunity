package audit

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/requestaudit"
	"gorm.io/gorm"
)

type Event struct {
	ID                uint64    `gorm:"primaryKey"`
	EventID           string    `gorm:"size:64;not null;uniqueIndex"`
	TenantID          string    `gorm:"size:64;not null;index"`
	ApplicationCode   string    `gorm:"size:64;not null"`
	Module            string    `gorm:"size:64;not null"`
	Operation         string    `gorm:"size:64;not null"`
	ResourceType      string    `gorm:"size:64;not null"`
	ResourceID        string    `gorm:"size:64;not null;index"`
	ActorID           string    `gorm:"size:64;not null"`
	ActorNameSnapshot string    `gorm:"size:200"`
	BeforeJSON        []byte    `gorm:"type:json"`
	AfterJSON         []byte    `gorm:"type:json"`
	Reason            string    `gorm:"size:500"`
	Result            string    `gorm:"size:32;not null"`
	RequestID         string    `gorm:"size:64;not null;index"`
	OccurredAt        time.Time `gorm:"precision:3;not null"`
}

func (Event) TableName() string { return "crm_audit_events" }

type Writer interface {
	Write(context.Context, Event) error
}

type GORMWriter struct {
	db              *gorm.DB
	platformOutbox  *requestaudit.Store
	environmentCode string
}

func NewGORMWriter(db *gorm.DB) *GORMWriter { return &GORMWriter{db: db} }

func (w *GORMWriter) UsePlatformOutbox(outbox *requestaudit.Store, environmentCode string) *GORMWriter {
	w.platformOutbox = outbox
	w.environmentCode = environmentCode
	return w
}

func (w *GORMWriter) Write(ctx context.Context, event Event) error {
	// 优先复用上下文中的业务事务，使状态变化与审计事件同成同败；没有事务时才使用共享连接。
	if event.EventID == "" {
		event.EventID = request.NewID()
	}
	event.ApplicationCode = "customer_and_opportunity"
	// 应用代码、追踪号和发生时间由服务端覆盖，调用方只能提供业务快照，不能伪造审计来源。
	event.RequestID = request.ID(ctx)
	event.OccurredAt = time.Now().UTC()
	if err := database.FromContext(ctx, w.db).Create(&event).Error; err != nil {
		return err
	}
	if w.platformOutbox == nil {
		return nil
	}
	result := "SUCCESS"
	switch event.Result {
	case "REJECTED", "DENIED":
		result = "DENIED"
	case "FAILED", "FAILURE":
		result = "FAILURE"
	}
	actorType := "USER"
	if strings.HasPrefix(event.ActorID, "machine:") || strings.HasSuffix(event.ActorID, "-machine") {
		actorType = "MACHINE"
	} else if event.ActorID == "" || strings.HasPrefix(event.ActorID, "system") {
		actorType = "SYSTEM"
	}
	return w.platformOutbox.Publish(ctx, requestaudit.BusinessEvent{
		EventID: event.EventID, TenantID: event.TenantID, ApplicationCode: event.ApplicationCode,
		EnvironmentCode: w.environmentCode, ActorType: actorType, ActorID: event.ActorID,
		ActorName: event.ActorNameSnapshot, Action: event.Module + "." + event.Operation,
		ResourceType: event.ResourceType, ResourceID: event.ResourceID, RequestID: event.RequestID,
		Result: result, RiskLevel: "HIGH", OccurredAt: event.OccurredAt,
	})
}

func JSON(value any) []byte {
	// 审计快照来自内部结构；编码失败退化为空值，调用方不得把该辅助函数用于需要返回错误的协议数据。
	encoded, _ := json.Marshal(value)
	return encoded
}
