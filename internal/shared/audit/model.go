package audit

import (
	"context"
	"encoding/json"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
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

type GORMWriter struct{ db *gorm.DB }

func NewGORMWriter(db *gorm.DB) *GORMWriter { return &GORMWriter{db: db} }

func (w *GORMWriter) Write(ctx context.Context, event Event) error {
	if event.EventID == "" {
		event.EventID = request.NewID()
	}
	event.ApplicationCode = "customer_and_opportunity"
	event.RequestID = request.ID(ctx)
	event.OccurredAt = time.Now().UTC()
	return database.FromContext(ctx, w.db).Create(&event).Error
}

func JSON(value any) []byte {
	encoded, _ := json.Marshal(value)
	return encoded
}
