package report

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/pagination"
)

func TestTransitionAllowed(t *testing.T) {
	tests := []struct {
		from, to Status
		allowed  bool
	}{{StatusSubmitted, StatusApproving, true}, {StatusSubmitted, StatusIssued, false}, {StatusApproving, StatusRejected, true}, {StatusApproving, StatusApprovedProcessing, true}, {StatusApprovedProcessing, StatusIssued, true}, {StatusApprovedProcessing, StatusProcessingFailed, true}, {StatusIssued, StatusApproving, false}}
	for _, tt := range tests {
		if got := transitionAllowed(tt.from, tt.to); got != tt.allowed {
			t.Errorf("transition %s -> %s = %v, want %v", tt.from, tt.to, got, tt.allowed)
		}
	}
}

func TestSourceHashSeparatesEventSources(t *testing.T) {
	if sourceHash("CREATE", "same-key") == sourceHash("CALLBACK", "same-key") {
		t.Fatal("different event sources must not share a deduplication hash")
	}
	if sourceHash("CALLBACK", "same-key") != sourceHash(" CALLBACK ", " same-key ") {
		t.Fatal("source hash must normalize surrounding whitespace")
	}
}

func TestRequestHashIsStableAndSensitiveToPayload(t *testing.T) {
	base := CreateCommand{ProjectID: "p1", ReportType: "FINAL", Reason: "delivery", ReceiveEmail: "A@EXAMPLE.COM"}
	if requestHash(base) != requestHash(CreateCommand{ProjectID: "p1", ReportType: "FINAL", Reason: "delivery", ReceiveEmail: "a@example.com"}) {
		t.Fatal("email normalization must produce stable hash")
	}
	changed := base
	changed.Reason = "another"
	if requestHash(base) == requestHash(changed) {
		t.Fatal("different payload must not share an idempotency request hash")
	}
}

type reportRepoStub struct {
	request        *Request
	file           *File
	ingestJob      *IngestJob
	update         map[string]any
	updateRuns     int
	events         []StatusEvent
	transactionErr error
	createErr      error
	findByKey      *Request
	findByKeyAfter int
	findByKeyCalls int
	eventErr       error
	findTenant     string
	findCustomer   uint64
	findRequestID  uint64
	eventTenant    string
	eventCustomer  uint64
	eventRequestID uint64
	seenSources    map[string]StatusEvent
	notifications  []Notification
	notification   *Notification
	readEvents     []NotificationReadEvent
	unreadCount    int64
}

func (r *reportRepoStub) WithTransaction(ctx context.Context, fn func(context.Context) error) error {
	if r.transactionErr != nil {
		return r.transactionErr
	}
	return fn(ctx)
}
func (r *reportRepoStub) Create(_ context.Context, value *Request) error {
	if r.createErr != nil {
		return r.createErr
	}
	if value.ID == 0 {
		value.ID = 7
	}
	return nil
}
func (r *reportRepoStub) List(context.Context, string, uint64, int, int) (pagination.Page[Request], error) {
	return pagination.Page[Request]{}, nil
}
func (r *reportRepoStub) Find(_ context.Context, tenant string, customer, requestID uint64) (*Request, error) {
	r.findTenant, r.findCustomer, r.findRequestID = tenant, customer, requestID
	return r.request, nil
}
func (r *reportRepoStub) FindByIdempotencyKey(context.Context, string, uint64, string) (*Request, error) {
	r.findByKeyCalls++
	if r.findByKey != nil && r.findByKeyCalls > r.findByKeyAfter {
		return r.findByKey, nil
	}
	return nil, ErrNotFound
}
func (r *reportRepoStub) FindForUpdate(context.Context, string, uint64) (*Request, error) {
	if r.request == nil {
		return nil, ErrNotFound
	}
	return r.request, nil
}
func (r *reportRepoStub) Update(_ context.Context, _ *Request, _ uint64, fields map[string]any) error {
	r.updateRuns++
	r.update = fields
	return nil
}
func (r *reportRepoStub) CreateFile(_ context.Context, file *File) error {
	r.file = file
	return nil
}
func (r *reportRepoStub) CreateIngestJob(_ context.Context, value *IngestJob) error {
	value.ID = 11
	r.ingestJob = value
	return nil
}
func (r *reportRepoStub) FindIngestJobForUpdate(context.Context, uint64) (*IngestJob, error) {
	if r.ingestJob == nil {
		return nil, ErrNotFound
	}
	return r.ingestJob, nil
}
func (r *reportRepoStub) UpdateIngestJob(_ context.Context, _ *IngestJob, fields map[string]any) error {
	if value, ok := fields["status"].(string); ok && r.ingestJob != nil {
		r.ingestJob.Status = value
	}
	return nil
}
func (r *reportRepoStub) CreateOutbox(context.Context, *Outbox) error { return nil }
func (r *reportRepoStub) CreateStatusEvent(_ context.Context, value *StatusEvent) error {
	if r.eventErr != nil {
		return r.eventErr
	}
	r.events = append(r.events, *value)
	return nil
}
func (r *reportRepoStub) FindStatusEventBySource(_ context.Context, tenant string, requestID uint64, sourceKeyHash string) (*StatusEvent, error) {
	value, ok := r.seenSources[tenant+"\x00"+sourceKeyHash]
	if !ok || requestID == 0 {
		return nil, ErrNotFound
	}
	return &value, nil
}
func (r *reportRepoStub) ListStatusEvents(_ context.Context, tenant string, customer, requestID uint64) ([]StatusEvent, error) {
	r.eventTenant, r.eventCustomer, r.eventRequestID = tenant, customer, requestID
	return append([]StatusEvent(nil), r.events...), nil
}
func (r *reportRepoStub) CreateNotification(_ context.Context, value *Notification) error {
	r.notifications = append(r.notifications, *value)
	return nil
}
func (r *reportRepoStub) ListNotifications(context.Context, Actor, bool, int, int) (pagination.Page[NotificationView], error) {
	return pagination.Page[NotificationView]{}, nil
}
func (r *reportRepoStub) CountUnreadNotifications(context.Context, Actor) (int64, error) {
	return r.unreadCount, nil
}
func (r *reportRepoStub) FindNotificationForUpdate(context.Context, Actor, uint64) (*Notification, error) {
	if r.notification == nil {
		return nil, ErrNotificationNotFound
	}
	return r.notification, nil
}
func (r *reportRepoStub) MarkNotificationRead(_ context.Context, value *Notification, at time.Time) error {
	value.Status, value.ReadAt = NotificationRead, &at
	return nil
}
func (r *reportRepoStub) CreateNotificationReadEvent(_ context.Context, value *NotificationReadEvent) error {
	r.readEvents = append(r.readEvents, *value)
	return nil
}
func (r *reportRepoStub) FindFile(context.Context, string, uint64) (*File, error) {
	return nil, ErrFileUnavailable
}
func (r *reportRepoStub) RevokeActiveGrants(context.Context, string, uint64, uint64, string, time.Time) error {
	return nil
}
func (r *reportRepoStub) CreateGrant(context.Context, *Grant) error { return nil }
func (r *reportRepoStub) FindGrantByIssueKeyForUpdate(context.Context, string, uint64, uint64, string, string) (*Grant, error) {
	return nil, ErrGrantNotFound
}
func (r *reportRepoStub) FindGrantForUpdate(context.Context, string, uint64, uint64, string, string) (*Grant, error) {
	return nil, ErrGrantNotFound
}
func (r *reportRepoStub) UpdateGrant(context.Context, *Grant, map[string]any) error { return nil }
func (r *reportRepoStub) CreateDownloadEvent(context.Context, *DownloadEvent) error { return nil }
func (r *reportRepoStub) CreateDownloadEventOnce(context.Context, *DownloadEvent) error {
	return nil
}

type projectAccessStub struct {
	allowed       bool
	called        int
	beforeLookup  func() bool
	orderViolated bool
}

func (s *projectAccessStub) Accessible(context.Context, string, uint64, string) (bool, error) {
	s.called++
	if s.beforeLookup != nil && !s.beforeLookup() {
		s.orderViolated = true
	}
	return s.allowed, nil
}

type emailProtectorStub struct{}

func (emailProtectorStub) Encrypt(context.Context, string) ([]byte, error) {
	return []byte("cipher"), nil
}

type idStub struct{ value string }

func (s idStub) NewID() string { return s.value }

type reportClock struct{ now time.Time }

func (c reportClock) Now() time.Time { return c.now }

type ingestorStub struct {
	input FileDescriptor
	err   error
}

func (s *ingestorStub) Encrypt(_ context.Context, value []byte) ([]byte, error) {
	_ = json.Unmarshal(value, &s.input)
	if s.err != nil {
		return nil, s.err
	}
	return append([]byte("cipher:"), value...), nil
}
func (s *ingestorStub) Decrypt(_ context.Context, value []byte) ([]byte, error) {
	return bytes.TrimPrefix(value, []byte("cipher:")), nil
}

type ingestorStubResult struct{ result IngestResult }

func (s *ingestorStubResult) Encrypt(context.Context, []byte) ([]byte, error) {
	return []byte("cipher"), nil
}
func (s *ingestorStubResult) Decrypt(context.Context, []byte) ([]byte, error) { return nil, nil }

func (s *ingestorStubResult) Ingest(context.Context, FileDescriptor) (IngestResult, error) {
	return s.result, nil
}

func (s *ingestorStub) Ingest(_ context.Context, input FileDescriptor) (IngestResult, error) {
	s.input = input
	if s.err != nil {
		return IngestResult{}, s.err
	}
	return IngestResult{
		ObjectKeyCipher: []byte("cipher"), ObjectVersion: "immutable-version-1",
		EncryptionKeyRef: "kms/key/1", EncryptionAlgorithm: "AES-256-GCM",
		ScanStatus: "CLEAN", ScanReference: "scan-1", ScannedAt: time.Date(2026, 7, 31, 23, 59, 0, 0, time.UTC),
		WatermarkStatus: "DOWNLOAD_TIME",
	}, nil
}

func callbackRequest() *Request {
	return &Request{ActorModel: ActorModel{ID: 7, TenantID: "tenant-1", Version: 3}, ProjectID: "project-1", CustomerID: 9, Status: StatusApproving, DownstreamRequestID: "PS-7"}
}

func validCallback() Callback {
	return Callback{IdempotencyKey: "cb-1", TenantID: "tenant-1", RequestID: 7, CustomerID: 9, ProjectID: "project-1", Version: 1, Status: StatusApprovedProcessing, DownstreamRequestID: "PS-7", ApprovalResult: "APPROVED"}
}

func TestCreateRecordsSubmittedEventInTransaction(t *testing.T) {
	now := time.Date(2026, 8, 1, 1, 2, 3, 0, time.UTC)
	repo := &reportRepoStub{}
	service := NewService(repo, &projectAccessStub{allowed: true}, emailProtectorStub{}, nil, reportClock{now: now}, idStub{value: "generated-id"})
	value, err := service.Create(context.Background(), Actor{TenantID: "tenant-1", CustomerID: 9, AccountID: "account-1"}, CreateCommand{ProjectID: "project-1", ReportType: "FINAL", Reason: "delivery", ReceiveEmail: "a@example.com", IdempotencyKey: "create-key"})
	if err != nil {
		t.Fatalf("Create() err=%v", err)
	}
	if value.Status != StatusSubmitted || len(repo.events) != 1 {
		t.Fatalf("value=%+v events=%+v", value, repo.events)
	}
	event := repo.events[0]
	if event.TenantID != "tenant-1" || event.CustomerID != 9 || event.RequestID != value.ID || event.EventType != "REPORT_SUBMITTED" || event.Sequence != 1 || event.FromStatus != "" || event.ToStatus != StatusSubmitted || event.ActorType != "CUSTOMER" || event.SourceKeyHash == "" || !event.OccurredAt.Equal(now) {
		t.Fatalf("event=%+v", event)
	}
	if event.PayloadHash != requestHash(CreateCommand{ProjectID: "project-1", ReportType: "FINAL", Reason: "delivery", ReceiveEmail: "a@example.com"}) {
		t.Fatalf("submitted event payload hash=%q", event.PayloadHash)
	}
}

func TestCreateNormalizesAndValidatesInputBeforePersistence(t *testing.T) {
	now := time.Now().UTC()
	repo := &reportRepoStub{}
	service := NewService(repo, &projectAccessStub{allowed: true}, emailProtectorStub{}, nil, reportClock{now: now}, idStub{value: "generated-id"})
	value, err := service.Create(context.Background(), Actor{TenantID: " tenant-1 ", CustomerID: 9, AccountID: " account-1 "}, CreateCommand{ProjectID: " project-1 ", ReportType: " FINAL ", Reason: " delivery\nnotes ", ReceiveEmail: " A@EXAMPLE.COM ", IdempotencyKey: " create-key "})
	if err != nil {
		t.Fatalf("Create() err=%v", err)
	}
	if value.TenantID != "tenant-1" || value.AccountID != "account-1" || value.ProjectID != "project-1" || value.ReportType != "FINAL" || value.Reason != "delivery\nnotes" || value.IdempotencyKey != "create-key" {
		t.Fatalf("value was not normalized: %+v", value)
	}

	for name, command := range map[string]CreateCommand{
		"invalid email": {ProjectID: "p", ReportType: "FINAL", Reason: "reason", ReceiveEmail: "not-an-email", IdempotencyKey: "key"},
		"long project":  {ProjectID: strings.Repeat("p", 65), ReportType: "FINAL", Reason: "reason", ReceiveEmail: "a@example.com", IdempotencyKey: "key"},
		"control type":  {ProjectID: "p", ReportType: "FINAL\x00", Reason: "reason", ReceiveEmail: "a@example.com", IdempotencyKey: "key"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, createErr := service.Create(context.Background(), Actor{TenantID: "tenant-1", CustomerID: 9, AccountID: "account-1"}, command); !errors.Is(createErr, ErrInvalidRequest) {
				t.Fatalf("Create() err=%v", createErr)
			}
		})
	}
}

func TestCreateConcurrentIdempotencyWinnerDoesNotCreateDuplicateEvent(t *testing.T) {
	cmd := CreateCommand{ProjectID: "project-1", ReportType: "FINAL", Reason: "delivery", ReceiveEmail: "a@example.com", IdempotencyKey: "create-key"}
	winner := &Request{ActorModel: ActorModel{ID: 22, TenantID: "tenant-1"}, CustomerID: 9, AccountID: "account-1", ProjectID: cmd.ProjectID, RequestHash: requestHash(cmd)}
	repo := &reportRepoStub{createErr: errors.New("duplicate key"), findByKey: winner, findByKeyAfter: 1}
	service := NewService(repo, &projectAccessStub{allowed: true}, emailProtectorStub{}, nil, reportClock{now: time.Now()}, idStub{value: "generated-id"})
	value, err := service.Create(context.Background(), Actor{TenantID: "tenant-1", CustomerID: 9, AccountID: "account-1"}, cmd)
	if err != nil || value != winner {
		t.Fatalf("Create() value=%+v err=%v", value, err)
	}
	if len(repo.events) != 0 {
		t.Fatalf("losing transaction leaked events: %+v", repo.events)
	}
	if repo.findByKeyCalls != 2 {
		t.Fatalf("race recovery lookups=%d want=2", repo.findByKeyCalls)
	}
}

func TestCreateChecksParentAccessBeforeIdempotencyReplay(t *testing.T) {
	cmd := CreateCommand{ProjectID: "project-1", ReportType: "FINAL", Reason: "delivery", ReceiveEmail: "a@example.com", IdempotencyKey: "create-key"}
	existing := &Request{ActorModel: ActorModel{ID: 22, TenantID: "tenant-1"}, CustomerID: 9, AccountID: "account-1", ProjectID: cmd.ProjectID, RequestHash: requestHash(cmd)}
	repo := &reportRepoStub{findByKey: existing}
	projects := &projectAccessStub{allowed: true, beforeLookup: func() bool { return repo.findByKeyCalls == 0 }}
	service := NewService(repo, projects, emailProtectorStub{}, nil, reportClock{now: time.Now()}, idStub{value: "generated-id"})
	value, err := service.Create(context.Background(), Actor{TenantID: "tenant-1", CustomerID: 9, AccountID: "account-1"}, cmd)
	if err != nil || value != existing {
		t.Fatalf("Create() value=%+v err=%v", value, err)
	}
	if projects.called != 1 || projects.orderViolated {
		t.Fatalf("project access was not checked before replay lookup: %+v calls=%d", projects, repo.findByKeyCalls)
	}
}

func TestCreateReplayAndRaceRecoveryRejectDifferentAccountOrProject(t *testing.T) {
	cmd := CreateCommand{ProjectID: "project-1", ReportType: "FINAL", Reason: "delivery", ReceiveEmail: "a@example.com", IdempotencyKey: "create-key"}
	actor := Actor{TenantID: "tenant-1", CustomerID: 9, AccountID: "account-1"}
	for name, winner := range map[string]*Request{
		"other account": {ActorModel: ActorModel{ID: 22, TenantID: actor.TenantID}, CustomerID: actor.CustomerID, AccountID: "account-2", ProjectID: cmd.ProjectID, RequestHash: requestHash(cmd)},
		"other project": {ActorModel: ActorModel{ID: 23, TenantID: actor.TenantID}, CustomerID: actor.CustomerID, AccountID: actor.AccountID, ProjectID: "project-2", RequestHash: requestHash(cmd)},
	} {
		t.Run(name+" fast replay", func(t *testing.T) {
			repo := &reportRepoStub{findByKey: winner}
			service := NewService(repo, &projectAccessStub{allowed: true}, emailProtectorStub{}, nil, reportClock{now: time.Now()}, idStub{value: "generated-id"})
			value, err := service.Create(context.Background(), actor, cmd)
			if value != nil || !errors.Is(err, ErrIdempotencyConflict) {
				t.Fatalf("Create() value=%+v err=%v", value, err)
			}
		})
		t.Run(name+" race recovery", func(t *testing.T) {
			repo := &reportRepoStub{createErr: errors.New("duplicate key"), findByKey: winner, findByKeyAfter: 1}
			service := NewService(repo, &projectAccessStub{allowed: true}, emailProtectorStub{}, nil, reportClock{now: time.Now()}, idStub{value: "generated-id"})
			value, err := service.Create(context.Background(), actor, cmd)
			if value != nil || !errors.Is(err, ErrIdempotencyConflict) {
				t.Fatalf("Create() value=%+v err=%v", value, err)
			}
		})
	}
}

func TestMarkApprovalStartedReplayDoesNotDuplicateEvent(t *testing.T) {
	now := time.Date(2026, 8, 1, 1, 2, 3, 0, time.UTC)
	request := callbackRequest()
	request.Status = StatusSubmitted
	request.DownstreamRequestID = ""
	repo := &reportRepoStub{request: request}
	service := NewService(repo, nil, nil, nil, reportClock{now: now}, nil)
	if err := service.MarkApprovalStarted(context.Background(), "tenant-1", request.ID, "PS-7"); err != nil {
		t.Fatalf("MarkApprovalStarted() err=%v", err)
	}
	if len(repo.events) != 1 || repo.events[0].Sequence != request.Version+1 || repo.events[0].FromStatus != StatusSubmitted || repo.events[0].ToStatus != StatusApproving {
		t.Fatalf("events=%+v", repo.events)
	}
	request.Status, request.DownstreamRequestID = StatusApproving, "PS-7"
	if err := service.MarkApprovalStarted(context.Background(), "tenant-1", request.ID, "PS-7"); err != nil {
		t.Fatalf("replay err=%v", err)
	}
	if len(repo.events) != 1 {
		t.Fatalf("replay duplicated event: %+v", repo.events)
	}
}

func TestMarkApprovalStartedFailsTransactionWhenEventCannotBeRecorded(t *testing.T) {
	request := callbackRequest()
	request.Status = StatusSubmitted
	request.DownstreamRequestID = ""
	want := errors.New("event insert failed")
	repo := &reportRepoStub{request: request, eventErr: want}
	service := NewService(repo, nil, nil, nil, reportClock{now: time.Now()}, nil)
	if err := service.MarkApprovalStarted(context.Background(), "tenant-1", request.ID, "PS-7"); !errors.Is(err, want) {
		t.Fatalf("MarkApprovalStarted() err=%v want=%v", err, want)
	}
	if repo.updateRuns != 1 {
		t.Fatalf("event must be attempted after projection update in one transaction: updates=%d", repo.updateRuns)
	}
}

func TestApplyCallbackIsIdempotentForSameVersionAndPayload(t *testing.T) {
	cb := validCallback()
	request := callbackRequest()
	request.Status = StatusApprovedProcessing
	request.LastCallbackVersion = cb.Version
	request.LastCallbackKey = cb.IdempotencyKey
	request.LastCallbackHash = hashCallback(cb)
	repo := &reportRepoStub{request: request}
	service := NewService(repo, nil, nil, &ingestorStub{}, reportClock{now: time.Now()}, nil)
	if err := service.ApplyCallback(context.Background(), cb); err != nil {
		t.Fatalf("ApplyCallback() err=%v", err)
	}
	if repo.updateRuns != 0 || repo.file != nil {
		t.Fatalf("idempotent replay changed projection")
	}
	if len(repo.events) != 0 {
		t.Fatalf("idempotent replay appended an event: %+v", repo.events)
	}
}

func TestApplyCallbackRejectsSameVersionDifferentPayload(t *testing.T) {
	cb := validCallback()
	request := callbackRequest()
	request.LastCallbackVersion = cb.Version
	request.LastCallbackKey = cb.IdempotencyKey
	request.LastCallbackHash = "another-hash"
	service := NewService(&reportRepoStub{request: request}, nil, nil, &ingestorStub{}, reportClock{now: time.Now()}, nil)
	if err := service.ApplyCallback(context.Background(), cb); !errors.Is(err, ErrCallbackConflict) {
		t.Fatalf("ApplyCallback() err=%v, want conflict", err)
	}
}

func TestApplyCallbackRejectsReusedIdempotencyKeyWithNewVersionPayload(t *testing.T) {
	cb := validCallback()
	request := callbackRequest()
	request.LastCallbackVersion = 1
	request.LastCallbackKey = cb.IdempotencyKey
	request.LastCallbackHash = hashCallback(cb)
	cb.Version = 2
	cb.ApprovalResult = "different payload"
	service := NewService(&reportRepoStub{request: request}, nil, nil, &ingestorStub{}, reportClock{now: time.Now()}, nil)
	if err := service.ApplyCallback(context.Background(), cb); !errors.Is(err, ErrCallbackConflict) {
		t.Fatalf("ApplyCallback() err=%v, want conflict", err)
	}
}

func TestApplyCallbackRejectsHistoricalIdempotencyKeyReuse(t *testing.T) {
	cb := validCallback()
	cb.Version = 3
	request := callbackRequest()
	request.Status = StatusProcessingFailed
	request.LastCallbackVersion = 2
	request.LastCallbackKey = "newer-key"
	request.LastCallbackHash = "newer-hash"
	repo := &reportRepoStub{request: request, seenSources: map[string]StatusEvent{
		"tenant-1\x00" + sourceHash("CALLBACK", cb.IdempotencyKey): {PayloadHash: "different-payload-hash"},
	}}
	service := NewService(repo, nil, nil, &ingestorStub{}, reportClock{now: time.Now()}, nil)
	if err := service.ApplyCallback(context.Background(), cb); !errors.Is(err, ErrCallbackConflict) {
		t.Fatalf("ApplyCallback() err=%v, want historical-key conflict", err)
	}
	if repo.updateRuns != 0 || len(repo.events) != 0 {
		t.Fatalf("historical-key reuse mutated aggregate: updates=%d events=%+v", repo.updateRuns, repo.events)
	}
}

func TestApplyCallbackAcceptsHistoricalExactReplay(t *testing.T) {
	cb := validCallback()
	request := callbackRequest()
	request.Status = StatusIssued
	request.LastCallbackVersion = 3
	request.LastCallbackKey = "newest-key"
	repo := &reportRepoStub{request: request, seenSources: map[string]StatusEvent{
		"tenant-1\x00" + sourceHash("CALLBACK", cb.IdempotencyKey): {PayloadHash: hashCallback(cb)},
	}}
	service := NewService(repo, nil, nil, &ingestorStub{}, reportClock{now: time.Now()}, nil)
	if err := service.ApplyCallback(context.Background(), cb); err != nil {
		t.Fatalf("ApplyCallback() historical exact replay err=%v", err)
	}
	if repo.updateRuns != 0 || len(repo.events) != 0 {
		t.Fatalf("historical exact replay mutated aggregate: updates=%d events=%+v", repo.updateRuns, repo.events)
	}
}

func TestApplyCallbackRejectsProjectOrCustomerMismatch(t *testing.T) {
	cb := validCallback()
	cb.CustomerID = 10
	service := NewService(&reportRepoStub{request: callbackRequest()}, nil, nil, &ingestorStub{}, reportClock{now: time.Now()}, nil)
	if err := service.ApplyCallback(context.Background(), cb); !errors.Is(err, ErrInvalidCallback) {
		t.Fatalf("ApplyCallback() err=%v, want invalid callback", err)
	}
}

func TestIssuedCallbackQueuesEncryptedDescriptorWithoutPublishingFile(t *testing.T) {
	request := callbackRequest()
	request.AccountID = "oidc-sub-1"
	request.Status = StatusApprovedProcessing
	cb := validCallback()
	cb.Version = 2
	cb.Status = StatusIssued
	cb.ObjectRef = "trusted-bucket/reports/report-7.pdf"
	cb.FileName = "final-report.pdf"
	cb.MIME = "application/pdf"
	cb.FileHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	cb.Size = 1024
	repo := &reportRepoStub{request: request}
	ingestor := &ingestorStub{}
	service := NewService(repo, nil, nil, ingestor, reportClock{now: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)}, nil)
	if err := service.ApplyCallback(context.Background(), cb); err != nil {
		t.Fatalf("ApplyCallback() err=%v", err)
	}
	if repo.file != nil || repo.ingestJob == nil || repo.ingestJob.Status != IngestPending || ingestor.input.ObjectRef != cb.ObjectRef || ingestor.input.FileHash != cb.FileHash {
		t.Fatalf("callback did not queue descriptor safely: file=%+v job=%+v input=%+v", repo.file, repo.ingestJob, ingestor.input)
	}
	if len(repo.events) != 1 || repo.events[0].EventType != "REPORT_INGEST_QUEUED" || repo.events[0].FromStatus != StatusApprovedProcessing || repo.events[0].ToStatus != StatusIngestPending {
		t.Fatalf("issued callback events=%+v", repo.events)
	}
	if len(repo.notifications) != 0 {
		t.Fatalf("queued callback published premature notification=%+v", repo.notifications)
	}
}

func TestIssuedCallbackIngestFailureDoesNotCreateNotification(t *testing.T) {
	request := callbackRequest()
	request.Status, request.AccountID = StatusApprovedProcessing, "oidc-sub-1"
	cb := validCallback()
	cb.Status, cb.Version = StatusIssued, 2
	cb.ObjectRef, cb.FileName, cb.MIME = "trusted/reports/report.pdf", "report.pdf", "application/pdf"
	cb.FileHash, cb.Size = strings.Repeat("a", 64), 1024
	repo := &reportRepoStub{request: request}
	service := NewService(repo, nil, nil, &ingestorStub{err: errors.New("ingest unavailable")}, reportClock{now: time.Now()}, nil)
	if err := service.ApplyCallback(context.Background(), cb); err == nil {
		t.Fatal("ApplyCallback() unexpectedly succeeded")
	}
	if len(repo.notifications) != 0 || repo.updateRuns != 0 || len(repo.events) != 0 {
		t.Fatalf("failed ingestion leaked state: notifications=%+v updates=%d events=%+v", repo.notifications, repo.updateRuns, repo.events)
	}
}

func TestCompleteIngestPublishesEvidenceStatusAndNotificationAtomically(t *testing.T) {
	now := time.Date(2026, 8, 1, 2, 0, 0, 0, time.UTC)
	request := callbackRequest()
	request.Status, request.AccountID = StatusIngestPending, "oidc-sub-1"
	descriptor := FileDescriptor{ObjectRef: "trusted/report.pdf", FileName: "report.pdf", MIME: "application/pdf", FileHash: strings.Repeat("a", 64), Size: 1024}
	job := IngestJob{ID: 11, EventID: "event-1", TenantID: request.TenantID, CustomerID: request.CustomerID, RequestID: request.ID, DescriptorHash: descriptorHash(descriptor), Status: IngestProcessing, LockedBy: "worker-1"}
	repo := &reportRepoStub{request: request, ingestJob: &job}
	result := IngestResult{ObjectKeyCipher: []byte("opaque-object-key"), ObjectVersion: "v1", EncryptionKeyRef: "key-ref", EncryptionAlgorithm: "AES-256-GCM", ScanStatus: "CLEAN", ScanReference: "scan-1", ScannedAt: now.Add(-time.Minute), WatermarkStatus: "DOWNLOAD_TIME"}
	service := NewService(repo, nil, nil, nil, reportClock{now: now}, nil)
	if err := service.CompleteIngest(context.Background(), job, descriptor, result); err != nil {
		t.Fatalf("CompleteIngest() err=%v", err)
	}
	if repo.file == nil || repo.file.RequestID != request.ID || repo.file.ScanStatus != "CLEAN" || repo.file.EncryptionAlgorithm != "AES-256-GCM" {
		t.Fatalf("file evidence=%+v", repo.file)
	}
	if repo.update["status"] != StatusIssued || len(repo.events) != 1 || repo.events[0].FromStatus != StatusIngestPending || repo.events[0].ToStatus != StatusIssued || len(repo.notifications) != 1 || repo.ingestJob.Status != IngestCompleted {
		t.Fatalf("update=%+v events=%+v notifications=%+v job=%+v", repo.update, repo.events, repo.notifications, repo.ingestJob)
	}
}

func TestMarkIngestDeadLetterPublishesFailureWithoutFileOrNotification(t *testing.T) {
	request := callbackRequest()
	request.Status = StatusIngestPending
	job := IngestJob{ID: 11, EventID: "event-1", TenantID: request.TenantID, CustomerID: request.CustomerID, RequestID: request.ID, DescriptorHash: "descriptor-hash", Status: IngestProcessing, LockedBy: "worker-1", RetryCount: 6}
	repo := &reportRepoStub{request: request, ingestJob: &job}
	service := NewService(repo, nil, nil, nil, reportClock{now: time.Now()}, nil)
	if err := service.MarkIngestDeadLetter(context.Background(), job); err != nil {
		t.Fatalf("MarkIngestDeadLetter() err=%v", err)
	}
	if repo.file != nil || len(repo.notifications) != 0 || repo.update["status"] != StatusProcessingFailed || len(repo.events) != 1 || repo.events[0].FromStatus != StatusIngestPending || repo.events[0].ToStatus != StatusProcessingFailed || repo.ingestJob.Status != IngestDeadLetter {
		t.Fatalf("file=%+v notifications=%+v update=%+v events=%+v job=%+v", repo.file, repo.notifications, repo.update, repo.events, repo.ingestJob)
	}
}

func TestReadNotificationIsAccountScopedAndIdempotent(t *testing.T) {
	now := time.Date(2026, 8, 1, 2, 3, 4, 0, time.UTC)
	value := &Notification{ID: 8, TenantID: "tenant-1", CustomerID: 9, RequestID: 7, AccountID: "oidc-sub-1", Status: NotificationUnread}
	repo := &reportRepoStub{notification: value}
	service := NewService(repo, nil, nil, nil, reportClock{now: now}, nil)
	actor := Actor{TenantID: value.TenantID, CustomerID: value.CustomerID, AccountID: value.AccountID}
	if err := service.ReadNotification(context.Background(), actor, value.ID); err != nil {
		t.Fatalf("ReadNotification() err=%v", err)
	}
	if value.Status != NotificationRead || len(repo.readEvents) != 1 || repo.readEvents[0].AccountID != actor.AccountID || repo.readEvents[0].RequestID != value.RequestID {
		t.Fatalf("notification=%+v events=%+v", value, repo.readEvents)
	}
	if err := service.ReadNotification(context.Background(), actor, value.ID); err != nil {
		t.Fatalf("ReadNotification() replay err=%v", err)
	}
	if len(repo.readEvents) != 1 {
		t.Fatalf("read replay appended evidence: %+v", repo.readEvents)
	}
	if err := service.ReadNotification(context.Background(), Actor{TenantID: "tenant-1", CustomerID: 9}, value.ID); !errors.Is(err, ErrNotificationNotFound) {
		t.Fatalf("invalid actor err=%v", err)
	}
}

func TestIssuedCallbackRejectsURLAndDoesNotCallIngestor(t *testing.T) {
	request := callbackRequest()
	request.Status = StatusApprovedProcessing
	cb := validCallback()
	cb.Status, cb.Version = StatusIssued, 2
	cb.ObjectRef = "https://attacker.example/report.pdf"
	cb.FileName, cb.MIME = "report.pdf", "application/pdf"
	cb.FileHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	cb.Size = 1024
	ingestor := &ingestorStub{}
	service := NewService(&reportRepoStub{request: request}, nil, nil, ingestor, reportClock{now: time.Now()}, nil)
	if err := service.ApplyCallback(context.Background(), cb); !errors.Is(err, ErrInvalidCallback) {
		t.Fatalf("ApplyCallback() err=%v, want invalid", err)
	}
	if ingestor.input.ObjectRef != "" {
		t.Fatal("untrusted URL reached file ingestor")
	}
}

func TestCallbackAndTrustedIngestResultRespectPersistenceBounds(t *testing.T) {
	request := callbackRequest()
	request.Status = StatusApprovedProcessing
	base := validCallback()
	base.Status, base.Version = StatusIssued, 2
	base.ObjectRef = "trusted/reports/report.pdf"
	base.FileName, base.MIME = "report.pdf", "application/pdf"
	base.FileHash = strings.Repeat("a", 64)
	base.Size = 1024

	t.Run("callback approval result too long", func(t *testing.T) {
		cb := base
		cb.ApprovalResult = strings.Repeat("a", maxApprovalResultBytes+1)
		ingestor := &ingestorStub{}
		service := NewService(&reportRepoStub{request: request}, nil, nil, ingestor, reportClock{now: time.Now()}, nil)
		if err := service.ApplyCallback(context.Background(), cb); !errors.Is(err, ErrInvalidCallback) {
			t.Fatalf("ApplyCallback() err=%v", err)
		}
		if ingestor.input.ObjectRef != "" {
			t.Fatal("invalid callback reached ingestor")
		}
	})

	t.Run("completion rejects invalid trusted evidence", func(t *testing.T) {
		for name, result := range map[string]IngestResult{
			"oversized key":            {ObjectKeyCipher: []byte("cipher"), ObjectVersion: "v1", EncryptionKeyRef: strings.Repeat("k", 256), EncryptionAlgorithm: "AES-256-GCM", ScanStatus: "CLEAN", ScanReference: "scan-1", ScannedAt: time.Now(), WatermarkStatus: "DOWNLOAD_TIME"},
			"object version":           {ObjectKeyCipher: []byte("cipher"), EncryptionKeyRef: "kms/key", EncryptionAlgorithm: "AES-256-GCM", ScanStatus: "CLEAN", ScanReference: "scan-1", ScannedAt: time.Now(), WatermarkStatus: "DOWNLOAD_TIME"},
			"algorithm":                {ObjectKeyCipher: []byte("cipher"), ObjectVersion: "v1", EncryptionKeyRef: "kms/key", EncryptionAlgorithm: "AES-128-GCM", ScanStatus: "CLEAN", ScanReference: "scan-1", ScannedAt: time.Now(), WatermarkStatus: "DOWNLOAD_TIME"},
			"scan status":              {ObjectKeyCipher: []byte("cipher"), ObjectVersion: "v1", EncryptionKeyRef: "kms/key", EncryptionAlgorithm: "AES-256-GCM", ScanStatus: "INFECTED", ScanReference: "scan-1", ScannedAt: time.Now(), WatermarkStatus: "DOWNLOAD_TIME"},
			"noncanonical scan status": {ObjectKeyCipher: []byte("cipher"), ObjectVersion: "v1", EncryptionKeyRef: "kms/key", EncryptionAlgorithm: "AES-256-GCM", ScanStatus: "clean", ScanReference: "scan-1", ScannedAt: time.Now(), WatermarkStatus: "DOWNLOAD_TIME"},
			"scan reference":           {ObjectKeyCipher: []byte("cipher"), ObjectVersion: "v1", EncryptionKeyRef: "kms/key", EncryptionAlgorithm: "AES-256-GCM", ScanStatus: "CLEAN", ScannedAt: time.Now(), WatermarkStatus: "DOWNLOAD_TIME"},
		} {
			t.Run(name, func(t *testing.T) {
				job := IngestJob{ID: 11, EventID: "event-1", TenantID: request.TenantID, CustomerID: request.CustomerID, RequestID: request.ID, DescriptorHash: descriptorHash(FileDescriptor{ObjectRef: base.ObjectRef, FileName: base.FileName, MIME: base.MIME, FileHash: base.FileHash, Size: base.Size}), Status: IngestProcessing, LockedBy: "worker-1"}
				repo := &reportRepoStub{request: request, ingestJob: &job}
				service := NewService(repo, nil, nil, nil, reportClock{now: time.Now()}, nil)
				if err := service.CompleteIngest(context.Background(), job, FileDescriptor{ObjectRef: base.ObjectRef, FileName: base.FileName, MIME: base.MIME, FileHash: base.FileHash, Size: base.Size}, result); err == nil {
					t.Fatal("CompleteIngest() accepted invalid evidence")
				}
			})
		}
	})
}

func TestOlderCallbackDoesNotAppendDuplicateEvent(t *testing.T) {
	cb := validCallback()
	cb.Version = 1
	request := callbackRequest()
	request.Status = StatusApprovedProcessing
	request.LastCallbackVersion = 2
	repo := &reportRepoStub{request: request}
	service := NewService(repo, nil, nil, &ingestorStub{}, reportClock{now: time.Now()}, nil)
	if err := service.ApplyCallback(context.Background(), cb); err != nil {
		t.Fatalf("ApplyCallback() err=%v", err)
	}
	if repo.updateRuns != 0 || len(repo.events) != 0 {
		t.Fatalf("older callback mutated aggregate: updates=%d events=%+v", repo.updateRuns, repo.events)
	}
}

func TestGetDetailUsesTenantCustomerRequestScopeForTimeline(t *testing.T) {
	now := time.Date(2026, 8, 1, 1, 2, 3, 0, time.UTC)
	request := callbackRequest()
	repo := &reportRepoStub{request: request, events: []StatusEvent{{TenantID: "tenant-1", CustomerID: 9, RequestID: request.ID, EventType: "APPROVAL_STARTED", ToStatus: StatusApproving, OccurredAt: now}}}
	service := NewService(repo, nil, nil, nil, reportClock{now: now}, nil)
	detail, err := service.GetDetail(context.Background(), Actor{TenantID: "tenant-1", CustomerID: 9}, request.ID)
	if err != nil {
		t.Fatalf("GetDetail() err=%v", err)
	}
	if detail.Request != request || len(detail.Events) != 1 || detail.Events[0].RequestID != request.ID {
		t.Fatalf("detail=%+v", detail)
	}
	if repo.findTenant != "tenant-1" || repo.findCustomer != 9 || repo.findRequestID != request.ID || repo.eventTenant != "tenant-1" || repo.eventCustomer != 9 || repo.eventRequestID != request.ID {
		t.Fatalf("detail scope find=(%q,%d,%d) events=(%q,%d,%d)", repo.findTenant, repo.findCustomer, repo.findRequestID, repo.eventTenant, repo.eventCustomer, repo.eventRequestID)
	}
}

func TestGetDetailRejectsMissingTenantScope(t *testing.T) {
	service := NewService(&reportRepoStub{request: callbackRequest()}, nil, nil, nil, reportClock{now: time.Now()}, nil)
	if _, err := service.GetDetail(context.Background(), Actor{CustomerID: 9}, 7); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetDetail() err=%v want not found", err)
	}
}
