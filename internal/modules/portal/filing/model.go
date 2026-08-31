package filing

import (
	"time"

	"gorm.io/gorm"
)

const (
	FormVersion = "2025.1"
	StatusDraft = "DRAFT"
	// StatusWaitingCRM 使用历史数据库枚举 WAITING_CONTRACT 以兼容既有迁移；
	// 业务含义是“已由 Portal 提交，等待客户与商机系统人工完善”，不是自动提交公安。
	StatusWaitingCRM       = "WAITING_CONTRACT"
	StatusWaitingContract  = StatusWaitingCRM // legacy alias
	StatusSubmitting       = "SUBMITTING"
	StatusSubmissionFailed = "SUBMISSION_FAILED"
	StatusSubmitted        = "SUBMITTED" // reached only with an immutable trusted provider receipt
	MaterialPendingUpload  = "PENDING_UPLOAD"
	MaterialFinalizing     = "FINALIZING"
	MaterialScanning       = "SCANNING"
	MaterialClean          = "CLEAN"
	MaterialRejected       = "REJECTED"
	MaterialScanFailed     = "SCAN_FAILED"
)

// Filing 是客户范围内备案的可变头记录；已提交内容不再从这里读取，而由每次提交生成的不可变 SubmissionSnapshot 固化。
type Filing struct {
	ID                   uint64         `gorm:"primaryKey"`
	TenantID             string         `gorm:"size:64;not null;index"`
	CreatedBy            string         `gorm:"size:128;not null"`
	UpdatedBy            string         `gorm:"size:128;not null"`
	CreatedAt            time.Time      `gorm:"precision:3;not null"`
	UpdatedAt            time.Time      `gorm:"precision:3;not null"`
	DeletedAt            gorm.DeletedAt `gorm:"precision:3;index"`
	Version              uint64         `gorm:"not null;default:1"`
	PublicID             string         `gorm:"size:64;not null;uniqueIndex"`
	FilingNo             string         `gorm:"size:48;not null;uniqueIndex:uq_portal_filing_no,priority:2"`
	CustomerID           uint64         `gorm:"not null;index:idx_portal_filing_customer,priority:1;uniqueIndex:uq_portal_filing_create,priority:2"`
	AccountID            string         `gorm:"size:128;not null;uniqueIndex:uq_portal_filing_create,priority:3"`
	ProjectID            string         `gorm:"size:64;not null;default:''"`
	FormVersion          string         `gorm:"size:16;not null"`
	Status               string         `gorm:"size:24;not null;index:idx_portal_filing_customer,priority:2"`
	CurrentStep          uint8          `gorm:"not null;default:1"`
	CompletionPct        uint8          `gorm:"not null;default:0"`
	SubmittedAt          *time.Time     `gorm:"precision:3"`
	LockedAt             *time.Time     `gorm:"precision:3"`
	UnlockedAt           *time.Time     `gorm:"precision:3"`
	UnlockReasonCipher   []byte         `gorm:"type:mediumblob"`
	CreateIdempotencyKey string         `gorm:"size:128;not null;uniqueIndex:uq_portal_filing_create,priority:4"`
	CreateRequestHash    string         `gorm:"size:64;not null"`
}

func (Filing) TableName() string { return "portal_filings" }

// Section 只保存当前已通过结构校验的草稿；每次替换都会追加带摘要和加密结果元数据的 Action。
type Section struct {
	ID               uint64    `gorm:"primaryKey;autoIncrement"`
	TenantID         string    `gorm:"size:64;not null;uniqueIndex:uq_portal_filing_section,priority:1"`
	FilingID         uint64    `gorm:"not null;uniqueIndex:uq_portal_filing_section,priority:2;index"`
	SectionCode      string    `gorm:"size:48;not null;uniqueIndex:uq_portal_filing_section,priority:3"`
	SchemaVersion    string    `gorm:"size:16;not null"`
	DataCipher       []byte    `gorm:"type:mediumblob;not null"`
	ValidationStatus string    `gorm:"size:16;not null"`
	UpdatedBy        string    `gorm:"size:128;not null"`
	CreatedAt        time.Time `gorm:"precision:3;not null"`
	UpdatedAt        time.Time `gorm:"precision:3;not null"`
	Version          uint64    `gorm:"not null;default:1"`
}

func (Section) TableName() string { return "portal_filing_sections" }

// MatrixSelection 每个矩阵代码固定一行；取消选择仅置为 false，保留乐观锁版本，防止版本重置后的旧写入复活。
type MatrixSelection struct {
	ID         uint64    `gorm:"primaryKey;autoIncrement"`
	TenantID   string    `gorm:"size:64;not null;uniqueIndex:uq_portal_filing_matrix,priority:1"`
	FilingID   uint64    `gorm:"not null;uniqueIndex:uq_portal_filing_matrix,priority:2;index"`
	MatrixCode string    `gorm:"size:48;not null;uniqueIndex:uq_portal_filing_matrix,priority:3"`
	RowCode    string    `gorm:"size:48;not null;default:''"`
	ColumnCode string    `gorm:"size:48;not null;default:''"`
	Selected   bool      `gorm:"not null;default:false"`
	UpdatedBy  string    `gorm:"size:128;not null"`
	CreatedAt  time.Time `gorm:"precision:3;not null"`
	UpdatedAt  time.Time `gorm:"precision:3;not null"`
	Version    uint64    `gorm:"not null;default:1"`
}

func (MatrixSelection) TableName() string { return "portal_filing_matrix" }

// SubmissionSnapshot 是不可变申请数据，仓储不提供更新或删除；解锁后重新提交会创建更晚的新快照。
type SubmissionSnapshot struct {
	ID              uint64    `gorm:"primaryKey;autoIncrement"`
	TenantID        string    `gorm:"size:64;not null;uniqueIndex:uq_portal_filing_submission,priority:1"`
	FilingID        uint64    `gorm:"not null;uniqueIndex:uq_portal_filing_submission,priority:2;index"`
	Sequence        uint64    `gorm:"not null;uniqueIndex:uq_portal_filing_submission,priority:3"`
	FormVersion     string    `gorm:"size:16;not null"`
	CanonicalCipher []byte    `gorm:"type:longblob;not null"`
	SnapshotSHA256  string    `gorm:"size:64;not null"`
	SubmittedBy     string    `gorm:"size:128;not null"`
	SubmittedAt     time.Time `gorm:"precision:3;not null"`
}

func (SubmissionSnapshot) TableName() string { return "portal_filing_submission_snapshots" }

// SubmissionOutbox 只保存不可变 Portal 快照的稳定引用，不是公安系统线协议载荷。
// WAITING_CRM 是有意的人工接管状态；Portal 不直接向公安提交。只有 CRM 人工完善并
// 明确授权后，才允许由后续人工流程转交公安。旧版 WAITING_CONTRACT 名称仅为数据库兼容保留。
type SubmissionOutbox struct {
	ID               uint64     `gorm:"primaryKey;autoIncrement"`
	EventID          string     `gorm:"size:64;not null;uniqueIndex:uq_portal_filing_submission_event,priority:2"`
	TenantID         string     `gorm:"size:64;not null;uniqueIndex:uq_portal_filing_submission_event,priority:1"`
	FilingID         uint64     `gorm:"not null;index"`
	SubmissionID     uint64     `gorm:"not null;uniqueIndex:uq_portal_filing_submission_outbox,priority:2"`
	ContractVersion  string     `gorm:"size:48;not null"`
	Payload          []byte     `gorm:"type:json;not null"`
	PayloadSHA256    string     `gorm:"size:64;not null"`
	Status           string     `gorm:"size:32;not null;index"`
	RetryCount       uint32     `gorm:"not null;default:0"`
	NextRetryAt      *time.Time `gorm:"precision:3"`
	LockedBy         string     `gorm:"size:128;not null;default:''"`
	LockedUntil      *time.Time `gorm:"precision:3"`
	LastErrorSummary string     `gorm:"size:1000;not null;default:''"`
	CreatedAt        time.Time  `gorm:"precision:3;not null"`
	SentAt           *time.Time `gorm:"precision:3"`
}

func (SubmissionOutbox) TableName() string { return "portal_filing_submission_outbox" }

// SubmissionReceipt 是已配置公安提交方返回的不可变凭证；本地锁定快照不会生成它，只有 worker 验证供应方响应后才能持久化。
type SubmissionReceipt struct {
	ID                     uint64    `gorm:"primaryKey;autoIncrement"`
	TenantID               string    `gorm:"size:64;not null;uniqueIndex:uq_portal_filing_receipt_submission,priority:1"`
	FilingID               uint64    `gorm:"not null;index"`
	SubmissionID           uint64    `gorm:"not null;uniqueIndex:uq_portal_filing_receipt_submission,priority:2"`
	EventID                string    `gorm:"size:64;not null"`
	ProviderReceiptID      string    `gorm:"size:128;not null;uniqueIndex:uq_portal_filing_receipt_external,priority:2"`
	ProviderAuthority      string    `gorm:"size:128;not null;uniqueIndex:uq_portal_filing_receipt_external,priority:1"`
	ProviderEvidenceCipher []byte    `gorm:"type:mediumblob;not null" json:"-"`
	ProviderEvidenceSHA256 string    `gorm:"size:64;not null"`
	ReceivedAt             time.Time `gorm:"precision:3;not null"`
	CreatedAt              time.Time `gorm:"precision:3;not null"`
}

func (SubmissionReceipt) TableName() string { return "portal_filing_submission_receipts" }

// Material 保存加密的不可变对象引用和扫描证据，文件字节不进入 MySQL。
// 每个材料代码只有一行，避免旧的安全版本与新上传但未扫描版本混淆。
type Material struct {
	ID                 uint64     `gorm:"primaryKey;autoIncrement"`
	TenantID           string     `gorm:"size:64;not null;uniqueIndex:uq_portal_filing_material_public,priority:1;uniqueIndex:uq_portal_filing_material_code,priority:1;uniqueIndex:uq_portal_filing_material_create,priority:1"`
	PublicID           string     `gorm:"size:64;not null;uniqueIndex:uq_portal_filing_material_public,priority:2"`
	FilingID           uint64     `gorm:"not null;uniqueIndex:uq_portal_filing_material_code,priority:2"`
	MaterialCode       string     `gorm:"size:48;not null;uniqueIndex:uq_portal_filing_material_code,priority:3"`
	ObjectKeyCipher    []byte     `gorm:"type:varbinary(1024);not null" json:"-"`
	ObjectVersion      string     `gorm:"size:256;not null" json:"-"`
	FileName           string     `gorm:"size:255;not null"`
	MIMEType           string     `gorm:"size:160;not null"`
	SizeBytes          uint64     `gorm:"not null"`
	SHA256             string     `gorm:"size:64;not null"`
	ScanStatus         string     `gorm:"size:32;not null"`
	ScanReference      string     `gorm:"size:128;not null" json:"-"`
	FinalizeLeaseUntil *time.Time `gorm:"precision:3" json:"-"`
	CreateActorID      string     `gorm:"size:128;not null;uniqueIndex:uq_portal_filing_material_create,priority:2" json:"-"`
	CreateKeyHash      string     `gorm:"size:64;not null;uniqueIndex:uq_portal_filing_material_create,priority:3" json:"-"`
	CreateRequestHash  string     `gorm:"size:64;not null" json:"-"`
	UploadedAt         *time.Time `gorm:"precision:3"`
	ScannedAt          *time.Time `gorm:"precision:3"`
	CreatedBy          string     `gorm:"size:128;not null" json:"-"`
	UpdatedBy          string     `gorm:"size:128;not null" json:"-"`
	CreatedAt          time.Time  `gorm:"precision:3;not null"`
	UpdatedAt          time.Time  `gorm:"precision:3;not null"`
	Version            uint64     `gorm:"not null;default:1"`
}

func (Material) TableName() string { return "portal_filing_materials" }

// Action 是只追加的审计和幂等账本；ResponseCipher 使用 Portal 数据密钥保存精确、最小化的成功响应。
type Action struct {
	ID             uint64    `gorm:"primaryKey;autoIncrement"`
	TenantID       string    `gorm:"size:64;not null;uniqueIndex:uq_portal_filing_action,priority:1"`
	FilingID       uint64    `gorm:"not null;uniqueIndex:uq_portal_filing_action,priority:2;index"`
	Action         string    `gorm:"size:48;not null;uniqueIndex:uq_portal_filing_action,priority:4"`
	ActorType      string    `gorm:"size:16;not null"`
	ActorID        string    `gorm:"size:128;not null;uniqueIndex:uq_portal_filing_action,priority:3"`
	IdempotencyKey string    `gorm:"size:128;not null;uniqueIndex:uq_portal_filing_action,priority:5"`
	RequestHash    string    `gorm:"size:64;not null"`
	RequestCipher  []byte    `gorm:"type:mediumblob;not null"`
	RequestID      string    `gorm:"size:128;not null;default:''"`
	ResponseCipher []byte    `gorm:"type:mediumblob;not null"`
	OccurredAt     time.Time `gorm:"precision:3;not null"`
}

func (Action) TableName() string { return "portal_filing_actions" }
