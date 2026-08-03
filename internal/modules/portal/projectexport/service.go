package projectexport

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/project"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/workerruntime"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	requestctx "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
)

var (
	ErrNotFound            = apperror.New(http.StatusNotFound, "PORTAL_PROJECT_EXPORT_NOT_FOUND", "project export not found")
	ErrInvalidRequest      = apperror.New(http.StatusBadRequest, "PORTAL_PROJECT_EXPORT_INVALID", "invalid project export request")
	ErrIdempotencyConflict = apperror.New(http.StatusConflict, "PORTAL_PROJECT_EXPORT_IDEMPOTENCY_CONFLICT", "idempotency key was used with another request")
	ErrNotReady            = apperror.New(http.StatusConflict, "PORTAL_PROJECT_EXPORT_NOT_READY", "project export is not ready")
	ErrInvalidGrant        = apperror.New(http.StatusNotFound, "PORTAL_PROJECT_EXPORT_GRANT_NOT_FOUND", "project export download authorization not found")
	ErrWorkerUnavailable   = apperror.New(http.StatusServiceUnavailable, "PORTAL_PROJECT_EXPORT_WORKER_UNAVAILABLE", "project export generation is temporarily unavailable")
)

const (
	grantActive    = "ACTIVE"
	grantUsed      = "USED"
	maxExportBytes = 2 << 20
)

type Actor struct {
	TenantID   string
	CustomerID uint64
	AccountID  string
}
type Clock interface{ Now() time.Time }
type IDGenerator interface{ NewID() string }
type ProjectReader interface {
	Get(context.Context, project.Scope, string) (*project.Detail, error)
}

type Repository interface {
	FindByKey(context.Context, Actor, string) (*Job, error)
	Create(context.Context, *Job, *Event) error
	FindOwned(context.Context, Actor, string, bool) (*Job, error)
	CreateGrant(context.Context, Actor, string, string, time.Time, time.Time, string) (*Grant, error)
	ConsumeGrant(context.Context, Actor, string, string, time.Time, string) (*Job, error)
	RecordDeliveryOutcome(context.Context, Actor, uint64, time.Time, string, bool, string) error
}

type Service struct {
	repo         Repository
	projects     ProjectReader
	clock        Clock
	ids          IDGenerator
	ttl          time.Duration
	readiness    workerruntime.Readiness
	workerMaxAge time.Duration
}

// UseWorkerReadiness 要求独立渲染 worker 最近有持久化心跳，才允许创建新导出任务。
func (s *Service) UseWorkerReadiness(readiness workerruntime.Readiness, maxAge time.Duration) *Service {
	s.readiness, s.workerMaxAge = readiness, maxAge
	return s
}

func (s *Service) workerReady(ctx context.Context) bool {
	// 生产 Portal 总会安装准入检查；允许零值仅为兼容领域嵌入与既有测试。
	if s.readiness == nil {
		return true
	}
	if s.clock == nil || s.workerMaxAge <= 0 {
		return false
	}
	ready, err := s.readiness.HasFreshHeartbeat(ctx, workerruntime.ProjectExportWorker, s.clock.Now().UTC().Add(-s.workerMaxAge))
	return err == nil && ready
}

func NewService(repo Repository, projects ProjectReader, clock Clock, ids IDGenerator, ttl time.Duration) *Service {
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	return &Service{repo: repo, projects: projects, clock: clock, ids: ids, ttl: ttl}
}

type CreateResult struct {
	PublicID, Status           string
	SourceUpdatedAt, CreatedAt time.Time
}
type StatusResult struct {
	PublicID, ProjectID, Status, FailureCode string
	SourceUpdatedAt, CreatedAt               time.Time
	CompletedAt                              *time.Time
}
type GrantResult struct {
	GrantID, DownloadToken string
	ExpiresAt              time.Time
}
type Download struct {
	FileName, MIME, FileHash string
	Bytes                    []byte
	complete                 func(context.Context, bool, string) error
}

// Complete 只记录 HTTP 服务写响应体时观察到的结果。
// 下载凭据在流传输前已原子消费，因此成功事件不等价于远端完整收到文件。
func (d *Download) Complete(ctx context.Context, success bool, reason string) error {
	if d == nil || d.complete == nil {
		return nil
	}
	return d.complete(ctx, success, strings.TrimSpace(reason))
}

func (s *Service) Create(ctx context.Context, actor Actor, projectID, idempotencyKey string) (CreateResult, error) {
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	// projectID 是上游不透明标识，空白具有语义，不能规范化成另一个项目 ID。
	if !validActor(actor) || strings.TrimSpace(projectID) == "" || len(projectID) > 64 || idempotencyKey == "" || len(idempotencyKey) > 128 || s.repo == nil || s.projects == nil || s.clock == nil || s.ids == nil {
		return CreateResult{}, ErrInvalidRequest
	}
	hash := digest(projectID)
	if existing, err := s.repo.FindByKey(ctx, actor, idempotencyKey); err == nil {
		if existing.RequestHash != hash || existing.ProjectID != projectID {
			return CreateResult{}, ErrIdempotencyConflict
		}
		return createResult(existing), nil
	} else if !errors.Is(err, ErrNotFound) {
		return CreateResult{}, err
	}
	if !s.workerReady(ctx) {
		return CreateResult{}, ErrWorkerUnavailable
	}
	detail, err := s.projects.Get(ctx, project.Scope{TenantID: actor.TenantID, CustomerID: actor.CustomerID}, projectID)
	if err != nil {
		return CreateResult{}, err
	}
	payload, err := json.Marshal(Capture{TenantID: actor.TenantID, CustomerID: actor.CustomerID, Detail: *detail})
	if err != nil || len(payload) > maxExportBytes {
		return CreateResult{}, ErrInvalidRequest
	}
	now := s.clock.Now().UTC()
	if !s.workerReady(ctx) {
		return CreateResult{}, ErrWorkerUnavailable
	}
	job := &Job{PublicID: s.ids.NewID(), TenantID: actor.TenantID, CustomerID: actor.CustomerID, AccountID: actor.AccountID, ProjectID: projectID, IdempotencyKey: idempotencyKey, RequestHash: hash, SnapshotJSON: payload, SourceUpdatedAt: detail.Snapshot.SourceUpdatedAt.UTC(), Status: StatusPending, CreatedAt: now, UpdatedAt: now, Version: 1}
	event := &Event{TenantID: actor.TenantID, CustomerID: actor.CustomerID, AccountID: actor.AccountID, EventType: "EXPORT_REQUESTED", Result: "SUCCESS", RequestTrace: requestctx.ID(ctx), OccurredAt: now}
	if err = s.repo.Create(ctx, job, event); err != nil {
		if existing, findErr := s.repo.FindByKey(ctx, actor, idempotencyKey); findErr == nil {
			if existing.RequestHash != hash || existing.ProjectID != projectID {
				return CreateResult{}, ErrIdempotencyConflict
			}
			return createResult(existing), nil
		} else if !errors.Is(findErr, ErrNotFound) {
			return CreateResult{}, errors.Join(err, findErr)
		}
		return CreateResult{}, err
	}
	return createResult(job), nil
}

func (s *Service) Status(ctx context.Context, actor Actor, publicID string) (StatusResult, error) {
	if !validActor(actor) || strings.TrimSpace(publicID) == "" {
		return StatusResult{}, ErrNotFound
	}
	value, err := s.repo.FindOwned(ctx, actor, strings.TrimSpace(publicID), false)
	if err != nil {
		return StatusResult{}, err
	}
	return StatusResult{PublicID: value.PublicID, ProjectID: value.ProjectID, Status: value.Status, FailureCode: value.FailureCode, SourceUpdatedAt: value.SourceUpdatedAt, CreatedAt: value.CreatedAt, CompletedAt: value.CompletedAt}, nil
}

func (s *Service) CreateGrant(ctx context.Context, actor Actor, publicID string) (GrantResult, error) {
	// 只为 READY 且属于当前账号的任务签发一次性短效凭据，数据库仅保存令牌摘要。
	if !validActor(actor) || strings.TrimSpace(publicID) == "" || s.clock == nil || s.ids == nil {
		return GrantResult{}, ErrNotFound
	}
	value, err := s.repo.FindOwned(ctx, actor, strings.TrimSpace(publicID), false)
	if err != nil {
		return GrantResult{}, err
	}
	if value.Status != StatusReady {
		return GrantResult{}, ErrNotReady
	}
	token, err := randomToken()
	if err != nil {
		return GrantResult{}, err
	}
	now := s.clock.Now().UTC()
	expires := now.Add(s.ttl)
	grant, err := s.repo.CreateGrant(ctx, actor, value.PublicID, s.ids.NewID(), now, expires, digest(token))
	if err != nil {
		return GrantResult{}, err
	}
	return GrantResult{GrantID: grant.PublicID, DownloadToken: token, ExpiresAt: grant.ExpiresAt}, nil
}

func (s *Service) Download(ctx context.Context, actor Actor, publicID, token string) (Download, error) {
	// 凭据在返回文件字节前原子消费；并发请求中只有一个能进入传输阶段。
	publicID, token = strings.TrimSpace(publicID), strings.TrimSpace(token)
	if !validActor(actor) || publicID == "" || len(token) < 32 || len(token) > 256 || s.clock == nil {
		return Download{}, ErrInvalidGrant
	}
	now := s.clock.Now().UTC()
	value, err := s.repo.ConsumeGrant(ctx, actor, publicID, digest(token), now, requestctx.ID(ctx))
	if err != nil {
		return Download{}, err
	}
	if value.Status != StatusReady || value.FileSize <= 0 || value.FileSize > maxExportBytes || int64(len(value.FileBytes)) != value.FileSize || digestBytes(value.FileBytes) != value.FileHash {
		return Download{}, ErrNotReady
	}
	return Download{
		FileName: value.FileName, MIME: "application/pdf", FileHash: value.FileHash,
		Bytes: append([]byte(nil), value.FileBytes...),
		complete: func(doneCtx context.Context, success bool, reason string) error {
			return s.repo.RecordDeliveryOutcome(doneCtx, actor, value.ID, s.clock.Now().UTC(), requestctx.ID(doneCtx), success, reason)
		},
	}, nil
}

func validActor(actor Actor) bool {
	return strings.TrimSpace(actor.TenantID) != "" && actor.CustomerID != 0 && strings.TrimSpace(actor.AccountID) != "" && len(actor.AccountID) <= 128
}
func createResult(value *Job) CreateResult {
	return CreateResult{PublicID: value.PublicID, Status: value.Status, SourceUpdatedAt: value.SourceUpdatedAt, CreatedAt: value.CreatedAt}
}
func digest(value string) string      { return digestBytes([]byte(value)) }
func digestBytes(value []byte) string { sum := sha256.Sum256(value); return hex.EncodeToString(sum[:]) }
func randomToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}
