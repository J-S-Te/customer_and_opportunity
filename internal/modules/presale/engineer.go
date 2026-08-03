package presale

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/pagination"
)

// 工程师池是 PMS 同步得到的租户级分派目录，不沿用申请数据范围，
// 也不接受调用者覆盖租户；能否查看仍由售前读取权限控制。
type EngineerListQuery struct {
	Keyword    string
	Department string
	Role       string
	Skill      string
	Page       int
	PageSize   int
}

type EngineerView struct {
	PersonID        string    `json:"person_id"`
	PersonName      string    `json:"person_name"`
	Department      string    `json:"department"`
	Role            string    `json:"role"`
	SkillTags       []string  `json:"skill_tags"`
	ValidFlag       bool      `json:"valid_flag"`
	SourceUpdatedAt time.Time `json:"source_updated_at"`
	SyncedAt        time.Time `json:"synced_at"`
}

type EngineerListPage struct {
	Items                []EngineerView `json:"items"`
	Page                 int            `json:"page"`
	PageSize             int            `json:"page_size"`
	Total                int64          `json:"total"`
	LastAttemptAt        *time.Time     `json:"last_attempt_at,omitempty"`
	LastSuccessfulSyncAt *time.Time     `json:"last_successful_sync_at,omitempty"`
	NextSyncAt           *time.Time     `json:"next_sync_at,omitempty"`
}

type EngineerSyncJobView struct {
	JobNo       string     `json:"job_no"`
	Status      string     `json:"status"`
	TriggerType string     `json:"trigger_type"`
	RequestedAt time.Time  `json:"requested_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
}

type EngineerDirectoryRepository interface {
	ListAssignableEngineers(context.Context, string, EngineerListQuery) (EngineerListPage, error)
	EnqueueEngineerSync(context.Context, string, string, string, string, time.Time) (*EngineerSyncJob, error)
}

type EngineerService struct {
	repo  EngineerDirectoryRepository
	clock Clock
	ids   IDGenerator
}

func NewEngineerService(repo EngineerDirectoryRepository, clock Clock, ids IDGenerator) *EngineerService {
	return &EngineerService{repo: repo, clock: clock, ids: ids}
}

func (s *EngineerService) List(ctx context.Context, actor Actor, query EngineerListQuery) (EngineerListPage, error) {
	if !actor.Can("presale.read") || strings.TrimSpace(actor.TenantID) == "" {
		return EngineerListPage{}, ErrForbidden
	}
	query.Page, query.PageSize = pagination.Normalize(query.Page, query.PageSize)
	query.Keyword = strings.TrimSpace(query.Keyword)
	query.Department = strings.TrimSpace(query.Department)
	query.Role = strings.TrimSpace(query.Role)
	query.Skill = strings.TrimSpace(query.Skill)
	if len([]rune(query.Keyword)) > 100 || len([]rune(query.Department)) > 128 || len([]rune(query.Role)) > 32 || len([]rune(query.Skill)) > 64 {
		return EngineerListPage{}, ErrInvalidInput
	}
	return s.repo.ListAssignableEngineers(ctx, actor.TenantID, query)
}

func (s *EngineerService) EnqueueSync(ctx context.Context, actor Actor, key string) (EngineerSyncJobView, error) {
	if !actor.Can("presale.engineer.sync") || strings.TrimSpace(actor.TenantID) == "" || strings.TrimSpace(actor.UserID) == "" {
		return EngineerSyncJobView{}, ErrForbidden
	}
	key = strings.TrimSpace(key)
	if key == "" || len(key) > 128 {
		return EngineerSyncJobView{}, ErrInvalidInput
	}
	if s.ids == nil {
		return EngineerSyncJobView{}, errors.New("engineer sync ID generator is unavailable")
	}
	digest := sha256.Sum256([]byte(actor.TenantID + "\x00" + actor.UserID + "\x00MANUAL"))
	job, err := s.repo.EnqueueEngineerSync(ctx, actor.TenantID, actor.UserID, key, hex.EncodeToString(digest[:]), s.clock.Now())
	if err != nil {
		return EngineerSyncJobView{}, err
	}
	return engineerSyncJobView(job), nil
}

func engineerSyncJobView(job *EngineerSyncJob) EngineerSyncJobView {
	return EngineerSyncJobView{JobNo: job.JobNo, Status: job.Status, TriggerType: job.TriggerType, RequestedAt: job.CreatedAt, StartedAt: job.StartedAt, FinishedAt: job.FinishedAt}
}
