package portalprojectworker

import "time"

type syncState struct {
	ID               uint64     `gorm:"primaryKey;autoIncrement"`
	TenantID         string     `gorm:"size:64;not null;uniqueIndex:uq_portal_project_sync_customer,priority:1"`
	CustomerID       uint64     `gorm:"not null;uniqueIndex:uq_portal_project_sync_customer,priority:2"`
	Cursor           string     `gorm:"column:sync_cursor;size:1024;not null"`
	NextRunAt        time.Time  `gorm:"precision:3;not null;index:idx_portal_project_sync_claim,priority:1"`
	LastAttemptAt    *time.Time `gorm:"precision:3"`
	LastSuccessAt    *time.Time `gorm:"precision:3"`
	LastErrorSummary string     `gorm:"size:1000;not null"`
	LockedBy         string     `gorm:"size:128;not null"`
	LockedUntil      *time.Time `gorm:"precision:3;index:idx_portal_project_sync_claim,priority:2"`
	CreatedAt        time.Time  `gorm:"precision:3;not null"`
	UpdatedAt        time.Time  `gorm:"precision:3;not null"`
	Version          uint64     `gorm:"not null;default:1"`
}

func (syncState) TableName() string { return "portal_project_sync_states" }

type sourcePage struct {
	Bundles    []sourceBundle
	NextCursor string
	HasMore    bool
}

type sourceBundle struct {
	ProjectID              string
	ProjectName            string
	ContractNo             string
	Status                 string
	ProgressPct            uint8
	CurrentStage           string
	ExpectedEndDate        *time.Time
	Delayed                bool
	ManagerName            string
	ManagerContactMasked   string
	ManagerPortalAccountID string
	SourceUpdatedAt        time.Time
	RawVersion             string
	Milestones             []sourceMilestone
	Activities             []sourceActivity
	Team                   []sourceTeamMember
}

type sourceMilestone struct {
	StageCode   string
	StageName   string
	Status      string
	PlannedAt   *time.Time
	CompletedAt *time.Time
	SortNo      int
}
type sourceActivity struct {
	SourceActivityID string
	Type             string
	Content          string
	OccurredAt       time.Time
}
type sourceTeamMember struct {
	PersonRef     string
	Name          string
	Role          string
	ContactMasked string
}
