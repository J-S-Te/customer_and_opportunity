package evaluation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/pagination"
	requestctx "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
)

var (
	ErrNotFound              = apperror.New(http.StatusNotFound, "PORTAL_EVALUATION_NOT_FOUND", "evaluation not found")
	ErrProjectNotFound       = apperror.New(http.StatusNotFound, "PORTAL_EVALUATION_PROJECT_NOT_FOUND", "project not found")
	ErrValidation            = apperror.New(http.StatusUnprocessableEntity, "PORTAL_EVALUATION_VALIDATION_ERROR", "evaluation request is invalid")
	ErrProjectNotEligible    = apperror.New(http.StatusConflict, "PORTAL_EVALUATION_PROJECT_NOT_ELIGIBLE", "project is not eligible for evaluation")
	ErrAlreadyEvaluated      = apperror.New(http.StatusConflict, "PORTAL_EVALUATION_ALREADY_SUBMITTED", "project was already evaluated")
	ErrIdempotencyConflict   = apperror.New(http.StatusConflict, "PORTAL_EVALUATION_IDEMPOTENCY_CONFLICT", "idempotency key was used with another request")
	ErrStatisticsUnavailable = apperror.New(http.StatusNotFound, "PORTAL_EVALUATION_STATISTICS_UNAVAILABLE", "evaluation statistics are unavailable")
)

const minimumStatisticsSample int64 = 5

type Actor struct {
	TenantID, AccountID string
	CustomerID          uint64
}

type SubmitCommand struct {
	ProjectID         string
	ProfessionalScore uint8
	ResponseScore     uint8
	ReportScore       uint8
	AttitudeScore     uint8
	Comment           string
	IdempotencyKey    string
}

type ProjectAccess interface {
	Status(context.Context, string, uint64, string) (string, bool, error)
}

type Clock interface{ Now() time.Time }
type IDGenerator interface{ NewID() string }

type Repository interface {
	WithTransaction(context.Context, func(context.Context) error) error
	FindByIdempotencyKey(context.Context, Actor, string) (*ServiceEvaluation, error)
	FindByProject(context.Context, string, uint64, string) (*ServiceEvaluation, error)
	FindOwned(context.Context, Actor, string) (*ServiceEvaluation, error)
	Create(context.Context, *ServiceEvaluation) error
	CreateAuditLog(context.Context, *AuditLog) error
	CreateAlert(context.Context, *Alert) error
	CreateNotification(context.Context, *Notification) error
	CreateOutbox(context.Context, *Outbox) error
	Statistics(context.Context, string) (Aggregate, error)
	ListLowScoreNotices(context.Context, string, string, int, int) (pagination.Page[LowScoreNoticeRow], error)
	FindLowScoreNoticeForUpdate(context.Context, string, string) (*LowScoreNoticeRow, error)
	MarkNoticeRead(context.Context, string, uint64, string, time.Time) error
}

func (s *Service) ListLowScoreNotices(ctx context.Context, tenantID, status string, page, pageSize int) (pagination.Page[LowScoreNotice], error) {
	tenantID, status = strings.TrimSpace(tenantID), strings.TrimSpace(status)
	if tenantID == "" || status != "" && status != "UNREAD" && status != "READ" {
		return pagination.Page[LowScoreNotice]{}, ErrValidation
	}
	page, pageSize = pagination.Normalize(page, pageSize)
	values, err := s.repo.ListLowScoreNotices(ctx, tenantID, status, page, pageSize)
	result := pagination.Page[LowScoreNotice]{Items: make([]LowScoreNotice, 0, len(values.Items)), Page: values.Page, PageSize: values.PageSize, Total: values.Total}
	for _, value := range values.Items {
		result.Items = append(result.Items, noticeView(value))
	}
	return result, err
}

func (s *Service) ReadLowScoreNotice(ctx context.Context, tenantID, actorID, publicEvaluationID string) (*LowScoreNotice, error) {
	tenantID, actorID, publicEvaluationID = strings.TrimSpace(tenantID), strings.TrimSpace(actorID), strings.TrimSpace(publicEvaluationID)
	if tenantID == "" || actorID == "" || publicEvaluationID == "" || len(publicEvaluationID) > 64 {
		return nil, ErrValidation
	}
	var result LowScoreNotice
	err := s.repo.WithTransaction(ctx, func(tx context.Context) error {
		value, findErr := s.repo.FindLowScoreNoticeForUpdate(tx, tenantID, publicEvaluationID)
		if findErr != nil {
			return findErr
		}
		if value.Status == "UNREAD" {
			now := s.clock.Now().UTC()
			if updateErr := s.repo.MarkNoticeRead(tx, tenantID, value.NotificationID, actorID, now); updateErr != nil {
				return updateErr
			}
			value.Status, value.ReadAt = "READ", &now
			if auditErr := s.repo.CreateAuditLog(tx, &AuditLog{TenantID: tenantID, EvaluationID: value.EvaluationID, Action: "LOW_SCORE_NOTICE_READ", ActorID: actorID, RequestID: requestctx.ID(ctx), OccurredAt: now}); auditErr != nil {
				return auditErr
			}
		}
		result = noticeView(*value)
		return nil
	})
	return &result, err
}

func noticeView(value LowScoreNoticeRow) LowScoreNotice {
	return LowScoreNotice{ID: value.PublicID, EvaluationNo: value.EvaluationNo, ProjectID: value.ProjectID, ProfessionalScore: value.ProfessionalScore, ResponseScore: value.ResponseScore, ReportScore: value.ReportScore, AttitudeScore: value.AttitudeScore, AverageScore: value.AverageScore, Comment: value.Comment, Status: value.Status, CreatedAt: value.CreatedAt, ReadAt: value.ReadAt}
}

type Service struct {
	repo     Repository
	projects ProjectAccess
	clock    Clock
	ids      IDGenerator
}

func NewService(repo Repository, projects ProjectAccess, clock Clock, ids IDGenerator) *Service {
	return &Service{repo: repo, projects: projects, clock: clock, ids: ids}
}

func (s *Service) Eligibility(ctx context.Context, actor Actor, projectID string) (Eligibility, error) {
	projectID = strings.TrimSpace(projectID)
	if !validActor(actor) || !validProjectID(projectID) {
		return Eligibility{}, ErrProjectNotFound
	}
	status, found, err := s.projects.Status(ctx, actor.TenantID, actor.CustomerID, projectID)
	if err != nil {
		return Eligibility{}, err
	}
	if !found {
		return Eligibility{}, ErrProjectNotFound
	}
	if existing, findErr := s.repo.FindByProject(ctx, actor.TenantID, actor.CustomerID, projectID); findErr == nil {
		result := Eligibility{ProjectID: projectID, Eligible: false, ReasonCode: "ALREADY_EVALUATED"}
		if existing.AccountID == actor.AccountID {
			result.EvaluationID = existing.PublicID
		}
		return result, nil
	} else if !errors.Is(findErr, ErrNotFound) {
		return Eligibility{}, findErr
	}
	if status != "COMPLETED" {
		return Eligibility{ProjectID: projectID, Eligible: false, ReasonCode: "PROJECT_NOT_COMPLETED"}, nil
	}
	return Eligibility{ProjectID: projectID, Eligible: true, ReasonCode: "ELIGIBLE"}, nil
}

func (s *Service) Submit(ctx context.Context, actor Actor, command SubmitCommand) (*View, error) {
	normalizeSubmit(&command)
	if !validActor(actor) || !validSubmit(command) {
		return nil, ErrValidation
	}
	requestHash := submitHash(command)
	if existing, err := s.repo.FindByIdempotencyKey(ctx, actor, command.IdempotencyKey); err == nil {
		if existing.CreateRequestHash != requestHash {
			return nil, ErrIdempotencyConflict
		}
		result := publicView(existing)
		return &result, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	total := command.ProfessionalScore + command.ResponseScore + command.ReportScore + command.AttitudeScore
	now := s.clock.Now().UTC()
	publicID, number := s.ids.NewID(), "EV-"+s.ids.NewID()
	if !validGeneratedID(publicID) || len(number) <= 3 || len(number) > 48 {
		return nil, errors.New("secure evaluation identifier generation failed")
	}
	value := &ServiceEvaluation{
		TenantID: actor.TenantID, CreatedBy: actor.AccountID, UpdatedBy: actor.AccountID, CreatedAt: now, UpdatedAt: now, Version: 1,
		PublicID: publicID, EvaluationNo: number, CustomerID: actor.CustomerID, AccountID: actor.AccountID, ProjectID: command.ProjectID,
		ProfessionalScore: command.ProfessionalScore, ResponseScore: command.ResponseScore, ReportScore: command.ReportScore, AttitudeScore: command.AttitudeScore,
		TotalScore: total, AverageScore: fixedAverage(int64(total), 4), Comment: command.Comment, Status: StatusSubmitted, SubmittedAt: now,
		CreateIdempotencyKey: command.IdempotencyKey, CreateRequestHash: requestHash,
	}
	err := s.repo.WithTransaction(ctx, func(tx context.Context) error {
		// Eligibility is deliberately re-read inside the write transaction. The
		// browser's earlier GET is never trusted as an authorization decision.
		status, found, projectErr := s.projects.Status(tx, actor.TenantID, actor.CustomerID, command.ProjectID)
		if projectErr != nil {
			return projectErr
		}
		if !found {
			return ErrProjectNotFound
		}
		if status != "COMPLETED" {
			return ErrProjectNotEligible
		}
		if _, findErr := s.repo.FindByProject(tx, actor.TenantID, actor.CustomerID, command.ProjectID); findErr == nil {
			return ErrAlreadyEvaluated
		} else if !errors.Is(findErr, ErrNotFound) {
			return findErr
		}
		if createErr := s.repo.Create(tx, value); createErr != nil {
			return createErr
		}
		if auditErr := s.repo.CreateAuditLog(tx, &AuditLog{TenantID: actor.TenantID, EvaluationID: value.ID, Action: "SUBMITTED", ActorID: actor.AccountID, RequestID: requestctx.ID(ctx), OccurredAt: now}); auditErr != nil {
			return auditErr
		}
		if !isLowScore(command, total) {
			return nil
		}
		if alertErr := s.repo.CreateAlert(tx, &Alert{TenantID: actor.TenantID, EvaluationID: value.ID, RuleCode: lowScoreRule, Status: "TRIGGERED", TriggeredAt: now}); alertErr != nil {
			return alertErr
		}
		if noticeErr := s.repo.CreateNotification(tx, &Notification{TenantID: actor.TenantID, EvaluationID: value.ID, Kind: "LOW_SCORE", Status: "UNREAD", CreatedAt: now}); noticeErr != nil {
			return noticeErr
		}
		eventID := s.ids.NewID()
		if !validGeneratedID(eventID) {
			return errors.New("secure evaluation event identifier generation failed")
		}
		payload, marshalErr := json.Marshal(map[string]any{
			"evaluation_id": value.PublicID, "project_id": value.ProjectID,
			"rule_code": lowScoreRule, "total_score": value.TotalScore,
		})
		if marshalErr != nil {
			return marshalErr
		}
		return s.repo.CreateOutbox(tx, &Outbox{EventID: eventID, TenantID: actor.TenantID, EventType: "PORTAL_EVALUATION_LOW_SCORE", AggregateID: value.ID, Payload: payload, Status: "PENDING", CreatedAt: now})
	})
	if err != nil {
		// Resolve both unique-key races from durable state after the losing
		// transaction has rolled back.
		if existing, findErr := s.repo.FindByIdempotencyKey(ctx, actor, command.IdempotencyKey); findErr == nil {
			if existing.CreateRequestHash != requestHash {
				return nil, ErrIdempotencyConflict
			}
			result := publicView(existing)
			return &result, nil
		}
		if _, findErr := s.repo.FindByProject(ctx, actor.TenantID, actor.CustomerID, command.ProjectID); findErr == nil {
			return nil, ErrAlreadyEvaluated
		}
		return nil, err
	}
	result := publicView(value)
	return &result, nil
}

func (s *Service) Get(ctx context.Context, actor Actor, publicID string) (*View, error) {
	publicID = strings.TrimSpace(publicID)
	if !validActor(actor) || publicID == "" || len(publicID) > 64 {
		return nil, ErrNotFound
	}
	value, err := s.repo.FindOwned(ctx, actor, publicID)
	if err != nil {
		return nil, err
	}
	result := publicView(value)
	return &result, nil
}

// Statistics returns only one tenant-wide aggregate. No project, customer or
// account filters are accepted, preventing callers from drilling a cohort down
// below the five-evaluation anonymity threshold.
func (s *Service) Statistics(ctx context.Context, tenantID string) (*Statistics, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, ErrStatisticsUnavailable
	}
	value, err := s.repo.Statistics(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if value.Count < minimumStatisticsSample {
		return nil, ErrStatisticsUnavailable
	}
	total := value.ProfessionalSum + value.ResponseSum + value.ReportSum + value.AttitudeSum
	return &Statistics{
		SampleSize:          value.Count,
		ProfessionalAverage: fixedAverage(value.ProfessionalSum, value.Count),
		ResponseAverage:     fixedAverage(value.ResponseSum, value.Count),
		ReportAverage:       fixedAverage(value.ReportSum, value.Count),
		AttitudeAverage:     fixedAverage(value.AttitudeSum, value.Count),
		OverallAverage:      fixedAverage(total, value.Count*4),
	}, nil
}

func normalizeSubmit(command *SubmitCommand) {
	command.ProjectID = strings.TrimSpace(command.ProjectID)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.Comment = sanitizePlainText(command.Comment)
}

func validActor(actor Actor) bool {
	return actor.CustomerID > 0 && actor.TenantID != "" && actor.AccountID != "" &&
		strings.TrimSpace(actor.TenantID) == actor.TenantID && strings.TrimSpace(actor.AccountID) == actor.AccountID
}

func validSubmit(command SubmitCommand) bool {
	return validProjectID(command.ProjectID) && validScore(command.ProfessionalScore) && validScore(command.ResponseScore) &&
		validScore(command.ReportScore) && validScore(command.AttitudeScore) && validKey(command.IdempotencyKey) &&
		utf8.ValidString(command.Comment) && utf8.RuneCountInString(command.Comment) <= 2000
}

func validProjectID(value string) bool {
	return value != "" && len(value) <= 64 && strings.TrimSpace(value) == value
}
func validScore(value uint8) bool { return value >= 1 && value <= 5 }
func validKey(value string) bool {
	return len(value) >= 8 && len(value) <= 128 && strings.TrimSpace(value) == value
}
func validGeneratedID(value string) bool {
	return value != "" && len(value) <= 64 && strings.TrimSpace(value) == value
}

func isLowScore(command SubmitCommand, total uint8) bool {
	return command.ProfessionalScore <= 2 || command.ResponseScore <= 2 || command.ReportScore <= 2 || command.AttitudeScore <= 2 || total <= 8
}

func submitHash(command SubmitCommand) string {
	value := strings.Join([]string{
		command.ProjectID, strconv.Itoa(int(command.ProfessionalScore)), strconv.Itoa(int(command.ResponseScore)),
		strconv.Itoa(int(command.ReportScore)), strconv.Itoa(int(command.AttitudeScore)), command.Comment,
	}, "\x1f")
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

// sanitizePlainText prevents stored markup from becoming executable if a
// future UI accidentally changes rendering context. Vue still renders this
// value through text interpolation; v-html is intentionally not used.
func sanitizePlainText(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	var result strings.Builder
	for _, current := range strings.TrimSpace(value) {
		switch current {
		case '<':
			result.WriteRune('＜')
		case '>':
			result.WriteRune('＞')
		default:
			if !unicode.IsControl(current) || current == '\n' || current == '\t' {
				result.WriteRune(current)
			}
		}
	}
	return strings.TrimSpace(result.String())
}

func fixedAverage(sum, denominator int64) string {
	if denominator <= 0 {
		return "0.00"
	}
	hundredths := (sum*100 + denominator/2) / denominator
	return fmt.Sprintf("%d.%02d", hundredths/100, hundredths%100)
}
