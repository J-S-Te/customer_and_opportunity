package opportunity

import (
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shopspring/decimal"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
)

var (
	ErrCommercialRecordInvalid = apperror.New(http.StatusUnprocessableEntity,
		"CRM_OPPORTUNITY_COMMERCIAL_RECORD_INVALID", "人工资料的类型、字段或状态无效")
	ErrCommercialRecordNotFound = apperror.New(http.StatusNotFound,
		"CRM_OPPORTUNITY_COMMERCIAL_RECORD_NOT_FOUND", "人工资料不存在或不可见")
	ErrCommercialRecordVoid = apperror.New(http.StatusConflict,
		"CRM_OPPORTUNITY_COMMERCIAL_RECORD_VOID", "已作废的人工资料不能再次修改")
	commercialAmountPattern = regexp.MustCompile(`^[0-9]+(?:\.[0-9]{1,2})?$`)
)

// CommercialRecord 保存人工资料；其状态不参与可信外部快照及商机阶段计算。
type CommercialRecord struct {
	database.Model
	PublicID      string           `gorm:"size:64;not null"`
	OpportunityID uint64           `gorm:"not null"`
	Type          string           `gorm:"size:16;not null"`
	Title         string           `gorm:"size:200;not null"`
	DocumentNo    string           `gorm:"size:64;not null"`
	RecordDate    time.Time        `gorm:"type:date;not null"`
	Amount        *decimal.Decimal `gorm:"type:decimal(18,2)"`
	Status        string           `gorm:"size:16;not null"`
	Notes         string           `gorm:"type:text;not null"`
	SourceKind    string           `gorm:"size:16;not null"`
	IsVoid        bool             `gorm:"not null"`
	VoidReason    string           `gorm:"size:500;not null"`
	CreatedByName string           `gorm:"size:200;not null"`
	UpdatedByName string           `gorm:"size:200;not null"`
}

// TableName 用途：指定人工资料的持久化表。
// 参数：无。
// 返回值：人工资料表名。
func (CommercialRecord) TableName() string { return "crm_opportunity_commercial_records" }

type commercialRecordAttachment struct {
	TenantID     string
	RecordID     uint64
	AttachmentID string
	Position     int
}

// TableName 用途：指定人工资料与可信附件的关联表。
// 参数：无。
// 返回值：人工资料附件关联表名。
func (commercialRecordAttachment) TableName() string {
	return "crm_opportunity_commercial_record_attachments"
}

type CommercialRecordRequest struct {
	Type           string   `json:"type"`
	Title          string   `json:"title"`
	DocumentNo     string   `json:"document_no"`
	RecordDate     string   `json:"record_date"`
	Amount         *string  `json:"amount"`
	Status         string   `json:"status"`
	Notes          string   `json:"notes"`
	AttachmentIDs  []string `json:"attachment_ids"`
	Version        uint64   `json:"version"`
	IdempotencyKey string   `json:"-"`
}

type CommercialRecordVoidRequest struct {
	Version uint64 `json:"version"`
	Reason  string `json:"reason"`
}

type CommercialRecordResponse struct {
	ID            string               `json:"id"`
	OpportunityID uint64               `json:"opportunity_id"`
	Type          string               `json:"type"`
	Title         string               `json:"title"`
	DocumentNo    string               `json:"document_no"`
	RecordDate    string               `json:"record_date"`
	Amount        *string              `json:"amount"`
	Status        string               `json:"status"`
	Notes         string               `json:"notes"`
	SourceKind    string               `json:"source_kind"`
	IsVoid        bool                 `json:"is_void"`
	VoidReason    string               `json:"void_reason"`
	Version       uint64               `json:"version"`
	CreatedBy     string               `json:"created_by"`
	CreatedByName string               `json:"created_by_name"`
	UpdatedBy     string               `json:"updated_by"`
	UpdatedByName string               `json:"updated_by_name"`
	CreatedAt     time.Time            `json:"created_at"`
	UpdatedAt     time.Time            `json:"updated_at"`
	Attachments   []AttachmentResponse `json:"attachments"`
}

// normalizeCommercialRecord 用途：校验并归一化人工资料，拒绝精度丢失及不匹配的状态。
// 参数：input 为人工登记字段；空金额表示未知金额。
// 返回值：归一化字段；校验失败时返回资料输入错误。
func normalizeCommercialRecord(input CommercialRecordRequest) (CommercialRecordRequest, error) {
	input.Title = strings.TrimSpace(input.Title)
	input.DocumentNo = strings.TrimSpace(input.DocumentNo)
	input.Notes = strings.TrimSpace(input.Notes)
	if input.Title == "" || utf8.RuneCountInString(input.Title) > 200 ||
		utf8.RuneCountInString(input.DocumentNo) > 64 || utf8.RuneCountInString(input.Notes) > 2000 {
		return input, ErrCommercialRecordInvalid
	}
	if date, err := time.Parse("2006-01-02", input.RecordDate); err != nil ||
		date.Format("2006-01-02") != input.RecordDate || date.Year() < 1000 {
		return input, ErrCommercialRecordInvalid
	}
	statuses := map[string][]string{
		"QUOTATION": {"DRAFT", "SUBMITTED", "APPROVED", "REJECTED", "CANCELLED"},
		"BID":       {"PREPARING", "SUBMITTED", "WON", "LOST", "CANCELLED"},
	}
	allowed, ok := statuses[input.Type]
	if !ok {
		return input, ErrCommercialRecordInvalid
	}
	if input.Status == "" {
		input.Status = allowed[0]
	}
	valid := false
	for _, status := range allowed {
		valid = valid || input.Status == status
	}
	if !valid {
		return input, ErrCommercialRecordInvalid
	}
	var err error
	input.Amount, err = normalizeCommercialAmount(input.Amount)
	if err != nil {
		return input, err
	}
	if len(input.AttachmentIDs) > 20 {
		return input, ErrCommercialRecordInvalid
	}
	seen := make(map[string]bool, len(input.AttachmentIDs))
	for _, id := range input.AttachmentIDs {
		if id == "" || len(id) > 64 || strings.TrimSpace(id) != id || seen[id] {
			return input, ErrCommercialRecordInvalid
		}
		seen[id] = true
	}
	return input, nil
}

// normalizeCommercialAmount 用途：将可空金额归一为两位十进制字符串，避免浮点误差。
// 参数：input 为元金额字符串；空串和 nil 均表示未知。
// 返回值：两位小数金额或 nil；格式、精度或范围错误时返回输入错误。
func normalizeCommercialAmount(input *string) (*string, error) {
	if input == nil || strings.TrimSpace(*input) == "" {
		return nil, nil
	}
	raw := strings.TrimSpace(*input)
	if len(raw) > 64 || !commercialAmountPattern.MatchString(raw) {
		return nil, ErrCommercialRecordInvalid
	}
	amount, err := decimal.NewFromString(raw)
	limit := decimal.RequireFromString("9999999999999999.99")
	if err != nil || amount.IsNegative() || amount.GreaterThan(limit) {
		return nil, ErrCommercialRecordInvalid
	}
	value := amount.StringFixed(2)
	return &value, nil
}

// toCommercialRecord 用途：生成只读 DTO，并按调用方已验证的权限附带附件元数据。
// 参数：value 为台账实体，attachments 为允许返回的可信附件列表。
// 返回值：保留人工来源、操作者、版本和作废痕迹的公开资料。
func toCommercialRecord(value *CommercialRecord, attachments []Attachment) CommercialRecordResponse {
	result := CommercialRecordResponse{
		ID: value.PublicID, OpportunityID: value.OpportunityID, Type: value.Type,
		Title: value.Title, DocumentNo: value.DocumentNo, RecordDate: value.RecordDate.Format("2006-01-02"),
		Status: value.Status, Notes: value.Notes, SourceKind: "MANUAL", IsVoid: value.IsVoid,
		VoidReason: value.VoidReason, Version: value.Version, CreatedBy: value.CreatedBy,
		CreatedByName: value.CreatedByName, UpdatedBy: value.UpdatedBy, UpdatedByName: value.UpdatedByName,
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt, Attachments: []AttachmentResponse{},
	}
	if value.Amount != nil {
		amount := value.Amount.StringFixed(2)
		result.Amount = &amount
	}
	for i := range attachments {
		result.Attachments = append(result.Attachments, toAttachment(&attachments[i]))
	}
	return result
}
