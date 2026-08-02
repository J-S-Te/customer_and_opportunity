package database

import (
	"time"

	"gorm.io/gorm"
)

type Model struct {
	ID        uint64         `gorm:"primaryKey" json:"id"`
	TenantID  string         `gorm:"size:64;not null;index" json:"-"`
	CreatedBy string         `gorm:"size:64;not null" json:"created_by"`
	UpdatedBy string         `gorm:"size:64;not null" json:"updated_by"`
	CreatedAt time.Time      `gorm:"precision:3" json:"created_at"`
	UpdatedAt time.Time      `gorm:"precision:3" json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"precision:3;index" json:"-"`
	Version   uint64         `gorm:"not null;default:1" json:"version"`
}
