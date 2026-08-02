package machineauth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

func TestAuthenticateValidApplicationTokenConsumesReplay(t *testing.T) {
	now := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	store := &fakeReplayStore{}
	authenticator := testAuthenticator(validClaims(now), store, now)
	request := machineRequest(now, "abcdefghijklmnopqrstuvwxyzABCDEF")
	principal, err := authenticator.Authenticate(context.Background(), request)
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if principal.UserID != "machine:crm-portal-mapping" || principal.TenantID != "tenant-a" || !principal.HasPermission("portal.identity_mapping.provision") {
		t.Fatalf("unexpected principal: %#v", principal)
	}
	if store.hash == "" || !store.expiresAt.Equal(now.Add(requestSkew)) {
		t.Fatalf("replay was not consumed through token expiry: %#v", store)
	}
}

func TestAuthenticateRejectsClaimTimestampNonceAndReplayFailures(t *testing.T) {
	now := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		claims claims
		mutate func(*http.Request)
		store  *fakeReplayStore
	}{
		{name: "wrong token use", claims: mutateClaims(validClaims(now), func(value *claims) { value.TokenUse = "access_token" }), store: &fakeReplayStore{}},
		{name: "wrong tenant", claims: mutateClaims(validClaims(now), func(value *claims) { value.TenantID = "tenant-b" }), store: &fakeReplayStore{}},
		{name: "expired", claims: mutateClaims(validClaims(now), func(value *claims) { value.ExpiresAt = now }), store: &fakeReplayStore{}},
		{name: "missing issued at", claims: mutateClaims(validClaims(now), func(value *claims) { value.IssuedAt = time.Time{} }), store: &fakeReplayStore{}},
		{name: "missing not before", claims: mutateClaims(validClaims(now), func(value *claims) { value.NotBefore = time.Time{} }), store: &fakeReplayStore{}},
		{name: "future issued at", claims: mutateClaims(validClaims(now), func(value *claims) {
			value.IssuedAt = now.Add(requestSkew + time.Second)
			value.NotBefore = value.IssuedAt
		}), store: &fakeReplayStore{}},
		{name: "future not before", claims: mutateClaims(validClaims(now), func(value *claims) { value.NotBefore = now.Add(requestSkew + time.Second) }), store: &fakeReplayStore{}},
		{name: "unknown scope", claims: mutateClaims(validClaims(now), func(value *claims) { value.Scopes = []string{"project.snapshot.read"} }), store: &fakeReplayStore{}},
		{name: "duplicate scope", claims: mutateClaims(validClaims(now), func(value *claims) { value.Scopes = []string{"report.callback.write", "report.callback.write"} }), store: &fakeReplayStore{}},
		{name: "stale timestamp", claims: validClaims(now), mutate: func(request *http.Request) {
			request.Header.Set("X-Integration-Timestamp", now.Add(-requestSkew-time.Nanosecond).Format(time.RFC3339Nano))
		}, store: &fakeReplayStore{}},
		{name: "future timestamp", claims: validClaims(now), mutate: func(request *http.Request) {
			request.Header.Set("X-Integration-Timestamp", now.Add(requestSkew+time.Nanosecond).Format(time.RFC3339Nano))
		}, store: &fakeReplayStore{}},
		{name: "weak nonce", claims: validClaims(now), mutate: func(request *http.Request) { request.Header.Set("X-Integration-Nonce", "short") }, store: &fakeReplayStore{}},
		{name: "replayed nonce", claims: validClaims(now), store: &fakeReplayStore{err: errReplay}},
	}
	for _, item := range tests {
		t.Run(item.name, func(t *testing.T) {
			authenticator := testAuthenticator(item.claims, item.store, now)
			request := machineRequest(now, "abcdefghijklmnopqrstuvwxyzABCDEF")
			if item.mutate != nil {
				item.mutate(request)
			}
			if _, err := authenticator.Authenticate(context.Background(), request); !errors.Is(err, errUnauthenticated) {
				t.Fatalf("Authenticate() error = %v", err)
			}
		})
	}
}

func TestAuthenticateRejectsVerifierFailureAndPreservesStorageFailure(t *testing.T) {
	now := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	request := machineRequest(now, "abcdefghijklmnopqrstuvwxyzABCDEF")
	verificationFailure := newAuthenticator(fakeVerifier{err: errors.New("bad signature")}, &fakeReplayStore{}, "tenant-a", func() time.Time { return now })
	if _, err := verificationFailure.Authenticate(context.Background(), request); !errors.Is(err, errUnauthenticated) {
		t.Fatalf("signature error = %v", err)
	}
	databaseError := errors.New("database unavailable")
	storageFailure := testAuthenticator(validClaims(now), &fakeReplayStore{err: databaseError}, now)
	if _, err := storageFailure.Authenticate(context.Background(), request); !errors.Is(err, databaseError) {
		t.Fatalf("storage error = %v", err)
	}
}

func TestDuplicateReplayRecognizesGORMAndMySQL(t *testing.T) {
	if !duplicateReplay(gorm.ErrDuplicatedKey) || !duplicateReplay(&mysqlDriver.MySQLError{Number: 1062}) {
		t.Fatal("duplicate-key errors were not recognized")
	}
	if duplicateReplay(&mysqlDriver.MySQLError{Number: 1205}) {
		t.Fatal("lock timeout was mistaken for a duplicate")
	}
}

func TestFeedbackManagementScopeIsAnAllowedSingleMachineScope(t *testing.T) {
	now := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	value := validClaims(now)
	value.Scopes = []string{"portal.feedback.manage"}
	principal, err := testAuthenticator(value, &fakeReplayStore{}, now).Authenticate(context.Background(), machineRequest(now, "abcdefghijklmnopqrstuvwxyzABCDEF"))
	if err != nil || !principal.HasPermission("portal.feedback.manage") {
		t.Fatalf("feedback management scope principal=%#v err=%v", principal, err)
	}
}

func TestEvaluationReadScopeIsAnAllowedSingleMachineScope(t *testing.T) {
	now := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	value := validClaims(now)
	value.Scopes = []string{"portal.evaluation.read"}
	principal, err := testAuthenticator(value, &fakeReplayStore{}, now).Authenticate(context.Background(), machineRequest(now, "abcdefghijklmnopqrstuvwxyzABCDEF"))
	if err != nil || !principal.HasPermission("portal.evaluation.read") {
		t.Fatalf("evaluation read scope principal=%#v err=%v", principal, err)
	}
}

func TestIdentityDisableScopePreservesVerifiedClientSubjectAndNonceBinding(t *testing.T) {
	now := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	value := validClaims(now)
	value.Subject = "crm-portal-disable"
	value.OAuthClientID = "oauth-disable-row"
	value.Scopes = []string{"portal.identity_mapping.disable"}
	store := &fakeReplayStore{}
	const nonce = "disableNonce12345678901234567890"
	principal, err := testAuthenticator(value, store, now).Authenticate(context.Background(), machineRequest(now, nonce))
	if err != nil {
		t.Fatalf("Authenticate() error=%v", err)
	}
	if principal.UserID != "machine:crm-portal-disable" || !principal.HasPermission("portal.identity_mapping.disable") {
		t.Fatalf("unexpected disable principal: %#v", principal)
	}
	wantHash := hashReplay("tenant-a", "oauth-disable-row", "crm-portal-disable", nonce)
	if store.hash != wantHash {
		t.Fatalf("replay hash=%q want=%q", store.hash, wantHash)
	}
	if wantHash == hashReplay("tenant-a", "another-oauth-row", "crm-portal-disable", nonce) ||
		wantHash == hashReplay("tenant-a", "oauth-disable-row", "other-subject", nonce) {
		t.Fatal("nonce replay identity must bind both OAuth client row and subject")
	}
}

func TestFilingUnlockScopeIsAnAllowedSingleMachineScope(t *testing.T) {
	now := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	value := validClaims(now)
	value.Scopes = []string{"portal.filing.unlock"}
	principal, err := testAuthenticator(value, &fakeReplayStore{}, now).Authenticate(context.Background(), machineRequest(now, "abcdefghijklmnopqrstuvwxyzABCDEF"))
	if err != nil || !principal.HasPermission("portal.filing.unlock") {
		t.Fatalf("filing unlock scope principal=%#v err=%v", principal, err)
	}
}

func TestFilingMaterialScanScopeUsesVerifiedTokenTenant(t *testing.T) {
	now := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	value := validClaims(now)
	value.Subject = "filing-material-scanner"
	value.Scopes = []string{"portal.filing_material.scan.write"}
	authenticator := testAuthenticator(value, &fakeReplayStore{}, now)
	principal, err := authenticator.Authenticate(context.Background(), machineRequest(now, "filingMaterialScanNonce0000000001"))
	if err != nil || principal.TenantID != "tenant-a" || !principal.HasPermission("portal.filing_material.scan.write") {
		t.Fatalf("filing material scanner principal=%#v err=%v", principal, err)
	}

	wrongTenant := value
	wrongTenant.TenantID = "tenant-b"
	if _, err = testAuthenticator(wrongTenant, &fakeReplayStore{}, now).Authenticate(context.Background(), machineRequest(now, "filingMaterialScanNonce0000000002")); !errors.Is(err, errUnauthenticated) {
		t.Fatalf("cross-tenant filing material scanner error=%v", err)
	}
}

func TestProjectHistoryReadScopeIsAnAllowedSingleMachineScope(t *testing.T) {
	now := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	value := validClaims(now)
	value.Scopes = []string{"portal.project_history.read"}
	principal, err := testAuthenticator(value, &fakeReplayStore{}, now).Authenticate(context.Background(), machineRequest(now, "abcdefghijklmnopqrstuvwxyzABCDEF"))
	if err != nil || !principal.HasPermission("portal.project_history.read") {
		t.Fatalf("project history principal=%#v err=%v", principal, err)
	}
}

func validClaims(now time.Time) claims {
	return claims{
		Subject: "crm-portal-mapping", OAuthClientID: "oauth-row-1", TenantID: "tenant-a", TokenUse: "application",
		Scopes: []string{"portal.identity_mapping.provision"}, IssuedAt: now.Add(-time.Minute), NotBefore: now.Add(-time.Minute), ExpiresAt: now.Add(10 * time.Minute),
	}
}

func mutateClaims(value claims, mutate func(*claims)) claims {
	mutate(&value)
	return value
}

func machineRequest(now time.Time, nonce string) *http.Request {
	request, _ := http.NewRequest(http.MethodPost, "https://portal.example/internal", nil)
	request.Header.Set("Authorization", "Bearer signed-token")
	request.Header.Set("X-Integration-Timestamp", now.Format(time.RFC3339Nano))
	request.Header.Set("X-Integration-Nonce", nonce)
	return request
}

func testAuthenticator(value claims, store replayStore, now time.Time) *Authenticator {
	return newAuthenticator(fakeVerifier{claims: value}, store, "tenant-a", func() time.Time { return now })
}

type fakeVerifier struct {
	claims claims
	err    error
}

func (f fakeVerifier) Verify(context.Context, string) (claims, error) { return f.claims, f.err }

type fakeReplayStore struct {
	hash      string
	expiresAt time.Time
	err       error
}

func (s *fakeReplayStore) Consume(_ context.Context, hash string, expiresAt, _ time.Time) error {
	s.hash, s.expiresAt = hash, expiresAt
	return s.err
}
