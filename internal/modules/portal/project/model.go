package project

import (
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
)

type Snapshot struct {
	database.Model
	ProjectID            string     `gorm:"size:64;not null;uniqueIndex:uq_portal_project,priority:2"`
	CustomerID           uint64     `gorm:"not null;index:idx_portal_projects_customer,priority:1"`
	ProjectName          string     `gorm:"size:200;not null"`
	ContractNo           string     `gorm:"size:64"`
	Status               string     `gorm:"size:32;not null;index:idx_portal_projects_customer,priority:2"`
	ProgressPct          uint8      `gorm:"not null"`
	CurrentStage         string     `gorm:"size:64;not null"`
	ExpectedEndDate      *time.Time `gorm:"type:date"`
	Delayed              bool       `gorm:"not null;index"`
	ManagerName          string     `gorm:"column:manager_name_snapshot;size:128"`
	ManagerContactMasked string     `gorm:"size:128"`
	// ManagerPortalAccountID 是项目源提供的权威站内信收件账号；姓名、人员和联系方式只用于展示，不能提升为投递身份。
	ManagerPortalAccountID string    `gorm:"size:128;not null;default:''"`
	SourceUpdatedAt        time.Time `gorm:"precision:3;not null"`
	SyncedAt               time.Time `gorm:"precision:3;not null"`
	RawVersion             string    `gorm:"size:64;not null"`
}

func (Snapshot) TableName() string { return "portal_project_snapshots" }

type Milestone struct {
	ID          uint64     `gorm:"primaryKey;autoIncrement"`
	TenantID    string     `gorm:"size:64;not null;index"`
	CustomerID  uint64     `gorm:"not null;index:idx_portal_milestone_customer,priority:1"`
	ProjectID   string     `gorm:"size:64;not null;index:idx_portal_milestone_customer,priority:2"`
	StageCode   string     `gorm:"size:64;not null"`
	StageName   string     `gorm:"size:128;not null"`
	Status      string     `gorm:"size:32;not null"`
	PlannedAt   *time.Time `gorm:"precision:3"`
	CompletedAt *time.Time `gorm:"precision:3"`
	SortNo      int        `gorm:"not null"`
}

func (Milestone) TableName() string { return "portal_project_milestones" }

type Activity struct {
	ID               uint64    `gorm:"primaryKey;autoIncrement"`
	TenantID         string    `gorm:"size:64;not null;index"`
	CustomerID       uint64    `gorm:"not null;index:idx_portal_activity_customer,priority:1"`
	ProjectID        string    `gorm:"size:64;not null;index:idx_portal_activity_customer,priority:2"`
	SourceActivityID string    `gorm:"size:64;not null"`
	Type             string    `gorm:"size:32;not null"`
	Content          string    `gorm:"type:text;not null"`
	OccurredAt       time.Time `gorm:"precision:3;not null"`
}

func (Activity) TableName() string { return "portal_project_activities" }

type TeamMember struct {
	ID            uint64 `gorm:"primaryKey;autoIncrement"`
	TenantID      string `gorm:"size:64;not null;index"`
	CustomerID    uint64 `gorm:"not null;index:idx_portal_team_customer,priority:1"`
	ProjectID     string `gorm:"size:64;not null;index:idx_portal_team_customer,priority:2"`
	PersonRef     string `gorm:"size:64;not null"`
	Name          string `gorm:"size:128;not null"`
	Role          string `gorm:"size:64;not null"`
	ContactMasked string `gorm:"size:128"`
}

func (TeamMember) TableName() string { return "portal_project_team" }

type Bundle struct {
	Snapshot   Snapshot
	Milestones []Milestone
	Activities []Activity
	Team       []TeamMember
}

// Detail 是有界的客户项目聚合；活动历史可能无限增长，故只通过独立分页接口暴露，不嵌入详情。
type Detail struct {
	Snapshot   Snapshot
	Milestones []Milestone
	Team       []TeamMember
}
