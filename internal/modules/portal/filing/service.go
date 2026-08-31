package filing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/pagination"
	requestctx "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
)

var (
	ErrNotFound            = apperror.New(http.StatusNotFound, "PORTAL_FILING_NOT_FOUND", "filing not found")
	ErrValidation          = apperror.New(http.StatusUnprocessableEntity, "PORTAL_FILING_VALIDATION_ERROR", "filing request is invalid")
	ErrVersionConflict     = apperror.New(http.StatusConflict, "PORTAL_FILING_VERSION_CONFLICT", "filing draft was modified by another request")
	ErrLocked              = apperror.New(http.StatusConflict, "PORTAL_FILING_LOCKED", "submitted filing is locked")
	ErrInvalidState        = apperror.New(http.StatusConflict, "PORTAL_FILING_INVALID_STATE", "filing state does not allow this operation")
	ErrIdempotencyConflict = apperror.New(http.StatusConflict, "PORTAL_FILING_IDEMPOTENCY_CONFLICT", "idempotency key was used with another request")
)

type Clock interface{ Now() time.Time }
type IDGenerator interface{ NewID() string }

type Protector interface {
	Encrypt(context.Context, []byte) ([]byte, error)
	Decrypt(context.Context, []byte) ([]byte, error)
}

type ProjectAccess interface {
	Accessible(context.Context, string, uint64, string) (bool, error)
}

type Service struct {
	repo      Repository
	protector Protector
	projects  ProjectAccess
	clock     Clock
	ids       IDGenerator
}

func NewService(repo Repository, protector Protector, projects ProjectAccess, clock Clock, ids IDGenerator) *Service {
	return &Service{repo: repo, protector: protector, projects: projects, clock: clock, ids: ids}
}

type CreateCommand struct {
	ProjectID      string `json:"project_id"`
	IdempotencyKey string `json:"-"`
}
type SaveSectionCommand struct {
	ExpectedVersion uint64
	Data            json.RawMessage
	IdempotencyKey  string
}
type SaveMatrixCommand struct {
	ExpectedFilingVersion uint64 `json:"expected_filing_version"`
	ExpectedMatrixVersion uint64 `json:"expected_matrix_version"`
	RowCode               string `json:"row_code"`
	ColumnCode            string `json:"column_code"`
	Selected              bool   `json:"selected"`
	IdempotencyKey        string `json:"-"`
}
type SubmitCommand struct {
	ExpectedVersion uint64 `json:"expected_version"`
	IdempotencyKey  string `json:"-"`
}
type UnlockCommand struct {
	CustomerID      uint64 `json:"customer_id"`
	ExpectedVersion uint64 `json:"expected_version"`
	Reason          string `json:"reason"`
	IdempotencyKey  string `json:"-"`
}
type InternalActor struct{ TenantID, ActorID string }

func (s *Service) Create(ctx context.Context, actor Actor, command CreateCommand) (*View, error) {
	// 创建键绑定账号与项目；项目可访问性从可信投影确认，不能由请求中的客户标识决定。
	command.ProjectID, command.IdempotencyKey = strings.TrimSpace(command.ProjectID), strings.TrimSpace(command.IdempotencyKey)
	if !validActor(actor) || len(command.ProjectID) > 64 || !validIdempotencyKey(command.IdempotencyKey) {
		return nil, ErrValidation
	}
	hash := hashValue(struct {
		ProjectID string `json:"project_id"`
	}{command.ProjectID})
	if value, err := s.repo.FindCreateAction(ctx, actor, command.IdempotencyKey); err == nil {
		if value.CreateRequestHash != hash {
			return nil, ErrIdempotencyConflict
		}
		return s.view(ctx, value)
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if command.ProjectID != "" {
		if s.projects == nil {
			return nil, ErrValidation
		}
		accessible, err := s.projects.Accessible(ctx, actor.TenantID, actor.CustomerID, command.ProjectID)
		if err != nil {
			return nil, err
		}
		if !accessible {
			return nil, ErrNotFound
		}
	}
	now := s.clock.Now().UTC()
	publicID, number := s.ids.NewID(), s.ids.NewID()
	if !validGeneratedID(publicID) || !validGeneratedID(number) {
		return nil, errors.New("secure filing identifier generation failed")
	}
	value := &Filing{PublicID: publicID, FilingNo: "FIL-" + number, CustomerID: actor.CustomerID, AccountID: actor.AccountID, ProjectID: command.ProjectID, FormVersion: FormVersion, Status: StatusDraft, CurrentStep: 1, CreateIdempotencyKey: command.IdempotencyKey, CreateRequestHash: hash}
	value.TenantID = actor.TenantID
	value.CreatedBy = actor.AccountID
	value.UpdatedBy = actor.AccountID
	value.CreatedAt = now
	value.UpdatedAt = now
	value.Version = 1
	var result *View
	err := s.repo.WithTransaction(ctx, func(tx context.Context) error {
		if err := s.repo.Create(tx, value); err != nil {
			return err
		}
		v := basicView(value)
		responseJSON, _ := json.Marshal(v)
		responseCipher, protectErr := s.protector.Encrypt(tx, responseJSON)
		if protectErr != nil {
			return protectErr
		}
		requestCipher, protectErr := s.protector.Encrypt(tx, mustMarshal(command))
		if protectErr != nil {
			return protectErr
		}
		if err := s.repo.CreateAction(tx, newAction(ctx, value, "CREATE", "CUSTOMER", actor.AccountID, command.IdempotencyKey, hash, requestCipher, responseCipher, now)); err != nil {
			return err
		}
		result = &v
		return nil
	})
	if err != nil {
		if existing, findErr := s.repo.FindCreateAction(ctx, actor, command.IdempotencyKey); findErr == nil {
			if existing.CreateRequestHash != hash {
				return nil, ErrIdempotencyConflict
			}
			return s.view(ctx, existing)
		}
		return nil, err
	}
	return result, nil
}

func (s *Service) List(ctx context.Context, actor Actor, page, pageSize int) (ListResult, error) {
	if !validActor(actor) {
		return ListResult{}, ErrNotFound
	}
	page, pageSize = pagination.Normalize(page, pageSize)
	values, total, err := s.repo.ListOwned(ctx, actor, page, pageSize)
	if err != nil {
		return ListResult{}, err
	}
	result := ListResult{Items: make([]View, 0, len(values)), Page: page, PageSize: pageSize, Total: total}
	filingIDs := make([]uint64, 0, len(values))
	for i := range values {
		filingIDs = append(filingIDs, values[i].ID)
	}
	sections, err := s.repo.ListSectionsByFilingIDs(ctx, actor.TenantID, filingIDs)
	if err != nil {
		return ListResult{}, err
	}
	byFiling := map[uint64][]Section{}
	for _, section := range sections {
		byFiling[section.FilingID] = append(byFiling[section.FilingID], section)
	}
	for i := range values {
		view := basicView(&values[i])
		applyListSummary(ctx, s.protector, &view, byFiling[values[i].ID])
		result.Items = append(result.Items, view)
	}
	return result, nil
}

// applyListSummary 只从草稿 section 明文提取列表展示所需的单位名称与系统名称；
// 提取失败时保留空值，不因单条加密数据异常中断整个列表。
func applyListSummary(ctx context.Context, protector Protector, view *View, sections []Section) {
	for _, section := range sections {
		plain, err := protector.Decrypt(ctx, section.DataCipher)
		if err != nil {
			continue
		}
		var data map[string]any
		if err := json.Unmarshal(plain, &data); err != nil {
			continue
		}
		switch section.SectionCode {
		case SectionOrganization:
			if name, ok := data["unit_name"].(string); ok && view.UnitName == "" {
				view.UnitName = name
			}
		case SectionClassifiedObject:
			if name, ok := data["system_name"].(string); ok && view.SystemName == "" {
				view.SystemName = name
			}
		}
	}
}

func (s *Service) Get(ctx context.Context, actor Actor, publicID string) (*View, error) {
	if !validActor(actor) || !validPublicID(publicID) {
		return nil, ErrNotFound
	}
	value, err := s.repo.FindOwned(ctx, actor, strings.TrimSpace(publicID))
	if err != nil {
		return nil, err
	}
	return s.view(ctx, value)
}

func (s *Service) SaveSection(ctx context.Context, actor Actor, publicID, code string, command SaveSectionCommand) (*SectionView, error) {
	// 先按 schema 校验明文，再加密保存；幂等账本记录规范请求摘要而非敏感正文。
	publicID, code, command.IdempotencyKey = strings.TrimSpace(publicID), strings.TrimSpace(code), strings.TrimSpace(command.IdempotencyKey)
	if !validActor(actor) || !validPublicID(publicID) || !isSectionCode(code) || !validIdempotencyKey(command.IdempotencyKey) || len(command.Data) == 0 {
		return nil, ErrValidation
	}
	canonical, issues, err := parseAndValidateSection(code, command.Data)
	if err != nil {
		return nil, apperror.WithDetails(ErrValidation, err.Error())
	}
	requestHash := hashValue(struct {
		Expected uint64          `json:"expected_version"`
		Data     json.RawMessage `json:"data"`
	}{command.ExpectedVersion, canonical})
	var result SectionView
	err = s.repo.WithTransaction(ctx, func(tx context.Context) error {
		filing, findErr := s.repo.FindOwnedForUpdate(tx, actor, publicID)
		if findErr != nil {
			return findErr
		}
		actionName := "SECTION_SAVE:" + code
		if replay, replayErr := s.repo.FindAction(tx, actor.TenantID, filing.ID, actor.AccountID, actionName, command.IdempotencyKey); replayErr == nil {
			if replay.RequestHash != requestHash {
				return ErrIdempotencyConflict
			}
			plain, decryptErr := s.protector.Decrypt(tx, replay.ResponseCipher)
			if decryptErr != nil {
				return decryptErr
			}
			return json.Unmarshal(plain, &result)
		} else if !errors.Is(replayErr, ErrNotFound) {
			return replayErr
		}
		if filing.Status != StatusDraft {
			return ErrLocked
		}
		now := s.clock.Now().UTC()
		status := "VALID"
		if len(issues) > 0 {
			status = "INVALID"
		}
		cipher, protectErr := s.protector.Encrypt(tx, canonical)
		if protectErr != nil {
			return protectErr
		}
		section, sectionErr := s.repo.FindSection(tx, actor.TenantID, filing.ID, code)
		if errors.Is(sectionErr, ErrNotFound) {
			if command.ExpectedVersion != 0 {
				return ErrVersionConflict
			}
			section = &Section{TenantID: actor.TenantID, FilingID: filing.ID, SectionCode: code, SchemaVersion: FormVersion, DataCipher: cipher, ValidationStatus: status, UpdatedBy: actor.AccountID, CreatedAt: now, UpdatedAt: now, Version: 1}
			if err := s.repo.CreateSection(tx, section); err != nil {
				return err
			}
		} else if sectionErr != nil {
			return sectionErr
		} else {
			if section.Version != command.ExpectedVersion {
				return ErrVersionConflict
			}
			if err := s.repo.UpdateSection(tx, section, command.ExpectedVersion, cipher, status, actor.AccountID, now); err != nil {
				return err
			}
			section.DataCipher = cipher
			section.ValidationStatus = status
			section.UpdatedBy = actor.AccountID
			section.UpdatedAt = now
			section.Version++
		}
		sections, listErr := s.repo.ListSections(tx, actor.TenantID, filing.ID)
		if listErr != nil {
			return listErr
		}
		validCount, currentStep := 0, uint8(1)
		for _, item := range sections {
			if item.ValidationStatus == "VALID" {
				validCount++
			}
			if step := sectionStep(item.SectionCode); step > currentStep {
				currentStep = step
			}
		}
		if err := s.repo.UpdateFiling(tx, filing, filing.Version, map[string]any{"current_step": currentStep, "completion_pct": uint8(validCount * 100 / len(SectionCodes)), "updated_by": actor.AccountID, "updated_at": now}); err != nil {
			return err
		}
		result = sectionView(section, canonical, issues)
		responseJSON, _ := json.Marshal(result)
		responseCipher, protectErr := s.protector.Encrypt(tx, responseJSON)
		if protectErr != nil {
			return protectErr
		}
		requestCipher, protectErr := s.protector.Encrypt(tx, mustMarshal(struct {
			ExpectedVersion uint64          `json:"expected_version"`
			Data            json.RawMessage `json:"data"`
		}{command.ExpectedVersion, canonical}))
		if protectErr != nil {
			return protectErr
		}
		return s.repo.CreateAction(tx, newAction(ctx, filing, actionName, "CUSTOMER", actor.AccountID, command.IdempotencyKey, requestHash, requestCipher, responseCipher, now))
	})
	return &result, err
}

func (s *Service) SaveMatrix(ctx context.Context, actor Actor, publicID, code string, command SaveMatrixCommand) (*MatrixView, error) {
	publicID, code, command.RowCode, command.ColumnCode, command.IdempotencyKey = strings.TrimSpace(publicID), strings.TrimSpace(code), strings.TrimSpace(command.RowCode), strings.TrimSpace(command.ColumnCode), strings.TrimSpace(command.IdempotencyKey)
	if !validActor(actor) || !validPublicID(publicID) || !isMatrixCode(code) || !validIdempotencyKey(command.IdempotencyKey) {
		return nil, ErrValidation
	}
	if command.Selected && !validMatrixCell(command.RowCode, command.ColumnCode) || !command.Selected && (command.RowCode != "" || command.ColumnCode != "") {
		return nil, ErrValidation
	}
	requestHash := hashValue(command)
	var result MatrixView
	err := s.repo.WithTransaction(ctx, func(tx context.Context) error {
		filing, findErr := s.repo.FindOwnedForUpdate(tx, actor, publicID)
		if findErr != nil {
			return findErr
		}
		actionName := "MATRIX_SAVE:" + code
		if replay, replayErr := s.repo.FindAction(tx, actor.TenantID, filing.ID, actor.AccountID, actionName, command.IdempotencyKey); replayErr == nil {
			if replay.RequestHash != requestHash {
				return ErrIdempotencyConflict
			}
			plain, decryptErr := s.protector.Decrypt(tx, replay.ResponseCipher)
			if decryptErr != nil {
				return decryptErr
			}
			return json.Unmarshal(plain, &result)
		} else if !errors.Is(replayErr, ErrNotFound) {
			return replayErr
		}
		if filing.Status != StatusDraft {
			return ErrLocked
		}
		if filing.Version != command.ExpectedFilingVersion {
			return ErrVersionConflict
		}
		now := s.clock.Now().UTC()
		matrix, matrixErr := s.repo.FindMatrix(tx, actor.TenantID, filing.ID, code)
		if errors.Is(matrixErr, ErrNotFound) {
			if command.ExpectedMatrixVersion != 0 {
				return ErrVersionConflict
			}
			matrix = &MatrixSelection{TenantID: actor.TenantID, FilingID: filing.ID, MatrixCode: code, RowCode: command.RowCode, ColumnCode: command.ColumnCode, Selected: command.Selected, UpdatedBy: actor.AccountID, CreatedAt: now, UpdatedAt: now, Version: 1}
			if err := s.repo.CreateMatrix(tx, matrix); err != nil {
				return err
			}
		} else if matrixErr != nil {
			return matrixErr
		} else {
			if matrix.Version != command.ExpectedMatrixVersion {
				return ErrVersionConflict
			}
			if err := s.repo.UpdateMatrix(tx, matrix, matrix.Version, command.RowCode, command.ColumnCode, command.Selected, actor.AccountID, now); err != nil {
				return err
			}
			matrix.RowCode = command.RowCode
			matrix.ColumnCode = command.ColumnCode
			matrix.Selected = command.Selected
			matrix.UpdatedBy = actor.AccountID
			matrix.UpdatedAt = now
			matrix.Version++
		}
		if err := s.repo.UpdateFiling(tx, filing, filing.Version, map[string]any{"updated_by": actor.AccountID, "updated_at": now}); err != nil {
			return err
		}
		result = matrixView(matrix)
		responseJSON, _ := json.Marshal(result)
		responseCipher, protectErr := s.protector.Encrypt(tx, responseJSON)
		if protectErr != nil {
			return protectErr
		}
		requestCipher, protectErr := s.protector.Encrypt(tx, mustMarshal(command))
		if protectErr != nil {
			return protectErr
		}
		return s.repo.CreateAction(tx, newAction(ctx, filing, actionName, "CUSTOMER", actor.AccountID, command.IdempotencyKey, requestHash, requestCipher, responseCipher, now))
	})
	return &result, err
}

func (s *Service) Validate(ctx context.Context, actor Actor, publicID string) (ValidationResult, error) {
	if !validActor(actor) || !validPublicID(publicID) {
		return ValidationResult{}, ErrNotFound
	}
	filing, err := s.repo.FindOwned(ctx, actor, strings.TrimSpace(publicID))
	if err != nil {
		return ValidationResult{}, err
	}
	sections, err := s.repo.ListSections(ctx, actor.TenantID, filing.ID)
	if err != nil {
		return ValidationResult{}, err
	}
	matrices, err := s.repo.ListMatrices(ctx, actor.TenantID, filing.ID)
	if err != nil {
		return ValidationResult{}, err
	}
	return s.validateAggregate(ctx, sections, matrices)
}

func (s *Service) Submit(ctx context.Context, actor Actor, publicID string, command SubmitCommand) (*View, error) {
	// 提交在同一事务内校验聚合、生成规范快照、锁定草稿并写 outbox，保证投递引用永远指向已提交版本。
	publicID, command.IdempotencyKey = strings.TrimSpace(publicID), strings.TrimSpace(command.IdempotencyKey)
	if !validActor(actor) || !validPublicID(publicID) || !validIdempotencyKey(command.IdempotencyKey) {
		return nil, ErrValidation
	}
	requestHash := hashValue(command)
	var result View
	err := s.repo.WithTransaction(ctx, func(tx context.Context) error {
		filing, findErr := s.repo.FindOwnedForUpdate(tx, actor, publicID)
		if findErr != nil {
			return findErr
		}
		if replay, replayErr := s.repo.FindAction(tx, actor.TenantID, filing.ID, actor.AccountID, "SUBMIT", command.IdempotencyKey); replayErr == nil {
			if replay.RequestHash != requestHash {
				return ErrIdempotencyConflict
			}
			plain, decryptErr := s.protector.Decrypt(tx, replay.ResponseCipher)
			if decryptErr != nil {
				return decryptErr
			}
			return json.Unmarshal(plain, &result)
		} else if !errors.Is(replayErr, ErrNotFound) {
			return replayErr
		}
		if filing.Status != StatusDraft {
			return ErrLocked
		}
		if filing.Version != command.ExpectedVersion {
			return ErrVersionConflict
		}
		sections, listErr := s.repo.ListSections(tx, actor.TenantID, filing.ID)
		if listErr != nil {
			return listErr
		}
		matrices, listErr := s.repo.ListMatrices(tx, actor.TenantID, filing.ID)
		if listErr != nil {
			return listErr
		}
		materials, listErr := s.repo.ListMaterials(tx, actor.TenantID, filing.ID)
		if listErr != nil {
			return listErr
		}
		validation, validateErr := s.validateAggregate(tx, sections, matrices)
		if validateErr != nil {
			return validateErr
		}
		if !validation.Valid {
			return apperror.WithDetails(ErrValidation, validation.Issues)
		}
		if materialIssues := requiredMaterialIssues(tx, s.protector, sections, materials); len(materialIssues) > 0 {
			return apperror.WithDetails(ErrValidation, materialIssues)
		}
		canonical, marshalErr := s.canonicalSnapshot(tx, sections, matrices)
		if marshalErr != nil {
			return marshalErr
		}
		digest := sha256.Sum256(canonical)
		canonicalCipher, protectErr := s.protector.Encrypt(tx, canonical)
		if protectErr != nil {
			return protectErr
		}
		sequence, seqErr := s.repo.NextSubmissionSequence(tx, actor.TenantID, filing.ID)
		if seqErr != nil {
			return seqErr
		}
		now := s.clock.Now().UTC()
		snapshot := &SubmissionSnapshot{TenantID: actor.TenantID, FilingID: filing.ID, Sequence: sequence, FormVersion: FormVersion, CanonicalCipher: canonicalCipher, SnapshotSHA256: hex.EncodeToString(digest[:]), SubmittedBy: actor.AccountID, SubmittedAt: now}
		if err := s.repo.CreateSubmission(tx, snapshot); err != nil {
			return err
		}
		outboxPayload, marshalErr := json.Marshal(struct {
			EventType     string `json:"event_type"`
			FilingID      string `json:"filing_id"`
			FilingNo      string `json:"filing_no"`
			FormVersion   string `json:"form_version"`
			SubmissionSeq uint64 `json:"submission_sequence"`
			SnapshotHash  string `json:"snapshot_sha256"`
		}{"FILING_READY_FOR_SUBMISSION", filing.PublicID, filing.FilingNo, filing.FormVersion, sequence, snapshot.SnapshotSHA256})
		if marshalErr != nil {
			return marshalErr
		}
		payloadDigest := sha256.Sum256(outboxPayload)
		if err := s.repo.CreateSubmissionOutbox(tx, &SubmissionOutbox{
			EventID: s.ids.NewID(), TenantID: actor.TenantID, FilingID: filing.ID, SubmissionID: snapshot.ID,
			ContractVersion: "portal.filing.submission-reference.v1", Payload: outboxPayload,
			PayloadSHA256: hex.EncodeToString(payloadDigest[:]), Status: "WAITING_CONTRACT", CreatedAt: now,
		}); err != nil {
			return err
		}
		if err := s.repo.UpdateFiling(tx, filing, filing.Version, map[string]any{"status": StatusWaitingContract, "completion_pct": 100, "submitted_at": now, "locked_at": now, "updated_by": actor.AccountID, "updated_at": now}); err != nil {
			return err
		}
		filing.Status = StatusWaitingContract
		filing.CompletionPct = 100
		filing.SubmittedAt = &now
		filing.LockedAt = &now
		filing.UpdatedAt = now
		filing.UpdatedBy = actor.AccountID
		result = basicView(filing)
		result.Submission = &SubmissionView{Sequence: sequence, SnapshotSHA256: snapshot.SnapshotSHA256, SubmittedAt: now}
		responseJSON, _ := json.Marshal(result)
		responseCipher, protectErr := s.protector.Encrypt(tx, responseJSON)
		if protectErr != nil {
			return protectErr
		}
		requestCipher, protectErr := s.protector.Encrypt(tx, mustMarshal(command))
		if protectErr != nil {
			return protectErr
		}
		return s.repo.CreateAction(tx, newAction(ctx, filing, "SUBMIT", "CUSTOMER", actor.AccountID, command.IdempotencyKey, requestHash, requestCipher, responseCipher, now))
	})
	return &result, err
}

// DeleteDraft 只允许客户删除本人尚未提交的草稿。已锁定/提交/提交失败等状态保留
// 不可变证据，禁止客户侧删除；删除在同一事务中完成：先追加 DELETE 审计动作，
// 再清理草稿子资源并软删除备案头，避免审计账本与草稿清理脱节。
func (s *Service) DeleteDraft(ctx context.Context, actor Actor, publicID string) (*View, error) {
	actor.TenantID, actor.AccountID, publicID = strings.TrimSpace(actor.TenantID), strings.TrimSpace(actor.AccountID), strings.TrimSpace(publicID)
	if !validActor(actor) || !validPublicID(publicID) {
		return nil, ErrValidation
	}
	requestHash := hashValue(map[string]string{"public_id": publicID})
	var result View
	err := s.repo.WithTransaction(ctx, func(tx context.Context) error {
		filing, findErr := s.repo.FindOwnedForUpdate(tx, actor, publicID)
		if findErr != nil {
			return findErr
		}
		if filing.Status != StatusDraft {
			return ErrInvalidState
		}
		now := s.clock.Now().UTC()
		result = basicView(filing)
		responseJSON, _ := json.Marshal(result)
		responseCipher, protectErr := s.protector.Encrypt(tx, responseJSON)
		if protectErr != nil {
			return protectErr
		}
		requestCipher, protectErr := s.protector.Encrypt(tx, mustMarshal(map[string]string{"public_id": publicID}))
		if protectErr != nil {
			return protectErr
		}
		if err := s.repo.CreateAction(tx, newAction(ctx, filing, "DELETE", "CUSTOMER", actor.AccountID, "delete:"+publicID, requestHash, requestCipher, responseCipher, now)); err != nil {
			return err
		}
		if err := s.repo.DeleteDraftData(tx, filing.TenantID, filing.ID); err != nil {
			return err
		}
		return s.repo.SoftDeleteFiling(tx, filing.TenantID, filing.ID, actor.AccountID, now)
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (s *Service) Unlock(ctx context.Context, actor InternalActor, publicID string, command UnlockCommand) (*View, error) {
	// 解锁是机器授权的内部动作，只开放后续草稿修改；历史提交快照和回执保持不可变。
	actor.TenantID, actor.ActorID, publicID, command.Reason, command.IdempotencyKey = strings.TrimSpace(actor.TenantID), strings.TrimSpace(actor.ActorID), strings.TrimSpace(publicID), strings.TrimSpace(command.Reason), strings.TrimSpace(command.IdempotencyKey)
	if actor.TenantID == "" || actor.ActorID == "" || command.CustomerID == 0 || !validPublicID(publicID) || len([]rune(command.Reason)) < 2 || len([]rune(command.Reason)) > 1000 || !validIdempotencyKey(command.IdempotencyKey) {
		return nil, ErrValidation
	}
	requestHash := hashValue(command)
	var result View
	err := s.repo.WithTransaction(ctx, func(tx context.Context) error {
		filing, findErr := s.repo.FindInternalForUpdate(tx, actor.TenantID, command.CustomerID, publicID)
		if findErr != nil {
			return findErr
		}
		if replay, replayErr := s.repo.FindAction(tx, actor.TenantID, filing.ID, actor.ActorID, "UNLOCK", command.IdempotencyKey); replayErr == nil {
			if replay.RequestHash != requestHash {
				return ErrIdempotencyConflict
			}
			plain, decryptErr := s.protector.Decrypt(tx, replay.ResponseCipher)
			if decryptErr != nil {
				return decryptErr
			}
			return json.Unmarshal(plain, &result)
		} else if !errors.Is(replayErr, ErrNotFound) {
			return replayErr
		}
		if filing.Status != StatusWaitingContract && filing.Status != StatusSubmissionFailed && filing.Status != StatusSubmitted {
			return ErrInvalidState
		}
		if filing.Version != command.ExpectedVersion {
			return ErrVersionConflict
		}
		now := s.clock.Now().UTC()
		if filing.Status == StatusWaitingContract {
			if err := s.repo.CancelWaitingSubmissionOutbox(tx, filing.TenantID, filing.ID, now); err != nil {
				return err
			}
		}
		reasonCipher, protectErr := s.protector.Encrypt(tx, []byte(command.Reason))
		if protectErr != nil {
			return protectErr
		}
		if err := s.repo.UpdateFiling(tx, filing, filing.Version, map[string]any{"status": StatusDraft, "locked_at": nil, "unlocked_at": now, "unlock_reason_cipher": reasonCipher, "updated_by": actor.ActorID, "updated_at": now}); err != nil {
			return err
		}
		filing.Status = StatusDraft
		filing.LockedAt = nil
		filing.UnlockedAt = &now
		filing.UnlockReasonCipher = reasonCipher
		filing.UpdatedAt = now
		filing.UpdatedBy = actor.ActorID
		result = basicView(filing)
		responseJSON, _ := json.Marshal(result)
		responseCipher, protectErr := s.protector.Encrypt(tx, responseJSON)
		if protectErr != nil {
			return protectErr
		}
		requestCipher, protectErr := s.protector.Encrypt(tx, mustMarshal(command))
		if protectErr != nil {
			return protectErr
		}
		return s.repo.CreateAction(tx, newAction(ctx, filing, "UNLOCK", "MACHINE", actor.ActorID, command.IdempotencyKey, requestHash, requestCipher, responseCipher, now))
	})
	return &result, err
}

func (s *Service) view(ctx context.Context, value *Filing) (*View, error) {
	sections, err := s.repo.ListSections(ctx, value.TenantID, value.ID)
	if err != nil {
		return nil, err
	}
	matrices, err := s.repo.ListMatrices(ctx, value.TenantID, value.ID)
	if err != nil {
		return nil, err
	}
	result := basicView(value)
	// 详情页标题也复用列表摘要，避免名称只存在加密 section 中时退回显示内部备案编号。
	applyListSummary(ctx, s.protector, &result, sections)
	materials, err := s.repo.ListMaterials(ctx, value.TenantID, value.ID)
	if err != nil {
		return nil, err
	}
	for index := range materials {
		result.Materials = append(result.Materials, materialView(&materials[index]))
	}
	sectionByCode := map[string]Section{}
	for _, item := range sections {
		sectionByCode[item.SectionCode] = item
	}
	for _, code := range SectionCodes {
		if item, ok := sectionByCode[code]; ok {
			plain, decryptErr := s.protector.Decrypt(ctx, item.DataCipher)
			if decryptErr != nil {
				return nil, decryptErr
			}
			_, issues, _ := parseAndValidateSection(code, plain)
			result.Sections = append(result.Sections, sectionView(&item, plain, issues))
		}
	}
	matrixByCode := map[string]MatrixSelection{}
	for _, item := range matrices {
		matrixByCode[item.MatrixCode] = item
	}
	for _, code := range MatrixCodes {
		if item, ok := matrixByCode[code]; ok {
			result.Matrices = append(result.Matrices, matrixView(&item))
		}
	}
	if snapshot, snapshotErr := s.repo.LatestSubmission(ctx, value.TenantID, value.ID); snapshotErr == nil {
		result.Submission = &SubmissionView{Sequence: snapshot.Sequence, SnapshotSHA256: snapshot.SnapshotSHA256, SubmittedAt: snapshot.SubmittedAt}
	} else if !errors.Is(snapshotErr, ErrNotFound) {
		return nil, snapshotErr
	}
	return &result, nil
}

func requiredMaterialIssues(ctx context.Context, protector Protector, sections []Section, materials []Material) []ValidationIssue {
	materialByCode := make(map[string]Material, len(materials))
	for _, material := range materials {
		materialByCode[material.MaterialCode] = material
	}
	sectionByCode := make(map[string]Section, len(sections))
	for _, section := range sections {
		sectionByCode[section.SectionCode] = section
	}
	requirements := map[string]string{}
	if item, ok := sectionByCode[SectionMaterials]; ok {
		plain, err := protector.Decrypt(ctx, item.DataCipher)
		if err == nil {
			var data map[string]any
			if json.Unmarshal(plain, &data) == nil {
				for field, code := range map[string]string{"topology_available": "NETWORK_TOPOLOGY", "security_governance_available": "SECURITY_GOVERNANCE", "security_design_available": "SECURITY_DESIGN", "security_products_available": "SECURITY_PRODUCTS", "security_services_available": "SECURITY_SERVICES", "authority_guidance_available": "AUTHORITY_GUIDANCE"} {
					if available, _ := data[field].(bool); available {
						requirements[code] = field
					}
				}
			}
		}
	}
	if item, ok := sectionByCode[SectionClassification]; ok {
		plain, err := protector.Decrypt(ctx, item.DataCipher)
		if err == nil {
			var data map[string]any
			if json.Unmarshal(plain, &data) == nil {
				if available, _ := data["classification_report_available"].(bool); available {
					requirements["CLASSIFICATION_REPORT"] = "classification_report_available"
				}
			}
		}
	}
	issues := []ValidationIssue{}
	for code, field := range requirements {
		material, ok := materialByCode[code]
		if !ok || material.ScanStatus != MaterialClean || strings.TrimSpace(material.ObjectVersion) == "" || strings.TrimSpace(material.ScanReference) == "" || material.ScannedAt == nil {
			issues = append(issues, ValidationIssue{Path: "materials." + field, Code: "CLEAN_MATERIAL_REQUIRED", Message: "declared material requires an immutable object version and successful security scan"})
		}
	}
	sortIssues(issues)
	return issues
}

func basicView(value *Filing) View {
	return View{ID: value.PublicID, FilingNo: value.FilingNo, ProjectID: value.ProjectID, FormVersion: value.FormVersion, Status: value.Status, CurrentStep: value.CurrentStep, CompletionPct: value.CompletionPct, SubmittedAt: value.SubmittedAt, LockedAt: value.LockedAt, UnlockedAt: value.UnlockedAt, Version: value.Version, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}
}
func sectionView(value *Section, plain []byte, issues []ValidationIssue) SectionView {
	return SectionView{Code: value.SectionCode, SchemaVersion: value.SchemaVersion, Data: json.RawMessage(append([]byte(nil), plain...)), ValidationStatus: value.ValidationStatus, ValidationIssues: issues, Version: value.Version, UpdatedAt: value.UpdatedAt}
}
func matrixView(value *MatrixSelection) MatrixView {
	return MatrixView{Code: value.MatrixCode, RowCode: value.RowCode, ColumnCode: value.ColumnCode, Selected: value.Selected, Version: value.Version, UpdatedAt: value.UpdatedAt}
}
func (s *Service) validateAggregate(ctx context.Context, sections []Section, matrices []MatrixSelection) (ValidationResult, error) {
	issues := []ValidationIssue{}
	sectionByCode := map[string]Section{}
	for _, item := range sections {
		sectionByCode[item.SectionCode] = item
	}
	plainByCode := map[string][]byte{}
	for _, code := range SectionCodes {
		item, ok := sectionByCode[code]
		if !ok {
			issues = append(issues, ValidationIssue{Path: "sections." + code, Code: "REQUIRED", Message: "section is required"})
			continue
		}
		plain, err := s.protector.Decrypt(ctx, item.DataCipher)
		if err != nil {
			return ValidationResult{}, err
		}
		plainByCode[code] = plain
		_, fieldIssues, parseErr := parseAndValidateSection(code, plain)
		if parseErr != nil {
			issues = append(issues, ValidationIssue{Path: "sections." + code, Code: "INVALID_SCHEMA", Message: parseErr.Error()})
			continue
		}
		for _, issue := range fieldIssues {
			issue.Path = "sections." + code + "." + issue.Path
			issues = append(issues, issue)
		}
	}
	matrixByCode := map[string]MatrixSelection{}
	for _, item := range matrices {
		matrixByCode[item.MatrixCode] = item
	}
	for _, code := range MatrixCodes {
		item, ok := matrixByCode[code]
		if !ok || !item.Selected || !validMatrixCell(item.RowCode, item.ColumnCode) {
			issues = append(issues, ValidationIssue{Path: "matrices." + code, Code: "REQUIRED", Message: "one valid matrix cell must be selected"})
		}
	}
	if classification, ok := plainByCode[SectionClassification]; ok {
		if report, reportOK := plainByCode[SectionClassificationReport]; reportOK {
			var left, right map[string]any
			leftDecoder := json.NewDecoder(strings.NewReader(string(classification)))
			leftDecoder.UseNumber()
			rightDecoder := json.NewDecoder(strings.NewReader(string(report)))
			rightDecoder.UseNumber()
			if leftDecoder.Decode(&left) == nil && rightDecoder.Decode(&right) == nil {
				for _, field := range []string{"business_information_level", "system_service_level", "final_level"} {
					leftLevel, leftOK := integerValue(left[field])
					rightLevel, rightOK := integerValue(right[field])
					if leftOK && rightOK && leftLevel != rightLevel {
						issues = append(issues, ValidationIssue{Path: "sections.CLASSIFICATION_REPORT." + field, Code: "CROSS_SECTION_MISMATCH", Message: "report level must match classification level"})
					}
				}
				issues = append(issues, matrixReportIssues(matrixByCode, right)...)
			}
		}
	}
	sortIssues(issues)
	return ValidationResult{Valid: len(issues) == 0, Issues: issues}, nil
}

func matrixReportIssues(matrices map[string]MatrixSelection, report map[string]any) []ValidationIssue {
	checks := []struct{ matrixCode, objectField, damageField, levelField string }{
		{MatrixBusinessInformation, "business_impact_object", "business_damage_degree", "business_information_level"},
		{MatrixSystemService, "service_impact_object", "service_damage_degree", "system_service_level"},
	}
	issues := []ValidationIssue{}
	for _, check := range checks {
		matrix, ok := matrices[check.matrixCode]
		if !ok || !matrix.Selected || !validMatrixCell(matrix.RowCode, matrix.ColumnCode) {
			continue
		}
		object, objectOK := report[check.objectField].(string)
		damage, damageOK := report[check.damageField].(string)
		level, levelOK := integerValue(report[check.levelField])
		expectedDamage := map[string]string{"GENERAL_DAMAGE": "GENERAL", "SERIOUS_DAMAGE": "SERIOUS", "EXTREME_DAMAGE": "EXTREME"}[matrix.ColumnCode]
		expectedLevel, cellOK := matrixLevel(matrix.RowCode, matrix.ColumnCode)
		prefix := "matrices." + check.matrixCode
		if !objectOK || object != matrix.RowCode {
			issues = append(issues, ValidationIssue{Path: prefix + ".row_code", Code: "REPORT_MISMATCH", Message: "matrix row must match the report impact object"})
		}
		if !damageOK || damage != expectedDamage {
			issues = append(issues, ValidationIssue{Path: prefix + ".column_code", Code: "REPORT_MISMATCH", Message: "matrix column must match the report damage degree"})
		}
		if cellOK && (!levelOK || level != expectedLevel) {
			issues = append(issues, ValidationIssue{Path: prefix, Code: "LEVEL_MISMATCH", Message: "matrix cell must derive the report component level"})
		}
	}
	return issues
}

func matrixLevel(row, column string) (int64, bool) {
	values := map[string]map[string]int64{
		"LEGAL_RIGHTS":      {"GENERAL_DAMAGE": 1, "SERIOUS_DAMAGE": 2, "EXTREME_DAMAGE": 2},
		"PUBLIC_INTEREST":   {"GENERAL_DAMAGE": 2, "SERIOUS_DAMAGE": 3, "EXTREME_DAMAGE": 4},
		"NATIONAL_SECURITY": {"GENERAL_DAMAGE": 4, "SERIOUS_DAMAGE": 5, "EXTREME_DAMAGE": 5},
	}
	columns, ok := values[row]
	if !ok {
		return 0, false
	}
	value, ok := columns[column]
	return value, ok
}

type snapshotSection struct {
	Code, SchemaVersion string
	Data                json.RawMessage
}
type snapshotMatrix struct{ Code, RowCode, ColumnCode string }
type snapshotDocument struct {
	FormVersion string            `json:"form_version"`
	Sections    []snapshotSection `json:"sections"`
	Matrices    []snapshotMatrix  `json:"matrices"`
}

func (s *Service) canonicalSnapshot(ctx context.Context, sections []Section, matrices []MatrixSelection) ([]byte, error) {
	sectionByCode := map[string]Section{}
	for _, item := range sections {
		sectionByCode[item.SectionCode] = item
	}
	matrixByCode := map[string]MatrixSelection{}
	for _, item := range matrices {
		matrixByCode[item.MatrixCode] = item
	}
	document := snapshotDocument{FormVersion: FormVersion, Sections: make([]snapshotSection, 0, len(SectionCodes)), Matrices: make([]snapshotMatrix, 0, len(MatrixCodes))}
	for _, code := range SectionCodes {
		item := sectionByCode[code]
		plain, err := s.protector.Decrypt(ctx, item.DataCipher)
		if err != nil {
			return nil, err
		}
		document.Sections = append(document.Sections, snapshotSection{Code: code, SchemaVersion: item.SchemaVersion, Data: json.RawMessage(plain)})
	}
	for _, code := range MatrixCodes {
		item := matrixByCode[code]
		document.Matrices = append(document.Matrices, snapshotMatrix{Code: code, RowCode: item.RowCode, ColumnCode: item.ColumnCode})
	}
	return json.Marshal(document)
}
func newAction(ctx context.Context, filing *Filing, action, actorType, actor, key, hash string, requestCipher, responseCipher []byte, at time.Time) *Action {
	return &Action{TenantID: filing.TenantID, FilingID: filing.ID, Action: action, ActorType: actorType, ActorID: actor, IdempotencyKey: key, RequestHash: hash, RequestCipher: requestCipher, RequestID: requestctx.ID(ctx), ResponseCipher: responseCipher, OccurredAt: at}
}

func mustMarshal(value any) []byte {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return raw
}
func validActor(actor Actor) bool {
	return strings.TrimSpace(actor.TenantID) != "" && strings.TrimSpace(actor.AccountID) != "" && actor.CustomerID > 0
}
func validGeneratedID(value string) bool {
	value = strings.TrimSpace(value)
	return len(value) >= 16 && len(value) <= 64 && value != "request-id-unavailable"
}
func validPublicID(value string) bool {
	value = strings.TrimSpace(value)
	return len(value) >= 8 && len(value) <= 64 && !strings.ContainsAny(value, "/\\\r\n")
}
func validIdempotencyKey(value string) bool {
	value = strings.TrimSpace(value)
	return len(value) >= 8 && len(value) <= 128 && !strings.ContainsAny(value, "\r\n")
}
func hashValue(value any) string {
	raw, _ := json.Marshal(value)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}
func sectionStep(code string) uint8 {
	for index, value := range SectionCodes {
		if code == value {
			return uint8(index + 1)
		}
	}
	return 1
}

// 即使仓储结果顺序不稳定，也固定校验问题排序，保证响应摘要和客户端展示可重复。
func sortIssues(issues []ValidationIssue) {
	sort.SliceStable(issues, func(i, j int) bool {
		return fmt.Sprint(issues[i].Path, issues[i].Code) < fmt.Sprint(issues[j].Path, issues[j].Code)
	})
}
