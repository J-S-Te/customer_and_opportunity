package filing

import (
	"encoding/json"
	"time"
)

type Actor struct {
	TenantID, AccountID string
	CustomerID          uint64
}

type ValidationIssue struct {
	Path    string `json:"path"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type SectionView struct {
	Code             string            `json:"code"`
	SchemaVersion    string            `json:"schema_version"`
	Data             json.RawMessage   `json:"data"`
	ValidationStatus string            `json:"validation_status"`
	ValidationIssues []ValidationIssue `json:"validation_issues,omitempty"`
	Version          uint64            `json:"version"`
	UpdatedAt        time.Time         `json:"updated_at"`
}

type MatrixView struct {
	Code       string    `json:"code"`
	RowCode    string    `json:"row_code,omitempty"`
	ColumnCode string    `json:"column_code,omitempty"`
	Selected   bool      `json:"selected"`
	Version    uint64    `json:"version"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// View deliberately excludes tenant/customer/account IDs, database keys,
// idempotency hashes, audit actors and canonical snapshot bodies.
type View struct {
	ID            string          `json:"id"`
	FilingNo      string          `json:"filing_no"`
	ProjectID     string          `json:"project_id,omitempty"`
	FormVersion   string          `json:"form_version"`
	Status        string          `json:"status"`
	CurrentStep   uint8           `json:"current_step"`
	CompletionPct uint8           `json:"completion_pct"`
	SubmittedAt   *time.Time      `json:"submitted_at,omitempty"`
	LockedAt      *time.Time      `json:"locked_at,omitempty"`
	UnlockedAt    *time.Time      `json:"unlocked_at,omitempty"`
	Version       uint64          `json:"version"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
	Sections      []SectionView   `json:"sections,omitempty"`
	Matrices      []MatrixView    `json:"matrices,omitempty"`
	Submission    *SubmissionView `json:"submission,omitempty"`
	Materials     []MaterialView  `json:"materials,omitempty"`
}

type MaterialView struct {
	ID         string     `json:"id"`
	Code       string     `json:"code"`
	FileName   string     `json:"file_name"`
	MIMEType   string     `json:"mime_type"`
	SizeBytes  uint64     `json:"size_bytes"`
	SHA256     string     `json:"sha256"`
	ScanStatus string     `json:"scan_status"`
	UploadedAt *time.Time `json:"uploaded_at,omitempty"`
	ScannedAt  *time.Time `json:"scanned_at,omitempty"`
	Version    uint64     `json:"version"`
}

type SubmissionView struct {
	Sequence       uint64    `json:"sequence"`
	SnapshotSHA256 string    `json:"snapshot_sha256"`
	SubmittedAt    time.Time `json:"submitted_at"`
}

type ValidationResult struct {
	Valid  bool              `json:"valid"`
	Issues []ValidationIssue `json:"issues"`
}

type ListResult struct {
	Items    []View `json:"items"`
	Page     int    `json:"page"`
	PageSize int    `json:"page_size"`
	Total    int64  `json:"total"`
}
