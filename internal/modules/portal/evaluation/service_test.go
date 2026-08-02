package evaluation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/pagination"
)

type memoryRepository struct {
	values        []*ServiceEvaluation
	audits        []AuditLog
	alerts        []Alert
	notifications []Notification
	outbox        []Outbox
	aggregate     Aggregate
}

func (r *memoryRepository) WithTransaction(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}
func (r *memoryRepository) FindByIdempotencyKey(_ context.Context, actor Actor, key string) (*ServiceEvaluation, error) {
	for _, value := range r.values {
		if value.TenantID == actor.TenantID && value.CustomerID == actor.CustomerID && value.AccountID == actor.AccountID && value.CreateIdempotencyKey == key {
			return value, nil
		}
	}
	return nil, ErrNotFound
}
func (r *memoryRepository) FindByProject(_ context.Context, tenant string, customer uint64, project string) (*ServiceEvaluation, error) {
	for _, value := range r.values {
		if value.TenantID == tenant && value.CustomerID == customer && value.ProjectID == project {
			return value, nil
		}
	}
	return nil, ErrNotFound
}
func (r *memoryRepository) FindOwned(_ context.Context, actor Actor, publicID string) (*ServiceEvaluation, error) {
	for _, value := range r.values {
		if value.TenantID == actor.TenantID && value.CustomerID == actor.CustomerID && value.AccountID == actor.AccountID && value.PublicID == publicID {
			return value, nil
		}
	}
	return nil, ErrNotFound
}
func (r *memoryRepository) Create(_ context.Context, value *ServiceEvaluation) error {
	if existing, _ := r.FindByProject(context.Background(), value.TenantID, value.CustomerID, value.ProjectID); existing != nil {
		return errors.New("duplicate project")
	}
	value.ID = uint64(len(r.values) + 1)
	r.values = append(r.values, value)
	return nil
}
func (r *memoryRepository) CreateAuditLog(_ context.Context, value *AuditLog) error {
	r.audits = append(r.audits, *value)
	return nil
}
func (r *memoryRepository) CreateAlert(_ context.Context, value *Alert) error {
	for _, current := range r.alerts {
		if current.TenantID == value.TenantID && current.EvaluationID == value.EvaluationID && current.RuleCode == value.RuleCode {
			return errors.New("duplicate alert")
		}
	}
	r.alerts = append(r.alerts, *value)
	return nil
}
func (r *memoryRepository) CreateNotification(_ context.Context, value *Notification) error {
	r.notifications = append(r.notifications, *value)
	return nil
}
func (r *memoryRepository) CreateOutbox(_ context.Context, value *Outbox) error {
	r.outbox = append(r.outbox, *value)
	return nil
}
func (r *memoryRepository) Statistics(context.Context, string) (Aggregate, error) {
	return r.aggregate, nil
}
func (r *memoryRepository) ListLowScoreNotices(_ context.Context, tenant, status string, page, size int) (pagination.Page[LowScoreNoticeRow], error) {
	result := pagination.Page[LowScoreNoticeRow]{Items: []LowScoreNoticeRow{}, Page: page, PageSize: size}
	for _, notice := range r.notifications {
		if notice.TenantID != tenant || status != "" && notice.Status != status {
			continue
		}
		for _, value := range r.values {
			if value.ID == notice.EvaluationID && value.TenantID == tenant {
				result.Items = append(result.Items, LowScoreNoticeRow{NotificationID: notice.ID, EvaluationID: value.ID, PublicID: value.PublicID, EvaluationNo: value.EvaluationNo, ProjectID: value.ProjectID, ProfessionalScore: value.ProfessionalScore, ResponseScore: value.ResponseScore, ReportScore: value.ReportScore, AttitudeScore: value.AttitudeScore, AverageScore: value.AverageScore, Comment: value.Comment, Status: notice.Status, CreatedAt: notice.CreatedAt, ReadAt: notice.ReadAt})
			}
		}
	}
	result.Total = int64(len(result.Items))
	return result, nil
}
func (r *memoryRepository) FindLowScoreNoticeForUpdate(_ context.Context, tenant, publicID string) (*LowScoreNoticeRow, error) {
	page, _ := r.ListLowScoreNotices(context.Background(), tenant, "", 1, 100)
	for i := range page.Items {
		if page.Items[i].PublicID == publicID {
			return &page.Items[i], nil
		}
	}
	return nil, ErrNotFound
}
func (r *memoryRepository) MarkNoticeRead(_ context.Context, tenant string, noticeID uint64, actor string, at time.Time) error {
	for i := range r.notifications {
		if r.notifications[i].TenantID == tenant && r.notifications[i].ID == noticeID && r.notifications[i].Status == "UNREAD" {
			r.notifications[i].Status, r.notifications[i].ReadAt, r.notifications[i].ReadBy = "READ", &at, actor
		}
	}
	return nil
}

type projectStatuses map[string]string

func (p projectStatuses) Status(_ context.Context, tenant string, customer uint64, projectID string) (string, bool, error) {
	value, ok := p[fmt.Sprintf("%s:%d:%s", tenant, customer, projectID)]
	return value, ok, nil
}

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

type sequenceIDs struct{ value int }

func (i *sequenceIDs) NewID() string {
	i.value++
	return fmt.Sprintf("000000000000000000000000000000%02d", i.value)
}

func testService(repo *memoryRepository, statuses projectStatuses) *Service {
	return NewService(repo, statuses, fixedClock{time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)}, &sequenceIDs{})
}

func validCommand() SubmitCommand {
	return SubmitCommand{ProjectID: "P-1", ProfessionalScore: 5, ResponseScore: 4, ReportScore: 3, AttitudeScore: 5, Comment: "服务很好", IdempotencyKey: "evaluation-key-1"}
}

func TestEligibilityIsCustomerScopedAndRequiresAuthoritativeCompletedStatus(t *testing.T) {
	repo := &memoryRepository{}
	statuses := projectStatuses{
		"tenant-a:7:P-complete": "COMPLETED",
		"tenant-a:7:P-running":  "IN_PROGRESS",
		"tenant-a:7:P-chinese":  "已完成",
	}
	service := testService(repo, statuses)
	actor := Actor{TenantID: "tenant-a", CustomerID: 7, AccountID: "sub-a"}
	for projectID, want := range map[string]bool{"P-complete": true, "P-running": false, "P-chinese": false} {
		value, err := service.Eligibility(context.Background(), actor, projectID)
		if err != nil || value.Eligible != want {
			t.Fatalf("project=%s value=%+v err=%v", projectID, value, err)
		}
	}
	if _, err := service.Eligibility(context.Background(), Actor{TenantID: "tenant-a", CustomerID: 8, AccountID: "sub-b"}, "P-complete"); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("another customer eligibility error=%v", err)
	}
}

func TestSubmitRechecksCompletionAndRejectsInvalidScores(t *testing.T) {
	repo := &memoryRepository{}
	service := testService(repo, projectStatuses{"tenant-a:7:P-1": "IN_PROGRESS"})
	actor := Actor{TenantID: "tenant-a", CustomerID: 7, AccountID: "sub-a"}
	if _, err := service.Submit(context.Background(), actor, validCommand()); !errors.Is(err, ErrProjectNotEligible) {
		t.Fatalf("incomplete project error=%v", err)
	}
	command := validCommand()
	command.ResponseScore = 0
	if _, err := service.Submit(context.Background(), actor, command); !errors.Is(err, ErrValidation) {
		t.Fatalf("invalid score error=%v", err)
	}
}

func TestSubmitIsActorScopedIdempotentAndProjectCanBeEvaluatedOnlyOnce(t *testing.T) {
	repo := &memoryRepository{}
	service := testService(repo, projectStatuses{"tenant-a:7:P-1": "COMPLETED"})
	actor := Actor{TenantID: "tenant-a", CustomerID: 7, AccountID: "sub-a"}
	command := validCommand()
	first, err := service.Submit(context.Background(), actor, command)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Submit(context.Background(), actor, command)
	if err != nil || second.ID != first.ID || len(repo.values) != 1 {
		t.Fatalf("replay=%+v err=%v values=%d", second, err, len(repo.values))
	}
	changed := command
	changed.Comment = "changed"
	if _, err := service.Submit(context.Background(), actor, changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed replay error=%v", err)
	}
	other := Actor{TenantID: "tenant-a", CustomerID: 7, AccountID: "sub-b"}
	otherKey := command
	otherKey.IdempotencyKey = "evaluation-key-2"
	if _, err := service.Submit(context.Background(), other, otherKey); !errors.Is(err, ErrAlreadyEvaluated) {
		t.Fatalf("second account error=%v", err)
	}
	if _, err := service.Get(context.Background(), other, first.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another account read error=%v", err)
	}
}

type concurrentWinnerRepository struct {
	*memoryRepository
	winner *ServiceEvaluation
}

func (r *concurrentWinnerRepository) Create(_ context.Context, attempted *ServiceEvaluation) error {
	winner := *attempted
	winner.ID = 91
	r.values = append(r.values, &winner)
	r.winner = &winner
	return errors.New("duplicate key after concurrent winner committed")
}

func TestConcurrentIdempotentWinnerIsReturnedAfterUniqueKeyRace(t *testing.T) {
	base := &memoryRepository{}
	repo := &concurrentWinnerRepository{memoryRepository: base}
	service := NewService(repo, projectStatuses{"tenant-a:7:P-1": "COMPLETED"}, fixedClock{time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)}, &sequenceIDs{})
	actor := Actor{TenantID: "tenant-a", CustomerID: 7, AccountID: "sub-a"}
	value, err := service.Submit(context.Background(), actor, validCommand())
	if err != nil || repo.winner == nil || value.ID != repo.winner.PublicID || len(repo.alerts) != 0 {
		t.Fatalf("value=%+v winner=%+v err=%v alerts=%d", value, repo.winner, err, len(repo.alerts))
	}
}

func TestLowScoreCreatesOneTransactionalInternalAlertProjection(t *testing.T) {
	repo := &memoryRepository{}
	service := testService(repo, projectStatuses{"tenant-a:7:P-1": "COMPLETED"})
	command := validCommand()
	command.ResponseScore = 2
	command.Comment = "<script>alert(1)</script>\x00"
	value, err := service.Submit(context.Background(), Actor{TenantID: "tenant-a", CustomerID: 7, AccountID: "sub-a"}, command)
	if err != nil {
		t.Fatal(err)
	}
	if value.AverageScore != "3.75" || value.TotalScore != 15 {
		t.Fatalf("average=%s total=%d", value.AverageScore, value.TotalScore)
	}
	if strings.Contains(value.Comment, "<") || strings.ContainsRune(value.Comment, '\x00') {
		t.Fatalf("comment was not sanitized: %q", value.Comment)
	}
	if len(repo.audits) != 1 || len(repo.alerts) != 1 || len(repo.notifications) != 1 || len(repo.outbox) != 1 || repo.notifications[0].Status != "UNREAD" {
		t.Fatalf("audits=%d alerts=%d notifications=%d outbox=%d", len(repo.audits), len(repo.alerts), len(repo.notifications), len(repo.outbox))
	}
	if _, err := service.Submit(context.Background(), Actor{TenantID: "tenant-a", CustomerID: 7, AccountID: "sub-a"}, command); err != nil || len(repo.alerts) != 1 {
		t.Fatalf("replay err=%v alerts=%d", err, len(repo.alerts))
	}
}

func TestPublicViewJSONDoesNotLeakInternalScopeOrIdempotency(t *testing.T) {
	value := &ServiceEvaluation{
		ID: 77, TenantID: "secret-tenant", CreatedBy: "secret-actor", UpdatedBy: "secret-updater", Version: 9,
		PublicID: "public-id", EvaluationNo: "EV-1", CustomerID: 8, AccountID: "secret-sub", ProjectID: "P-1",
		ProfessionalScore: 5, ResponseScore: 5, ReportScore: 5, AttitudeScore: 5, TotalScore: 20, AverageScore: "5.00",
		CreateIdempotencyKey: "secret-key", CreateRequestHash: "secret-hash",
	}
	raw, err := json.Marshal(publicView(value))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, secret := range []string{"secret-tenant", "secret-actor", "secret-updater", "secret-sub", "secret-key", "secret-hash", "customer_id", "account_id", "created_by", "updated_by", "version"} {
		if strings.Contains(text, secret) {
			t.Fatalf("public view leaked %q: %s", secret, text)
		}
	}
}

func TestStatisticsFailClosedBelowFiveAndUsesDeterministicAverages(t *testing.T) {
	repo := &memoryRepository{aggregate: Aggregate{Count: 4, ProfessionalSum: 20, ResponseSum: 20, ReportSum: 20, AttitudeSum: 20}}
	service := testService(repo, nil)
	if _, err := service.Statistics(context.Background(), "tenant-a"); !errors.Is(err, ErrStatisticsUnavailable) {
		t.Fatalf("small cohort error=%v", err)
	}
	repo.aggregate = Aggregate{Count: 5, ProfessionalSum: 23, ResponseSum: 20, ReportSum: 19, AttitudeSum: 21}
	value, err := service.Statistics(context.Background(), "tenant-a")
	if err != nil || value.SampleSize != 5 || value.ProfessionalAverage != "4.60" || value.OverallAverage != "4.15" {
		t.Fatalf("statistics=%+v err=%v", value, err)
	}
}

func TestLowScoreNoticeListAndReadAreTenantScopedAndIdempotent(t *testing.T) {
	now := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	repo := &memoryRepository{
		values: []*ServiceEvaluation{
			{ID: 1, TenantID: "tenant-a", PublicID: "evaluation-a", EvaluationNo: "EV-A", ProjectID: "P-A", ProfessionalScore: 2, ResponseScore: 4, ReportScore: 4, AttitudeScore: 4, AverageScore: "3.50", Comment: "please improve"},
			{ID: 2, TenantID: "tenant-b", PublicID: "evaluation-b", EvaluationNo: "EV-B", ProjectID: "P-B", ProfessionalScore: 1},
		},
		notifications: []Notification{
			{ID: 10, TenantID: "tenant-a", EvaluationID: 1, Kind: "LOW_SCORE", Status: "UNREAD", CreatedAt: now},
			{ID: 20, TenantID: "tenant-b", EvaluationID: 2, Kind: "LOW_SCORE", Status: "UNREAD", CreatedAt: now},
		},
	}
	service := testService(repo, nil)
	page, err := service.ListLowScoreNotices(context.Background(), "tenant-a", "UNREAD", 1, 20)
	if err != nil || page.Total != 1 || page.Items[0].ID != "evaluation-a" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	if _, err := service.ReadLowScoreNotice(context.Background(), "tenant-a", "machine:manager", "evaluation-b"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant read error=%v", err)
	}
	value, err := service.ReadLowScoreNotice(context.Background(), "tenant-a", "machine:manager", "evaluation-a")
	if err != nil || value.Status != "READ" || len(repo.audits) != 1 || repo.notifications[0].ReadBy != "machine:manager" {
		t.Fatalf("value=%+v err=%v audits=%d notification=%+v", value, err, len(repo.audits), repo.notifications[0])
	}
	if _, err := service.ReadLowScoreNotice(context.Background(), "tenant-a", "machine:manager", "evaluation-a"); err != nil || len(repo.audits) != 1 {
		t.Fatalf("idempotent replay err=%v audits=%d", err, len(repo.audits))
	}
}

func TestLowScoreNoticeJSONDoesNotLeakInternalIdentifiers(t *testing.T) {
	value := noticeView(LowScoreNoticeRow{NotificationID: 77, EvaluationID: 88, PublicID: "evaluation-public", EvaluationNo: "EV-PUBLIC", ProjectID: "P-1", ProfessionalScore: 2, ResponseScore: 4, ReportScore: 4, AttitudeScore: 4, AverageScore: "3.50", Comment: "visible"})
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, secret := range []string{"notification_id", "evaluation_id", "customer_id", "account_id", "tenant_id", "actor_id", "request_id", "idempotency", "77", "88"} {
		if strings.Contains(text, secret) {
			t.Fatalf("notice DTO leaked %q: %s", secret, text)
		}
	}
}
