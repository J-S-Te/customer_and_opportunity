package opportunity

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shopspring/decimal"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/businessclock"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
	"gorm.io/gorm"
)

const (
	WinModeContract      = "contract"
	WinModeLocalEvidence = "local_evidence"
	PendingWinEvidence   = "WIN_EVIDENCE"
)

// WinEvidenceInput 是人工赢单登记，不表示合同系统已核验或已建立合同。
type WinEvidenceInput struct {
	EvidenceType      string `json:"evidence_type" binding:"required,max=32"`
	EvidenceReference string `json:"evidence_reference" binding:"required,max=200"`
	ConfirmedOn       string `json:"confirmed_on" binding:"required,len=10"`
	ConfirmedAmount   string `json:"confirmed_amount" binding:"required,max=19"`
	EvidenceNote      string `json:"evidence_note" binding:"required,max=1000"`
}

type WinConfirmation struct {
	ID                 uint64          `gorm:"primaryKey" json:"id"`
	TenantID           string          `json:"-"`
	OpportunityID      uint64          `json:"opportunity_id"`
	CustomerID         uint64          `json:"customer_id"`
	OpportunityVersion uint64          `json:"opportunity_version"`
	Mode               string          `json:"mode"`
	EvidenceType       string          `json:"evidence_type"`
	EvidenceReference  string          `json:"evidence_reference"`
	ConfirmedOn        time.Time       `gorm:"type:date" json:"confirmed_on"`
	ConfirmedAmount    decimal.Decimal `gorm:"type:decimal(18,2)" json:"confirmed_amount"`
	EvidenceNote       string          `json:"evidence_note"`
	ConfirmedBy        string          `json:"confirmed_by"`
	ConfirmedAt        time.Time       `json:"confirmed_at"`
	RequestID          string          `json:"request_id"`
}

// TableName 指定本地赢单证据的独立持久表。
// 参数：无。
// 返回值：CRM 赢单证据表名。
func (WinConfirmation) TableName() string { return "crm_opportunity_win_confirmations" }

type WinEvidenceStore interface {
	Create(context.Context, *WinConfirmation) error
	Get(context.Context, string, uint64) (*WinConfirmation, error)
}

type gormWinEvidenceStore struct{ db *gorm.DB }

// NewWinEvidenceStore 复用 CRM 数据库与现有事务上下文。
// 参数：db 为 CRM 自有数据库连接。
// 返回值：独立赢单证据仓储，不修改已有商机仓储接口。
func NewWinEvidenceStore(db *gorm.DB) WinEvidenceStore { return &gormWinEvidenceStore{db: db} }

// UseWinConfirmation 配置显式赢单模式和转合同订阅。
// 参数：mode 为已校验模式，subscribed 为转单订阅，store 为证据仓储。
// 返回值：当前服务，供装配继续配置其他能力。
func (s *Service) UseWinConfirmation(mode string, subscribed bool, store WinEvidenceStore) *Service {
	s.winMode, s.contractTransferDisabled, s.winEvidence = mode, !subscribed, store
	return s
}

// Create 在商机变更的同一事务保存不可变赢单证据。
// 参数：ctx 携带现有事务，value 为已校验的服务端记录。
// 返回值：持久化错误；唯一键冲突不能覆盖已有证据。
func (s *gormWinEvidenceStore) Create(ctx context.Context, value *WinConfirmation) error {
	return database.FromContext(ctx, s.db).Create(value).Error
}

// Get 按租户与商机精确读取证据，调用方必须先完成商机数据范围校验。
// 参数：ctx 为请求上下文，tenant 为租户，id 为商机标识。
// 返回值：证据；尚未登记返回 nil，数据库异常原样返回。
func (s *gormWinEvidenceStore) Get(ctx context.Context, tenant string, id uint64) (*WinConfirmation, error) {
	var value WinConfirmation
	err := database.FromContext(ctx, s.db).Where("tenant_id=? AND opportunity_id=?", tenant, id).Take(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &value, err
}

// winModeValue 将未显式配置的服务保持为原合同验真模式。
// 参数：无。
// 返回值：生效的赢单模式。
func (s *Service) winModeValue() string {
	if s.winMode == "" {
		return WinModeContract
	}
	return s.winMode
}

// prepareWinConfirmation 按显式模式验证证据，不以缺失合同适配器放行。
// 参数：ctx 为身份上下文，model 为可见商机，contract 为合同引用，input 为本地证据。
// 返回值：本地证据或合同模式的 nil；拒绝模式混用、依赖失败或无效依据。
func (s *Service) prepareWinConfirmation(ctx context.Context, model *Opportunity,
	contract *string, input *WinEvidenceInput) (*WinConfirmation, error) {
	if s.winModeValue() == WinModeLocalEvidence {
		if contract != nil && strings.TrimSpace(*contract) != "" {
			return nil, ErrWinModeMismatch
		}
		if s.winEvidence == nil {
			return nil, ErrWinEvidenceUnavailable
		}
		return validateWinEvidence(input, s.now())
	}
	if s.winModeValue() != WinModeContract || input != nil {
		return nil, ErrWinModeMismatch
	}
	if contract == nil || strings.TrimSpace(*contract) == "" {
		return nil, ErrContractRequired
	}
	if s.contracts == nil {
		return nil, apperror.New(503, "INTEGRATION_CONTRACT_NOT_CONFIGURED", "contract verification is not configured")
	}
	belongs, err := s.contracts.BelongsToCustomer(ctx, strings.TrimSpace(*contract), model.CustomerID)
	if err != nil {
		return nil, apperror.Wrap(err, 503, "INTEGRATION_DEPENDENCY_UNAVAILABLE", "contract verification is unavailable")
	}
	if !belongs {
		return nil, apperror.New(422, "CRM_OPPORTUNITY_CONTRACT_CUSTOMER_MISMATCH", "contract belongs to another customer")
	}
	return nil, nil
}

// validateWinEvidence 校验人工依据、金额及北京业务日期。
// 参数：input 为请求证据，now 为服务端当前时刻。
// 返回值：规范化证据；缺少或非法字段返回明确业务错误。
func validateWinEvidence(input *WinEvidenceInput, now time.Time) (*WinConfirmation, error) {
	if input == nil {
		return nil, ErrWinEvidenceRequired
	}
	kind := strings.TrimSpace(input.EvidenceType)
	reference := strings.TrimSpace(input.EvidenceReference)
	note := strings.TrimSpace(input.EvidenceNote)
	validKind := kind == "PURCHASE_ORDER" || kind == "AWARD_NOTICE" ||
		kind == "CUSTOMER_CONFIRMATION" || kind == "OTHER"
	day, dateErr := time.Parse("2006-01-02", input.ConfirmedOn)
	amountText := strings.TrimSpace(input.ConfirmedAmount)
	if !winAmountPattern.MatchString(amountText) {
		return nil, ErrWinEvidenceInvalid
	}
	amount, amountErr := decimal.NewFromString(amountText)
	validText := reference != "" && note != "" &&
		utf8.RuneCountInString(reference) <= 200 && utf8.RuneCountInString(note) <= 1000 &&
		!strings.ContainsAny(reference+note, "\x00")
	validDay := dateErr == nil && day.Year() >= 1000 &&
		input.ConfirmedOn <= now.In(businessclock.Location()).Format("2006-01-02")
	validAmount := amountErr == nil && !amount.IsNegative() && amount.Exponent() >= -2 &&
		!amount.GreaterThan(decimal.RequireFromString("9999999999999999.99"))
	if !validKind || !validText || !validDay || !validAmount {
		return nil, ErrWinEvidenceInvalid
	}
	return &WinConfirmation{Mode: WinModeLocalEvidence, EvidenceType: kind, EvidenceReference: reference,
		ConfirmedOn: day, ConfirmedAmount: amount, EvidenceNote: note}, nil
}

// saveWinConfirmation 将身份及商机坐标写入本地证据并保持在调用事务内。
// 参数：ctx 为事务上下文，model 为更新后的商机，evidence 为可选本地证据。
// 返回值：无本地证据时成功；持久化失败返回错误并触发外层回滚。
func (s *Service) saveWinConfirmation(ctx context.Context, model *Opportunity, evidence *WinConfirmation) error {
	if evidence == nil {
		return nil
	}
	evidence.TenantID, evidence.OpportunityID, evidence.CustomerID = model.TenantID, model.ID, model.CustomerID
	evidence.OpportunityVersion, evidence.ConfirmedBy = model.Version, model.UpdatedBy
	evidence.ConfirmedAt, evidence.RequestID = s.now(), request.ID(ctx)
	return s.winEvidence.Create(ctx, evidence)
}

// winAuditSnapshot 同时记录人工赢单依据，保留原合同模式审计结构。
// 参数：model 为商机，evidence 为本地确认记录。
// 返回值：可序列化的审计快照。
func winAuditSnapshot(model *Opportunity, evidence *WinConfirmation) any {
	if evidence == nil {
		return toResponse(model)
	}
	return map[string]any{"opportunity": toResponse(model), "win_confirmation": evidence}
}

// ensureContractTransferSubscribed 阻断未订阅时的新增及重试投递。
// 参数：无。
// 返回值：未订阅错误或 nil；不修改历史任务状态。
func (s *Service) ensureContractTransferSubscribed() error {
	if s.contractTransferDisabled {
		return ErrContractTransferNotSubscribed
	}
	return nil
}

var winAmountPattern = regexp.MustCompile(`^[0-9]{1,16}(\.[0-9]{1,2})?$`)

var (
	ErrWinEvidenceRequired           = apperror.New(422, "CRM_WIN_EVIDENCE_REQUIRED", "请填写本地赢单依据")
	ErrWinEvidenceInvalid            = apperror.New(422, "CRM_WIN_EVIDENCE_INVALID", "赢单依据、金额或确认日期无效")
	ErrWinModeMismatch               = apperror.New(422, "CRM_WIN_CONFIRMATION_MODE_MISMATCH", "赢单依据与当前确认模式不一致")
	ErrWinEvidenceUnavailable        = apperror.New(503, "CRM_WIN_EVIDENCE_UNAVAILABLE", "本地赢单依据存储未配置")
	ErrContractTransferNotSubscribed = apperror.New(409, "CRM_CONTRACT_TRANSFER_NOT_SUBSCRIBED", "当前未订阅合同转单，历史队列保持原状态")
)

type WinConfirmationDetail struct {
	Mode         string           `json:"mode"`
	Confirmation *WinConfirmation `json:"confirmation"`
	ConfirmedOn  string           `json:"confirmed_on,omitempty"`
}

// GetWinConfirmation 读取可见商机的本地历史证据，模式切换也不抹除历史。
// 参数：ctx 为当前身份上下文，id 为商机标识。
// 返回值：当前模式及可选证据；越权、存储异常时失败。
func (s *Service) GetWinConfirmation(ctx context.Context, id uint64) (*WinConfirmationDetail, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = s.repo.FindByID(ctx, principal, id); err != nil {
		return nil, err
	}
	if s.winEvidence == nil {
		return nil, ErrWinEvidenceUnavailable
	}
	evidence, err := s.winEvidence.Get(ctx, principal.TenantID, id)
	if err != nil {
		return nil, err
	}
	result := &WinConfirmationDetail{Mode: s.winModeValue(), Confirmation: evidence}
	if evidence != nil {
		result.ConfirmedOn = evidence.ConfirmedOn.Format("2006-01-02")
	}
	return result, nil
}
