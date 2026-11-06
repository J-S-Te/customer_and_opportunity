package localidentity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/project"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/report"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/reportsecurity"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	requestctx "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ReportSource struct {
	Record
	OperationNo      string `gorm:"size:64;not null;uniqueIndex" json:"operation_no"`
	RequestID        uint64 `gorm:"not null;uniqueIndex" json:"request_id"`
	CustomerID       uint64 `gorm:"not null;index" json:"customer_id"`
	ProjectID        string `gorm:"size:64;not null" json:"project_id"`
	AccountID        string `gorm:"size:128;not null" json:"account_id"`
	Namespace        string `gorm:"size:32;not null" json:"namespace"`
	FileName         string `gorm:"size:255;not null" json:"file_name"`
	SHA256           string `gorm:"size:64;not null" json:"sha256"`
	Size             int64  `gorm:"not null" json:"size"`
	DescriptorCipher []byte `gorm:"type:varbinary(2048);not null" json:"-"`
	DescriptorHash   string `gorm:"size:64;not null" json:"-"`
}

// TableName 指定本地PDF来源台账，与项目系统回调坐标隔离。
// 参数：无。
// 返回值：固定表名。
func (ReportSource) TableName() string { return "portal_local_report_sources" }

type ReportSummary struct {
	RequestID uint64 `json:"request_id"`
	RequestNo string `json:"request_no"`
	Status    string `json:"status"`
}

type ReportInput struct {
	CustomerID     uint64 `json:"customer_id"`
	Namespace      string `json:"namespace"`
	ProjectID      string `json:"project_id"`
	AccountID      string `json:"account_id"`
	ReportType     string `json:"report_type"`
	Reason         string `json:"reason"`
	FileName       string `json:"file_name"`
	IdempotencyKey string `json:"-"`
}

// preparePDF 限制并核实原PDF内容，避免文件名或网关受理冒充安全发布。
// 参数：ctx为请求，name/content为原始文件。
// 返回值：有界字节和真实不可变描述符；结构或内容非法时失败。
func preparePDF(ctx context.Context, name string, content io.Reader) ([]byte, report.FileDescriptor, error) {
	if content == nil || !validText(name, 255, true) || filepath.Base(name) != name || !strings.EqualFold(filepath.Ext(name), ".pdf") {
		return nil, report.FileDescriptor{}, ErrInvalid
	}
	raw, err := io.ReadAll(io.LimitReader(content, (20<<20)+1))
	if err != nil {
		return nil, report.FileDescriptor{}, err
	}
	if len(raw) == 0 || len(raw) > 20<<20 {
		return nil, report.FileDescriptor{}, ErrInvalid
	}
	sum := sha256.Sum256(raw)
	d := report.FileDescriptor{SourceType: "PORTAL_LOCAL", FileName: name, MIME: "application/pdf", FileHash: hex.EncodeToString(sum[:]), Size: int64(len(raw))}
	if err := reportsecurity.ValidatePDF(ctx, raw, d); err != nil {
		return nil, report.FileDescriptor{}, ErrInvalid
	}
	return raw, d, nil
}

// CreateReport 建立真实本地申请并上传原PDF，保持INGEST_PENDING直到安全入库完成。
// 参数：ctx为报告管理员，input为精确客户账号与项目，content为待上传PDF。
// 返回值：原操作账本及真实申请；外部失败仍保留相同操作号供重试。
func (s *Service) CreateReport(ctx context.Context, input ReportInput, content io.Reader) (OperationResult, error) {
	p, err := s.principal(ctx, PermissionReportManage, true)
	if err != nil {
		return OperationResult{}, err
	}
	if s.Files == nil || s.DescriptorProtector == nil {
		return OperationResult{}, ErrUnavailable
	}
	if GuardCustomerInput(ModeLocal, input.Namespace, input.CustomerID) != nil || !localProjectCode.MatchString(input.ProjectID) || !validText(input.AccountID, 128, true) || !validText(input.ReportType, 64, true) || !validText(input.Reason, 2000, true) || !validText(input.IdempotencyKey, 128, true) {
		return OperationResult{}, ErrInvalid
	}
	raw, d, err := preparePDF(ctx, input.FileName, content)
	if err != nil {
		return OperationResult{}, err
	}
	hash := requestHash(struct {
		Input ReportInput
		Hash  string
		Size  int64
	}{input, d.FileHash, d.Size})
	var op Operation
	err = s.Repo.WithTransaction(ctx, func(tx context.Context) error {
		if _, err := s.Repo.lockCustomer(tx, p.TenantID, input.CustomerID); err != nil {
			return err
		}
		existing := s.Repo.tx(tx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND created_by=? AND idempotency_key=?", p.TenantID, p.UserID, input.IdempotencyKey).Take(&op).Error
		if existing == nil {
			if op.Kind != "REPORT" || op.RequestHash != hash {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(existing, gorm.ErrRecordNotFound) {
			return existing
		}
		if err := s.validateReportTarget(tx, p, input); err != nil {
			return err
		}
		number, err := s.token(16)
		if err != nil {
			return err
		}
		op = Operation{Record: s.record(p), OperationNo: number, IdempotencyKey: input.IdempotencyKey, RequestHash: hash, Kind: "REPORT", CustomerID: input.CustomerID, PlatformUserID: input.AccountID, Stage: "FILE_PENDING", Status: "RETRY_WAIT", SnapshotCipher: []byte{}}
		if err := s.Repo.tx(tx).Create(&op).Error; err != nil {
			return err
		}
		request := report.Request{ActorModel: report.ActorModel{TenantID: p.TenantID, CreatedBy: p.UserID, UpdatedBy: p.UserID, CreatedAt: s.now(), UpdatedAt: s.now(), Version: 1}, RequestNo: "PL" + number[:30], ProjectID: input.ProjectID, CustomerID: input.CustomerID, AccountID: input.AccountID, ReportType: input.ReportType, Reason: input.Reason, Status: report.StatusIngestPending, SubmittedAt: s.now(), IdempotencyKey: "portal-local-v1:" + number, RequestHash: hash, ReportValidityStatus: "ACTIVE"}
		if err := s.Repo.tx(tx).Create(&request).Error; err != nil {
			return err
		}
		source := ReportSource{Record: s.record(p), OperationNo: number, RequestID: request.ID, CustomerID: input.CustomerID, ProjectID: input.ProjectID, AccountID: input.AccountID, Namespace: LocalNamespace, FileName: input.FileName, SHA256: d.FileHash, Size: d.Size, DescriptorCipher: []byte{}}
		if err := s.Repo.tx(tx).Create(&source).Error; err != nil {
			return err
		}
		op.ResultID = source.ID
		if err := s.Repo.tx(tx).Model(&op).Update("result_id", source.ID).Error; err != nil {
			return err
		}
		event := report.StatusEvent{TenantID: p.TenantID, CustomerID: input.CustomerID, RequestID: request.ID, EventType: "REPORT_LOCAL_SOURCE_REGISTERED", Sequence: 1, ToStatus: report.StatusIngestPending, ActorType: "USER", ActorID: p.UserID, SourceKeyHash: requestHash("portal-local-v1:" + number), PayloadHash: hash, RequestTrace: requestctx.ID(tx), OccurredAt: s.now()}
		if err := s.Repo.tx(tx).Create(&event).Error; err != nil {
			return err
		}
		return s.audit(tx, p, "PORTAL_LOCAL_REPORT_PREPARED", "portal_local_operation", number)
	})
	if err != nil {
		return OperationResult{}, err
	}
	if op.Status == "COMPLETED" {
		return s.operationResult(ctx, op)
	}
	return s.uploadReport(ctx, p, op, raw, d)
}

// validateReportTarget 复核真实本地项目、已消费客户身份及精确MANUAL授权。
// 参数：ctx为事务，p为可信管理员，input为冻结目标。
// 返回值：目标不存在、跨客户或授权已撤销时失败。
func (s *Service) validateReportTarget(ctx context.Context, p auth.Principal, input ReportInput) error {
	var local LocalProject
	if err := s.Repo.tx(ctx).Where("tenant_id=? AND customer_id=? AND project_id=? AND namespace=?", p.TenantID, input.CustomerID, input.ProjectID, LocalNamespace).Take(&local).Error; err != nil {
		return ErrNotFound
	}
	a := AccountAdapter{Repo: s.Repo, TenantID: s.TenantID, HMACKey: s.HMACKey, Now: s.now}
	if err := a.ValidateCustomer(ctx, p.TenantID, input.CustomerID, input.AccountID, false); err != nil {
		return err
	}
	var binding project.AccountBinding
	if err := s.Repo.tx(ctx).Where("tenant_id=? AND customer_id=? AND project_id=? AND account_id=? AND source=? AND status='ACTIVE'", p.TenantID, input.CustomerID, input.ProjectID, input.AccountID, project.BindingSourceManual).Take(&binding).Error; err != nil {
		return ErrNotFound
	}
	return nil
}

// uploadReport 以原操作号上传同一PDF并冻结真实网关回执。
// 参数：ctx/p为管理请求，op为既有账本，raw/d为已验证原文件。
// 返回值：摄取队列受理结果；失败不改成报告已发放。
func (s *Service) uploadReport(ctx context.Context, p auth.Principal, op Operation, raw []byte, d report.FileDescriptor) (OperationResult, error) {
	var source ReportSource
	if err := s.Repo.tx(ctx).Where("tenant_id=? AND operation_no=?", p.TenantID, op.OperationNo).Take(&source).Error; err != nil {
		return OperationResult{}, err
	}
	if source.SHA256 != d.FileHash || source.Size != d.Size || source.FileName != d.FileName {
		return OperationResult{Operation: op}, ErrConflict
	}
	if op.Stage == "FILE_PENDING" {
		descriptor, err := s.Files.Upload(ctx, source.RequestID, op.OperationNo, source.FileName, bytes.NewReader(raw))
		if err != nil {
			return OperationResult{Operation: op}, ErrUnavailable
		}
		if descriptor.SourceType != "PORTAL_LOCAL" || descriptor.FileName != source.FileName || descriptor.FileHash != source.SHA256 || descriptor.Size != source.Size || descriptor.ObjectRef == "" || descriptor.MIME != "application/pdf" {
			return OperationResult{Operation: op}, ErrUnavailable
		}
		encoded, err := json.Marshal(descriptor)
		if err != nil {
			return OperationResult{}, err
		}
		cipher, err := s.DescriptorProtector.Encrypt(ctx, encoded)
		if err != nil || len(cipher) == 0 || len(cipher) > 2048 {
			return OperationResult{Operation: op}, ErrUnavailable
		}
		digest := sha256.Sum256(encoded)
		err = s.Repo.WithTransaction(ctx, func(tx context.Context) error {
			var current Operation
			if err := s.Repo.tx(tx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND id=?", p.TenantID, op.ID).Take(&current).Error; err != nil {
				return err
			}
			if current.Stage != "FILE_PENDING" {
				op = current
				return nil
			}
			if err := s.Repo.tx(tx).Model(&source).Updates(map[string]any{"descriptor_cipher": cipher, "descriptor_hash": base64.RawURLEncoding.EncodeToString(digest[:])}).Error; err != nil {
				return err
			}
			if err := s.Repo.tx(tx).Model(&current).Updates(map[string]any{"stage": "FILE_READY", "updated_at": s.now(), "version": gorm.Expr("version+1")}).Error; err != nil {
				return err
			}
			op.Stage = "FILE_READY"
			return nil
		})
		if err != nil {
			return OperationResult{Operation: op}, err
		}
	}
	return s.queueReport(ctx, p, &op)
}

// queueReport 事务化提交既有安全摄取任务，不伪造扫描、vault或签发结果。
// 参数：ctx/p为管理员，op为已冻结文件的原操作。
// 返回值：真实待摄取申请及操作完成回执；FILE_PENDING要求重传原文件。
func (s *Service) queueReport(ctx context.Context, p auth.Principal, op *Operation) (OperationResult, error) {
	err := s.Repo.WithTransaction(ctx, func(tx context.Context) error {
		if err := s.Repo.tx(tx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND id=?", p.TenantID, op.ID).Take(op).Error; err != nil {
			return err
		}
		if op.Status == "COMPLETED" {
			return nil
		}
		if op.Stage != "FILE_READY" {
			return ErrUnavailable
		}
		var source ReportSource
		if err := s.Repo.tx(tx).Where("tenant_id=? AND operation_no=? AND namespace=?", p.TenantID, op.OperationNo, LocalNamespace).Take(&source).Error; err != nil {
			return err
		}
		if err := s.validateReportTarget(tx, p, ReportInput{CustomerID: source.CustomerID, ProjectID: source.ProjectID, AccountID: source.AccountID}); err != nil {
			return err
		}
		if len(source.DescriptorCipher) == 0 || source.DescriptorHash == "" {
			return ErrUnavailable
		}
		event := sha256.Sum256([]byte("portal-local-v1:ingest:" + op.OperationNo))
		job := report.IngestJob{EventID: hex.EncodeToString(event[:]), TenantID: p.TenantID, CustomerID: source.CustomerID, RequestID: source.RequestID, ReportRevision: 1, DescriptorCipher: source.DescriptorCipher, DescriptorHash: source.DescriptorHash, Status: report.IngestPending, CreatedAt: s.now()}
		if err := s.Repo.tx(tx).Create(&job).Error; err != nil {
			return err
		}
		if err := s.Repo.tx(tx).Model(op).Updates(map[string]any{"status": "COMPLETED", "stage": "INGEST_QUEUED", "last_error": "", "updated_at": s.now(), "updated_by": p.UserID, "version": gorm.Expr("version+1")}).Error; err != nil {
			return err
		}
		op.Stage, op.Status = "INGEST_QUEUED", "COMPLETED"
		return s.audit(tx, p, "PORTAL_LOCAL_REPORT_INGEST_QUEUED", "portal_report_request", fmt.Sprint(source.RequestID))
	})
	if err != nil {
		return OperationResult{Operation: *op}, err
	}
	return s.operationResult(ctx, *op)
}

// RetryReportUpload 接收原操作号与同一PDF，不允许替换既有客户或项目。
// 参数：ctx为报告管理员，number/name/content为原操作及原文件。
// 返回值：真实操作或文件摘要冲突错误。
func (s *Service) RetryReportUpload(ctx context.Context, number, name string, content io.Reader) (OperationResult, error) {
	p, err := s.principal(ctx, PermissionReportManage, true)
	if err != nil {
		return OperationResult{}, err
	}
	if s.Files == nil || s.DescriptorProtector == nil {
		return OperationResult{}, ErrUnavailable
	}
	var op Operation
	if err := s.Repo.tx(ctx).Where("tenant_id=? AND operation_no=? AND kind='REPORT'", p.TenantID, number).Take(&op).Error; err != nil {
		return OperationResult{}, ErrNotFound
	}
	raw, d, err := preparePDF(ctx, name, content)
	if err != nil {
		return OperationResult{Operation: op}, err
	}
	return s.uploadReport(ctx, p, op, raw, d)
}
