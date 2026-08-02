package report

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/pagination"
)

type downloadRepoStub struct {
	request         *Request
	file            *File
	grant           *Grant
	events          []DownloadEvent
	revoked         int
	updated         []map[string]any
	findScope       Actor
	findRequest     uint64
	createErr       error
	rollbackOnError bool
	issueReplay     *Grant
	eventErr        error
	seenDedupe      map[string]struct{}
	riskAlerts      []RiskAlert
	riskReviews     []RiskReviewEvent
}

func (r *downloadRepoStub) WithTransaction(ctx context.Context, fn func(context.Context) error) error {
	eventCount, updateCount := len(r.events), len(r.updated)
	err := fn(ctx)
	if err != nil && r.rollbackOnError {
		r.events, r.updated = r.events[:eventCount], r.updated[:updateCount]
	}
	return err
}
func (r *downloadRepoStub) Find(_ context.Context, tenant string, customer, id uint64) (*Request, error) {
	if r.request == nil || r.request.TenantID != tenant || r.request.CustomerID != customer || r.request.ID != id {
		return nil, ErrNotFound
	}
	return r.request, nil
}
func (r *downloadRepoStub) FindForUpdate(_ context.Context, tenant string, id uint64) (*Request, error) {
	if r.request == nil || r.request.TenantID != tenant || r.request.ID != id {
		return nil, ErrNotFound
	}
	return r.request, nil
}
func (r *downloadRepoStub) FindFile(_ context.Context, tenant string, id uint64) (*File, error) {
	if r.file == nil || r.file.TenantID != tenant || r.file.RequestID != id {
		return nil, ErrFileUnavailable
	}
	return r.file, nil
}
func (r *downloadRepoStub) RevokeActiveGrants(context.Context, string, uint64, uint64, string, time.Time) error {
	r.revoked++
	return nil
}
func (r *downloadRepoStub) CreateGrant(_ context.Context, grant *Grant) error {
	if r.createErr != nil {
		return r.createErr
	}
	grant.ID = 55
	r.grant = grant
	return nil
}
func (r *downloadRepoStub) FindGrantByIssueKeyForUpdate(_ context.Context, tenant string, customer, requestID uint64, accountID, key string) (*Grant, error) {
	if r.issueReplay != nil && r.issueReplay.TenantID == tenant && r.issueReplay.CustomerID == customer && r.issueReplay.RequestID == requestID && r.issueReplay.AccountID == accountID && r.issueReplay.IssueKeyHash == key {
		return r.issueReplay, nil
	}
	return nil, ErrGrantNotFound
}
func (r *downloadRepoStub) FindGrantForUpdate(_ context.Context, tenant string, customer, requestID uint64, accountID, hash string) (*Grant, error) {
	r.findScope, r.findRequest = Actor{TenantID: tenant, CustomerID: customer, AccountID: accountID}, requestID
	if r.grant == nil || r.grant.TokenHash != hash || !sameGrantScope(r.grant, r.findScope, requestID) {
		return nil, ErrGrantNotFound
	}
	return r.grant, nil
}
func (r *downloadRepoStub) UpdateGrant(_ context.Context, value *Grant, fields map[string]any) error {
	r.updated = append(r.updated, fields)
	if status, ok := fields["status"].(GrantStatus); ok {
		value.Status = status
	}
	if slot, exists := fields["active_slot"]; exists {
		if slot == nil {
			value.ActiveSlot = nil
		} else if raw, ok := slot.(string); ok {
			value.ActiveSlot = &raw
		}
	}
	if riskState, ok := fields["risk_state"].(string); ok {
		value.RiskState = riskState
	}
	value.Version++
	return nil
}
func (r *downloadRepoStub) CreateDownloadEvent(_ context.Context, event *DownloadEvent) error {
	if r.eventErr != nil {
		return r.eventErr
	}
	r.events = append(r.events, *event)
	return nil
}
func (r *downloadRepoStub) CreateDownloadEventOnce(_ context.Context, event *DownloadEvent) error {
	if r.eventErr != nil {
		return r.eventErr
	}
	if event.DedupeKey != nil {
		if r.seenDedupe == nil {
			r.seenDedupe = make(map[string]struct{})
		}
		if _, exists := r.seenDedupe[*event.DedupeKey]; exists {
			return nil
		}
		r.seenDedupe[*event.DedupeKey] = struct{}{}
	}
	r.events = append(r.events, *event)
	return nil
}
func (r *downloadRepoStub) CreateRiskAlert(_ context.Context, value *RiskAlert) error {
	value.ID = uint64(len(r.riskAlerts) + 1)
	r.riskAlerts = append(r.riskAlerts, *value)
	return nil
}
func (r *downloadRepoStub) ListRiskAlerts(_ context.Context, actor Actor, openOnly bool, page, pageSize int) (pagination.Page[RiskAlertView], error) {
	result := pagination.Page[RiskAlertView]{Page: page, PageSize: pageSize, Items: []RiskAlertView{}}
	for _, value := range r.riskAlerts {
		if value.TenantID == actor.TenantID && value.CustomerID == actor.CustomerID && value.AccountID == actor.AccountID && (!openOnly || value.Status == RiskAlertOpen) {
			result.Items = append(result.Items, riskAlertStubView(value))
		}
	}
	result.Total = int64(len(result.Items))
	return result, nil
}
func (r *downloadRepoStub) ListRiskAlertsForReview(_ context.Context, tenantID, status string, page, pageSize int) (pagination.Page[RiskAlertView], error) {
	result := pagination.Page[RiskAlertView]{Page: page, PageSize: pageSize, Items: []RiskAlertView{}}
	for _, value := range r.riskAlerts {
		if value.TenantID == tenantID && (status == "" || value.Status == status) {
			result.Items = append(result.Items, riskAlertStubView(value))
		}
	}
	result.Total = int64(len(result.Items))
	return result, nil
}
func (r *downloadRepoStub) FindRiskAlertForUpdate(_ context.Context, tenantID, publicID string) (*RiskAlert, error) {
	for index := range r.riskAlerts {
		if r.riskAlerts[index].TenantID == tenantID && r.riskAlerts[index].PublicID == publicID {
			return &r.riskAlerts[index], nil
		}
	}
	return nil, ErrRiskAlertNotFound
}
func (r *downloadRepoStub) FindRiskAlertView(ctx context.Context, tenantID, publicID string) (*RiskAlertView, error) {
	value, err := r.FindRiskAlertForUpdate(ctx, tenantID, publicID)
	if err != nil {
		return nil, err
	}
	view := riskAlertStubView(*value)
	return &view, nil
}
func (r *downloadRepoStub) UpdateRiskAlert(_ context.Context, value *RiskAlert, fields map[string]any) error {
	value.Status = fields["status"].(string)
	value.ActiveSlot = nil
	value.ResolvedAt = fields["resolved_at"].(*time.Time)
	value.ResolvedBy = fields["resolved_by"].(string)
	value.ResolutionAction = fields["resolution_action"].(string)
	value.ResolutionReason = fields["resolution_reason"].(string)
	value.Version++
	return nil
}
func (r *downloadRepoStub) CreateRiskReviewEvent(_ context.Context, value *RiskReviewEvent) error {
	r.riskReviews = append(r.riskReviews, *value)
	return nil
}
func (r *downloadRepoStub) FindRiskReviewEvent(_ context.Context, tenantID, actorID, key string) (*RiskReviewEvent, error) {
	for index := range r.riskReviews {
		value := &r.riskReviews[index]
		if value.TenantID == tenantID && value.ActorID == actorID && value.IdempotencyHash == key {
			return value, nil
		}
	}
	return nil, ErrRiskAlertNotFound
}
func (r *downloadRepoStub) FindGrantByIDForUpdate(_ context.Context, tenantID string, id uint64) (*Grant, error) {
	if r.grant == nil || r.grant.TenantID != tenantID || r.grant.ID != id {
		return nil, ErrGrantNotFound
	}
	return r.grant, nil
}
func (r *downloadRepoStub) FindActiveGrantForUpdate(_ context.Context, tenantID string, customerID, requestID uint64, accountID string) (*Grant, error) {
	if r.grant != nil && r.grant.TenantID == tenantID && r.grant.CustomerID == customerID && r.grant.RequestID == requestID && r.grant.AccountID == accountID && r.grant.Status == GrantActive {
		return r.grant, nil
	}
	return nil, ErrGrantNotFound
}

func riskAlertStubView(value RiskAlert) RiskAlertView {
	return RiskAlertView{AlertID: value.PublicID, RequestID: value.RequestID, AccountID: value.AccountID, RiskCode: value.RiskCode, Status: value.Status, DetectedAt: value.DetectedAt, ResolvedAt: value.ResolvedAt, ResolvedBy: value.ResolvedBy, ResolutionAction: value.ResolutionAction, ResolutionReason: value.ResolutionReason, Version: value.Version}
}

type tokenStub struct {
	token string
	err   error
}

func (s tokenStub) NewToken() (string, error) { return s.token, s.err }

type verifiedReaderStub struct {
	result PreparedDownload
	err    error
}

type repeatVerifiedReader struct{ fileHash string }

func (r repeatVerifiedReader) OpenVerified(context.Context, *File) (PreparedDownload, error) {
	return PreparedDownload{Reader: io.NopCloser(strings.NewReader("abc")), FileName: "report.pdf", MIME: "application/pdf", Size: 3, FileHash: r.fileHash}, nil
}

func (s verifiedReaderStub) OpenVerified(context.Context, *File) (PreparedDownload, error) {
	return s.result, s.err
}

type riskStub struct {
	freeze bool
	reason string
	err    error
}

type watermarkStub struct {
	trackingCode string
}

func (w *watermarkStub) Apply(_ context.Context, raw []byte, value WatermarkContext) ([]byte, error) {
	w.trackingCode = value.TrackingCode
	return append([]byte(nil), raw...), nil
}

func (s riskStub) Evaluate(context.Context, Actor, *Request, *Grant, DownloadMetadata) (bool, string, error) {
	return s.freeze, s.reason, s.err
}

func downloadFixture(now time.Time) (*downloadRepoStub, Actor, string) {
	actor := Actor{TenantID: "tenant-1", CustomerID: 9, AccountID: "account-1"}
	request := &Request{ActorModel: ActorModel{ID: 7, TenantID: actor.TenantID}, CustomerID: actor.CustomerID, AccountID: actor.AccountID, Status: StatusIssued}
	scannedAt := now.Add(-time.Minute)
	file := &File{Model: database.Model{ID: 8, TenantID: actor.TenantID}, RequestID: request.ID, FileName: "report.pdf", MIME: "application/pdf", Size: 3, FileHash: "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad", ObjectKeyCipher: []byte("cipher"), ObjectVersion: "version-1", EncryptionKeyRef: "kms/key", EncryptionAlgorithm: "AES-256-GCM", ScanStatus: "CLEAN", ScanReference: "scan-1", ScannedAt: &scannedAt}
	token := strings.Repeat("t", 43)
	grant := &Grant{ActorModel: ActorModel{ID: 55, TenantID: actor.TenantID, Version: 1}, PublicID: "grant-1", CustomerID: actor.CustomerID, RequestID: request.ID, AccountID: actor.AccountID, TokenHash: tokenHash(token), Status: GrantActive, ExpiresAt: now.Add(time.Hour)}
	return &downloadRepoStub{request: request, file: file, grant: grant}, actor, token
}

func TestCreateGrantRequiresIssuedScopedReportAndTrustedFile(t *testing.T) {
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	repo, actor, token := downloadFixture(now)
	repo.grant = nil
	service := NewDownloadService(repo, nil, nil, reportClock{now: now}, idStub{value: "grant-public"}, tokenStub{token: token}, 72*time.Hour)
	value, err := service.CreateGrant(context.Background(), actor, 7, GrantCommand{IdempotencyKey: "click-1"})
	if err != nil {
		t.Fatalf("CreateGrant() err=%v", err)
	}
	if value.DownloadToken != token || value.GrantID != "grant-public" || !value.ExpiresAt.Equal(now.Add(72*time.Hour)) {
		t.Fatalf("result=%+v", value)
	}
	if repo.revoked != 1 || repo.grant == nil || repo.grant.TokenHash == token || repo.grant.TokenHash != tokenHash(token) {
		t.Fatalf("grant=%+v revoked=%d", repo.grant, repo.revoked)
	}
	if len(repo.events) != 1 || repo.events[0].EventType != "GRANT_ISSUED" || repo.events[0].IdempotencyHash == "" {
		t.Fatalf("events=%+v", repo.events)
	}

	repo2, actor2, _ := downloadFixture(now)
	repo2.grant = nil
	repo2.request.Status = StatusApproving
	service2 := NewDownloadService(repo2, nil, nil, reportClock{now: now}, idStub{value: "id"}, tokenStub{token: token}, 0)
	if _, err = service2.CreateGrant(context.Background(), actor2, 7, GrantCommand{IdempotencyKey: "click"}); !errors.Is(err, ErrReportNotIssued) {
		t.Fatalf("not issued err=%v", err)
	}
}

func TestCreateGrantIDORIsNotFound(t *testing.T) {
	now := time.Now().UTC()
	repo, _, token := downloadFixture(now)
	service := NewDownloadService(repo, nil, nil, reportClock{now: now}, idStub{value: "id"}, tokenStub{token: token}, 0)
	_, err := service.CreateGrant(context.Background(), Actor{TenantID: "tenant-1", CustomerID: 10, AccountID: "other"}, 7, GrantCommand{IdempotencyKey: "click"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("IDOR err=%v", err)
	}
}

func TestCreateGrantReplayCannotRecoverPlaintextToken(t *testing.T) {
	now := time.Now().UTC()
	repo, actor, token := downloadFixture(now)
	repo.grant = nil
	repo.issueReplay = &Grant{ActorModel: ActorModel{TenantID: actor.TenantID}, CustomerID: actor.CustomerID, RequestID: 7, AccountID: actor.AccountID, IssueKeyHash: sourceHash("GRANT", "click")}
	service := NewDownloadService(repo, nil, nil, reportClock{now: now}, idStub{value: "id"}, tokenStub{token: token}, 0)
	if _, err := service.CreateGrant(context.Background(), actor, 7, GrantCommand{IdempotencyKey: "click"}); !errors.Is(err, ErrIssueReplay) {
		t.Fatalf("replay err=%v", err)
	}
	if repo.grant != nil || repo.revoked != 0 {
		t.Fatalf("replay mutated grants: grant=%+v revoked=%d", repo.grant, repo.revoked)
	}
}

func TestCreateGrantActiveSlotRaceFailsClosedWithoutReturningCredential(t *testing.T) {
	now := time.Now().UTC()
	repo, actor, token := downloadFixture(now)
	repo.grant = nil
	repo.createErr = errors.New("Error 1062: Duplicate entry for key 'uq_portal_report_grant_active'")
	service := NewDownloadService(repo, nil, nil, reportClock{now: now}, idStub{value: "id"}, tokenStub{token: token}, 0)
	value, err := service.CreateGrant(context.Background(), actor, 7, GrantCommand{IdempotencyKey: "fresh-click"})
	if value != nil || !errors.Is(err, ErrIssueReplay) {
		t.Fatalf("CreateGrant() value=%+v err=%v", value, err)
	}
}

func TestAuthorizeDownloadScopesTokenToTenantCustomerAccountAndRequest(t *testing.T) {
	now := time.Now().UTC()
	repo, actor, token := downloadFixture(now)
	service := NewDownloadService(repo, verifiedReaderStub{result: PreparedDownload{Reader: io.NopCloser(strings.NewReader("abc")), FileName: "report.pdf", MIME: "application/pdf", Size: 3, FileHash: repo.file.FileHash}}, nil, reportClock{now: now}, nil, nil, 0)
	other := actor
	other.AccountID = "other"
	if _, err := service.AuthorizeDownload(context.Background(), other, 7, token, DownloadMetadata{}); !errors.Is(err, ErrGrantNotFound) {
		t.Fatalf("cross-account err=%v", err)
	}
	if repo.findScope.AccountID != "other" || repo.findRequest != 7 {
		t.Fatalf("repository lookup not fully scoped: %+v %d", repo.findScope, repo.findRequest)
	}
	if len(repo.events) != 1 || repo.events[0].GrantID != nil || repo.events[0].ReasonCode != "PORTAL_REPORT_GRANT_NOT_FOUND" {
		t.Fatalf("invalid token attempt was not safely audited: %+v", repo.events)
	}
}

func TestInvalidTokenAuditIsBoundedPerAccountReportAndHour(t *testing.T) {
	now := time.Date(2026, 8, 1, 10, 30, 0, 0, time.UTC)
	repo, actor, _ := downloadFixture(now)
	service := NewDownloadService(repo, nil, nil, reportClock{now: now}, nil, nil, 0)
	for _, token := range []string{strings.Repeat("x", 43), strings.Repeat("y", 43)} {
		if _, err := service.AuthorizeDownload(context.Background(), actor, 7, token, DownloadMetadata{}); !errors.Is(err, ErrGrantNotFound) {
			t.Fatalf("AuthorizeDownload() err=%v", err)
		}
	}
	if len(repo.events) != 1 || repo.events[0].DedupeKey == nil {
		t.Fatalf("invalid token audit was not bounded: %+v", repo.events)
	}
}

func TestMalformedTokenIsAlsoCoveredByBoundedDenialAudit(t *testing.T) {
	now := time.Date(2026, 8, 1, 10, 30, 0, 0, time.UTC)
	repo, actor, _ := downloadFixture(now)
	service := NewDownloadService(repo, nil, nil, reportClock{now: now}, nil, nil, 0)
	for _, token := range []string{"short", strings.Repeat("x", maximumTokenLen+1)} {
		if _, err := service.AuthorizeDownload(context.Background(), actor, 7, token, DownloadMetadata{}); !errors.Is(err, ErrGrantNotFound) {
			t.Fatalf("AuthorizeDownload() err=%v", err)
		}
	}
	if len(repo.events) != 1 || repo.events[0].DedupeKey == nil || repo.events[0].GrantID != nil {
		t.Fatalf("malformed token denial audit was not bounded: %+v", repo.events)
	}
}

func TestMalformedTokenCannotEnumerateMissingOrForeignReport(t *testing.T) {
	now := time.Date(2026, 8, 1, 10, 30, 0, 0, time.UTC)
	repo, actor, _ := downloadFixture(now)
	service := NewDownloadService(repo, nil, nil, reportClock{now: now}, nil, nil, 0)

	for _, test := range []struct {
		name      string
		actor     Actor
		requestID uint64
	}{
		{name: "missing", actor: actor, requestID: 999},
		{name: "foreign customer", actor: Actor{TenantID: actor.TenantID, CustomerID: actor.CustomerID + 1, AccountID: actor.AccountID}, requestID: 7},
		{name: "foreign tenant", actor: Actor{TenantID: "other-tenant", CustomerID: actor.CustomerID, AccountID: actor.AccountID}, requestID: 7},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := len(repo.events)
			_, err := service.AuthorizeDownload(context.Background(), test.actor, test.requestID, "short", DownloadMetadata{})
			if !errors.Is(err, ErrGrantNotFound) {
				t.Fatalf("AuthorizeDownload() err=%v, want opaque not found", err)
			}
			if len(repo.events) != before {
				t.Fatalf("foreign or missing report must not create an audit row: %+v", repo.events[before:])
			}
		})
	}
}

func TestCorruptOversizedFileMetadataFailsBeforeReaderAndBuffer(t *testing.T) {
	now := time.Now().UTC()
	repo, actor, token := downloadFixture(now)
	repo.file.Size = maxReportFileSize + 1
	reader := verifiedReaderStub{err: errors.New("reader must not be opened")}
	service := NewDownloadService(repo, reader, nil, reportClock{now: now}, nil, nil, 0)
	if _, err := service.AuthorizeDownload(context.Background(), actor, 7, token, DownloadMetadata{}); !errors.Is(err, ErrDownloadIntegrity) {
		t.Fatalf("AuthorizeDownload() err=%v", err)
	}
	if len(service.bufferSlots) != 0 || len(repo.events) != 1 || repo.events[0].ReasonCode != "INTEGRITY_FAILED" {
		t.Fatalf("slots=%d events=%+v", len(service.bufferSlots), repo.events)
	}
}

func TestDownloadRejectsFileWithoutExplicitCleanScanEvidence(t *testing.T) {
	now := time.Now().UTC()
	for _, status := range []string{"", "PENDING", "INFECTED"} {
		t.Run(status, func(t *testing.T) {
			repo, actor, token := downloadFixture(now)
			repo.file.ScanStatus = status
			reader := verifiedReaderStub{err: errors.New("reader must not be opened")}
			service := NewDownloadService(repo, reader, nil, reportClock{now: now}, nil, nil, 0)
			if _, err := service.AuthorizeDownload(context.Background(), actor, 7, token, DownloadMetadata{}); !errors.Is(err, ErrDownloadIntegrity) {
				t.Fatalf("scan status %q error=%v", status, err)
			}
		})
	}
}

func TestInvalidTokenAuditFailureFailsClosed(t *testing.T) {
	now := time.Now().UTC()
	repo, actor, _ := downloadFixture(now)
	repo.eventErr = errors.New("audit unavailable")
	service := NewDownloadService(repo, nil, nil, reportClock{now: now}, nil, nil, 0)
	_, err := service.AuthorizeDownload(context.Background(), actor, 7, strings.Repeat("x", 43), DownloadMetadata{})
	if err == nil || err.Error() != ErrDownloadAudit.Error() {
		t.Fatalf("AuthorizeDownload() err=%v want audit unavailable", err)
	}
}

func TestAuthorizeDownloadExpiresAndFreezesBeforeOpeningFile(t *testing.T) {
	now := time.Now().UTC()
	for name, test := range map[string]struct {
		configure func(*downloadRepoStub)
		want      error
	}{
		"expired": {func(r *downloadRepoStub) { r.grant.ExpiresAt = now }, ErrGrantExpired},
		"frozen":  {func(r *downloadRepoStub) { r.grant.Status = GrantFrozen }, ErrGrantFrozen},
	} {
		t.Run(name, func(t *testing.T) {
			repo, actor, token := downloadFixture(now)
			repo.rollbackOnError = true
			test.configure(repo)
			service := NewDownloadService(repo, verifiedReaderStub{err: errors.New("must not open")}, nil, reportClock{now: now}, nil, nil, 0)
			if _, err := service.AuthorizeDownload(context.Background(), actor, 7, token, DownloadMetadata{}); !errors.Is(err, test.want) {
				t.Fatalf("err=%v want=%v", err, test.want)
			}
			if len(repo.events) != 1 || repo.events[0].EventType != "DOWNLOAD_DENIED" {
				t.Fatalf("events=%+v", repo.events)
			}
		})
	}
}

func TestDeniedDecisionCommitsAuditBeforeReturningBusinessError(t *testing.T) {
	now := time.Now().UTC()
	repo, actor, token := downloadFixture(now)
	repo.rollbackOnError = true
	repo.grant.ExpiresAt = now
	service := NewDownloadService(repo, nil, nil, reportClock{now: now}, nil, nil, 0)
	_, err := service.AuthorizeDownload(context.Background(), actor, 7, token, DownloadMetadata{})
	if !errors.Is(err, ErrGrantExpired) {
		t.Fatalf("err=%v", err)
	}
	if len(repo.events) != 1 || len(repo.updated) != 1 {
		t.Fatalf("denial changes rolled back: events=%+v updates=%+v", repo.events, repo.updated)
	}
}

func TestRiskPolicyCanFreezeGrant(t *testing.T) {
	now := time.Now().UTC()
	repo, actor, token := downloadFixture(now)
	service := NewDownloadService(repo, verifiedReaderStub{}, riskStub{freeze: true, reason: "TRUSTED_GATEWAY_RULE"}, reportClock{now: now}, nil, nil, 0)
	if _, err := service.AuthorizeDownload(context.Background(), actor, 7, token, DownloadMetadata{}); !errors.Is(err, ErrGrantFrozen) {
		t.Fatalf("err=%v", err)
	}
	if len(repo.updated) != 1 || repo.updated[0]["status"] != GrantFrozen {
		t.Fatalf("updates=%+v", repo.updated)
	}
}

func TestRiskFreezeCreatesScopedAlertAndReviewIsIdempotent(t *testing.T) {
	now := time.Date(2026, 8, 2, 3, 4, 5, 0, time.UTC)
	repo, actor, token := downloadFixture(now)
	service := NewDownloadService(repo, verifiedReaderStub{}, riskStub{freeze: true, reason: "TRUSTED_GATEWAY_RULE"}, reportClock{now: now}, idStub{value: "risk-alert-1"}, nil, 0)
	if _, err := service.AuthorizeDownload(context.Background(), actor, 7, token, DownloadMetadata{}); !errors.Is(err, ErrGrantFrozen) {
		t.Fatalf("AuthorizeDownload() err=%v", err)
	}
	if len(repo.riskAlerts) != 1 || repo.riskAlerts[0].TenantID != actor.TenantID || repo.riskAlerts[0].CustomerID != actor.CustomerID || repo.riskAlerts[0].AccountID != actor.AccountID || repo.riskAlerts[0].RiskCode != "TRUSTED_GATEWAY_RULE" || repo.riskAlerts[0].Status != RiskAlertOpen {
		t.Fatalf("risk alert=%+v", repo.riskAlerts)
	}
	// The in-memory repository must reflect the transaction's optimistic update
	// before the independently authenticated operator can review the alert.
	repo.grant.Status, repo.grant.ActiveSlot, repo.grant.RiskState, repo.grant.Version = GrantFrozen, nil, "TRUSTED_GATEWAY_RULE", 2
	command := RiskReviewCommand{ExpectedVersion: 1, Action: RiskActionUnfreeze, Reason: "reviewed trusted gateway evidence", IdempotencyKey: "review-1"}
	view, err := service.ReviewRiskAlert(context.Background(), actor.TenantID, "machine:operator", "risk-alert-1", command)
	if err != nil {
		t.Fatal(err)
	}
	if view.Status != RiskAlertResolved || view.ResolutionAction != RiskActionUnfreeze || repo.grant.Status != GrantActive || repo.grant.ActiveSlot == nil || *repo.grant.ActiveSlot != "ACTIVE" || repo.grant.RiskState != "" || len(repo.riskReviews) != 1 {
		t.Fatalf("view=%+v grant=%+v review=%+v", view, repo.grant, repo.riskReviews)
	}
	if _, err = service.ReviewRiskAlert(context.Background(), actor.TenantID, "machine:operator", "risk-alert-1", command); err != nil {
		t.Fatalf("exact replay err=%v", err)
	}
	if len(repo.riskReviews) != 1 {
		t.Fatalf("replay appended review events: %+v", repo.riskReviews)
	}
	command.Reason = "different payload"
	if _, err = service.ReviewRiskAlert(context.Background(), actor.TenantID, "machine:operator", "risk-alert-1", command); !errors.Is(err, ErrRiskReviewConflict) {
		t.Fatalf("conflicting replay err=%v", err)
	}
}

func TestRiskRevokeAndReissueNeverGeneratesPlaintextCredential(t *testing.T) {
	now := time.Date(2026, 8, 2, 3, 4, 5, 0, time.UTC)
	repo, actor, _ := downloadFixture(now)
	repo.grant.Status, repo.grant.RiskState, repo.grant.Version = GrantFrozen, "MULTI_DEVICE", 2
	active := "OPEN"
	repo.riskAlerts = []RiskAlert{{ID: 1, PublicID: "alert-1", TenantID: actor.TenantID, CustomerID: actor.CustomerID, RequestID: 7, GrantID: repo.grant.ID, AccountID: actor.AccountID, RiskCode: "MULTI_DEVICE", Status: RiskAlertOpen, ActiveSlot: &active, DetectedAt: now, Version: 1}}
	tokens := &countingTokenGenerator{}
	service := NewDownloadService(repo, nil, nil, reportClock{now: now}, nil, tokens, 0)
	view, err := service.ReviewRiskAlert(context.Background(), actor.TenantID, "machine:operator", "alert-1", RiskReviewCommand{ExpectedVersion: 1, Action: RiskActionRevokeAndReissue, Reason: "customer identity reconfirmed", IdempotencyKey: "review-2"})
	if err != nil {
		t.Fatal(err)
	}
	if tokens.calls != 0 || view.ResolutionAction != RiskActionRevokeAndReissue || repo.grant.Status != GrantRevoked || len(repo.events) != 1 || repo.events[0].EventType != "GRANT_REVOKED_FOR_REISSUE" {
		t.Fatalf("view=%+v grant=%+v events=%+v tokenCalls=%d", view, repo.grant, repo.events, tokens.calls)
	}
}

type countingTokenGenerator struct{ calls int }

func (g *countingTokenGenerator) NewToken() (string, error) {
	g.calls++
	return strings.Repeat("x", 43), nil
}

func TestUnavailableReaderAuditsFailureWithoutSuccessCount(t *testing.T) {
	now := time.Now().UTC()
	repo, actor, token := downloadFixture(now)
	service := NewDownloadService(repo, nil, nil, reportClock{now: now}, nil, nil, 0)
	if _, err := service.AuthorizeDownload(context.Background(), actor, 7, token, DownloadMetadata{}); !errors.Is(err, ErrDownloadUnavailable) {
		t.Fatalf("err=%v", err)
	}
	if len(repo.updated) != 0 || len(repo.events) != 1 || repo.events[0].Result != "DEPENDENCY_UNAVAILABLE" {
		t.Fatalf("updates=%+v events=%+v", repo.updated, repo.events)
	}
}

func TestVerifiedReaderMetadataMismatchFailsClosed(t *testing.T) {
	now := time.Now().UTC()
	repo, actor, token := downloadFixture(now)
	reader := verifiedReaderStub{result: PreparedDownload{Reader: io.NopCloser(strings.NewReader("evil")), FileName: "report.pdf", MIME: "application/pdf", Size: 4, FileHash: "bad"}}
	service := NewDownloadService(repo, reader, nil, reportClock{now: now}, nil, nil, 0)
	if _, err := service.AuthorizeDownload(context.Background(), actor, 7, token, DownloadMetadata{}); !errors.Is(err, ErrDownloadIntegrity) {
		t.Fatalf("err=%v", err)
	}
	if len(repo.updated) != 0 || repo.events[0].ReasonCode != "INTEGRITY_FAILED" {
		t.Fatalf("updated=%+v events=%+v", repo.updated, repo.events)
	}
}

func TestVerifiedReaderStreamBytesAreCheckedBeforeReturningContent(t *testing.T) {
	now := time.Now().UTC()
	repo, actor, token := downloadFixture(now)
	repo.file.FileHash = "d9d4a498ccf3834fb97fd6118fb80318f0c1c75eec745c5a4acd887413985198"
	reader := verifiedReaderStub{result: PreparedDownload{Reader: io.NopCloser(strings.NewReader("abd")), FileName: "report.pdf", MIME: "application/pdf", Size: 3, FileHash: repo.file.FileHash}}
	service := NewDownloadService(repo, reader, nil, reportClock{now: now}, nil, nil, 0)
	if _, err := service.AuthorizeDownload(context.Background(), actor, 7, token, DownloadMetadata{}); !errors.Is(err, ErrDownloadIntegrity) {
		t.Fatalf("AuthorizeDownload() err=%v want integrity failure", err)
	}
	if len(repo.events) != 1 || repo.events[0].ReasonCode != "INTEGRITY_FAILED" {
		t.Fatalf("events=%+v", repo.events)
	}
}

func TestDownloadBufferCapacityWaitIsContextCancellable(t *testing.T) {
	now := time.Now().UTC()
	repo, actor, token := downloadFixture(now)
	service := NewDownloadService(repo, verifiedReaderStub{result: PreparedDownload{Reader: io.NopCloser(strings.NewReader("abc")), FileName: "report.pdf", MIME: "application/pdf", Size: 3, FileHash: repo.file.FileHash}}, nil, reportClock{now: now}, nil, nil, 0)
	service.bufferSlots <- struct{}{}
	service.bufferSlots <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.AuthorizeDownload(ctx, actor, 7, token, DownloadMetadata{}); !errors.Is(err, ErrDownloadUnavailable) {
		t.Fatalf("AuthorizeDownload() err=%v", err)
	}
	if len(repo.events) != 1 || repo.events[0].ReasonCode != "DOWNLOAD_CAPACITY_CANCELLED" {
		t.Fatalf("events=%+v", repo.events)
	}
}

func TestDownloadBufferSlotLivesUntilContentClose(t *testing.T) {
	now := time.Now().UTC()
	repo1, actor, token1 := downloadFixture(now)
	repo2, _, token2 := downloadFixture(now)
	repo2.grant.TokenHash = tokenHash(strings.Repeat("u", 43))
	token2 = strings.Repeat("u", 43)
	reader := repeatVerifiedReader{fileHash: repo1.file.FileHash}
	service := NewDownloadService(repo1, reader, nil, reportClock{now: now}, nil, nil, 0)
	first, err := service.AuthorizeDownload(context.Background(), actor, 7, token1, DownloadMetadata{})
	if err != nil {
		t.Fatal(err)
	}
	service.repo = repo2
	second, err := service.AuthorizeDownload(context.Background(), actor, 7, token2, DownloadMetadata{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = service.AuthorizeDownload(ctx, actor, 7, token2, DownloadMetadata{}); !errors.Is(err, ErrDownloadUnavailable) {
		t.Fatalf("third download did not wait for slot: %v", err)
	}
	if err = first.Reader.Close(); err != nil {
		t.Fatal(err)
	}
	third, err := service.AuthorizeDownload(context.Background(), actor, 7, token2, DownloadMetadata{})
	if err != nil {
		t.Fatalf("slot was not released by content close: %v", err)
	}
	_ = second.Reader.Close()
	_ = third.Reader.Close()
}

func TestSuccessfulCompletionAtomicallyCountsAndAudits(t *testing.T) {
	now := time.Now().UTC()
	repo, actor, token := downloadFixture(now)
	repo.file.FileHash = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	service := NewDownloadService(repo, verifiedReaderStub{result: PreparedDownload{Reader: io.NopCloser(strings.NewReader("abc")), FileName: "report.pdf", MIME: "application/pdf", Size: 3, FileHash: repo.file.FileHash}}, nil, reportClock{now: now}, nil, nil, 0)
	content, err := service.AuthorizeDownload(context.Background(), actor, 7, token, DownloadMetadata{})
	if err != nil {
		t.Fatal(err)
	}
	if err = content.Complete(context.Background(), true, ""); err != nil {
		t.Fatal(err)
	}
	if len(repo.updated) != 1 || repo.updated[0]["download_count"] != uint64(1) || len(repo.events) != 1 || repo.events[0].EventType != "DOWNLOAD_SUCCEEDED" {
		t.Fatalf("updates=%+v events=%+v", repo.updated, repo.events)
	}
}

func TestProductionWatermarkTrackingIsScopedAndPersistedOnlyAsDigest(t *testing.T) {
	now := time.Now().UTC()
	repo, actor, token := downloadFixture(now)
	repo.file.Size = 6
	repo.file.FileHash = "d9d4a498ccf3834fb97fd6118fb80318f0c1c75eec745c5a4acd887413985198"
	watermark := &watermarkStub{}
	service := NewDownloadService(repo, verifiedReaderStub{result: PreparedDownload{Reader: io.NopCloser(strings.NewReader("%PDF-x")), FileName: "report.pdf", MIME: "application/pdf", Size: 6, FileHash: repo.file.FileHash}}, riskStub{}, reportClock{now: now}, idStub{value: "tracking-secret-1"}, nil, 0).RequireProductionSecurity(watermark)
	content, err := service.AuthorizeDownload(context.Background(), actor, 7, token, DownloadMetadata{})
	if err != nil {
		t.Fatal(err)
	}
	if err = content.Complete(context.Background(), true, ""); err != nil {
		t.Fatal(err)
	}
	if watermark.trackingCode != "tracking-secret-1" || len(repo.events) != 1 {
		t.Fatalf("tracking=%q events=%+v", watermark.trackingCode, repo.events)
	}
	want := watermarkTrackingDigest(actor, 7, watermark.trackingCode)
	if repo.events[0].TrackingDigest != want || repo.events[0].TrackingDigest == watermark.trackingCode || len(repo.events[0].TrackingDigest) != 64 {
		t.Fatalf("tracking digest=%q want=%q", repo.events[0].TrackingDigest, want)
	}
	other := actor
	other.AccountID = "another-account"
	if watermarkTrackingDigest(other, 7, watermark.trackingCode) == want {
		t.Fatal("tracking digest is not bound to the account scope")
	}
}
