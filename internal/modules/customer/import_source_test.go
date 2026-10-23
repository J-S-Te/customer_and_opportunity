package customer

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/filegatewayclient"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
)

type importSideEffectGateway struct{ calls int }

func (g *importSideEffectGateway) StoreImport(context.Context, string, string, string, []byte) error {
	g.calls++
	return nil
}

// 持久化边界返回副本：保存失败不得意外
// 修改持久状态，正如进程退出无法提交内存中的 ID。
type importSourceMemoryStore struct {
	source      ImportSource
	job         ImportJob
	failSave    bool
	wrongTenant bool
}

func (r *importSourceMemoryStore) CreateImportSource(_ context.Context, source *ImportSource) error {
	r.source = *source
	return nil
}

func (r *importSourceMemoryStore) ClaimImportSource(_ context.Context, tenantID, token string, now, until time.Time) (*ImportSource, *ImportJob, error) {
	if r.source.Status == "RELEASED" || (!r.wrongTenant && r.source.TenantID != tenantID) {
		return nil, nil, nil
	}
	r.source.LockedBy, r.source.LockedUntil = token, &until
	source, job := r.source, r.job
	return &source, &job, nil
}

func (r *importSourceMemoryStore) SaveImportSource(_ context.Context, source *ImportSource, token string, now time.Time) error {
	if r.failSave {
		r.failSave = false
		return errors.New("simulated process loss before durable confirmation")
	}
	if token != r.source.LockedBy || r.source.LockedUntil == nil || !r.source.LockedUntil.After(now) {
		return ErrImportJobConflict
	}
	r.source = *source
	r.source.LockedBy, r.source.LockedUntil = "", nil
	return nil
}

func sourceFixture(t *testing.T) (*Service, *importSourceMemoryStore, *time.Time) {
	t.Helper()
	now := time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)
	svc := &Service{codec: profileCodec(t), now: func() time.Time { return now }}
	cipher, err := svc.codec.Encrypt("source-content")
	if err != nil {
		t.Fatal(err)
	}
	const jobNo = "0123456789abcdef0123456789abcdef"
	store := &importSourceMemoryStore{
		source: ImportSource{ID: 1, TenantID: "tenant-a", JobID: 7, JobNo: jobNo, ActorID: "actor-a", RequestID: "crm-customer-import-" + jobNo, Filename: "customers.xlsx", ContentCipher: cipher, Status: "PENDING"},
		job:    ImportJob{ID: 7, TenantID: "tenant-a", JobNo: jobNo, ActorID: "actor-a", Status: "PREVIEWED", ExpiresAt: now.Add(30 * time.Minute)},
	}
	svc.importSources = store
	return svc, store, &now
}

type importHTTPState struct {
	ready                       bool
	sessions, uploads, releases int
	releaseFails                bool
	requestID                   string
}

func importHTTPGateway(t *testing.T, state *importHTTPState) *ImportGatewayAdapter {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/v2/upload-sessions":
			state.sessions++
			if r.Header.Get("Authorization") != "Bearer machine-scoped-token" {
				t.Error("source session lost machine authorization")
			}
			var input map[string]any
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
			}
			if input["purpose"] != "crm.customer.import" || input["actor_user_id"] != "actor-a" || input["resource_type"] != "CUSTOMER_IMPORT" || input["binding_type"] != "SOURCE" || input["resource_id"] != "0123456789abcdef0123456789abcdef" || input["sha256"] != "5d92f6c4f5cd91d48ccb51bd92559a55f16819ef278898a09c45315c9bff428c" || input["size_bytes"] != float64(14) {
				t.Errorf("source binding authority or immutable content changed: %+v", input)
			}
			id, _ := input["idempotency_key"].(string)
			if state.requestID != "" && state.requestID != id {
				t.Error("retry changed immutable request identity")
			}
			state.requestID = id
			status := "CREATED"
			if state.ready {
				status = "READY"
			}
			_, _ = io.WriteString(w, `{"data":{"upload_id":"upload-1","file_id":"file-1","status":"`+status+`","upload_url":"/file-gateway/api/v2/upload-sessions/upload-1/content"}}`)
		case r.Method == "POST" && r.URL.Path == "/api/v2/upload-sessions/upload-1/tickets":
			_, _ = io.WriteString(w, `{"data":{"ticket":"ticket-1","upload_url":"/file-gateway/api/v2/upload-sessions/upload-1/content","expires_at":"2026-10-22T10:00:00Z"}}`)
		case r.Method == "PUT" && r.URL.Path == "/api/v2/upload-sessions/upload-1/content":
			state.uploads++
			content, _ := io.ReadAll(r.Body)
			if string(content) != "source-content" || r.Header.Get("Authorization") != "UploadTicket ticket-1" {
				t.Error("upload lost source or ticket")
			}
			state.ready = true
			_, _ = io.WriteString(w, `{"data":{"binding_id":"binding-1"}}`)
		case r.Method == "POST" && r.URL.Path == "/api/v1/files/file-1/bindings/release":
			state.releases++
			var input map[string]string
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
			}
			if input["application_id"] != "app-a" || input["resource_type"] != "CUSTOMER_IMPORT" || input["resource_id"] != "0123456789abcdef0123456789abcdef" || input["binding_type"] != "SOURCE" || r.Header.Get("Authorization") != "Bearer machine-scoped-token" {
				t.Errorf("release changed original tuple: %+v", input)
			}
			if state.releaseFails {
				http.Error(w, "retry later", 503)
				return
			}
			_, _ = io.WriteString(w, `{"data":{}}`)
		default:
			t.Errorf("unexpected gateway request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	client, err := filegatewayclient.New(server.URL, server.Client(), func(context.Context) (string, error) { return "machine-scoped-token", nil })
	if err != nil {
		t.Fatal(err)
	}
	return NewImportGatewayAdapter(client, "app-a")
}

func allowImportSource(context.Context, bool) error { return nil }

func TestImportSourceRecoversUploadBeforeLocalConfirmationAndRetriesRelease(t *testing.T) {
	svc, store, now := sourceFixture(t)
	state := &importHTTPState{}
	svc.importGateway = importHTTPGateway(t, state)
	store.failSave = true
	if worked, err := svc.ProcessImportSource(context.Background(), "tenant-a", allowImportSource); !worked || err == nil {
		t.Fatalf("lost confirmation worked=%v error=%v", worked, err)
	}
	if store.source.FileID != "" || state.uploads != 1 {
		t.Fatalf("failed confirmation persisted in-memory receipt: %+v uploads=%d", store.source, state.uploads)
	}
	*now = now.Add(3 * time.Minute)
	if _, err := svc.ProcessImportSource(context.Background(), "tenant-a", allowImportSource); err != nil {
		t.Fatal(err)
	}
	if store.source.Status != "UPLOADED" || store.source.FileID != "file-1" || state.uploads != 1 || state.sessions != 2 {
		t.Fatalf("replay did not recover existing file: status=%s file=%s uploads=%d sessions=%d", store.source.Status, store.source.FileID, state.uploads, state.sessions)
	}
	store.job.Status = "COMPLETED"
	state.releaseFails = true
	if _, err := svc.ProcessImportSource(context.Background(), "tenant-a", allowImportSource); err == nil {
		t.Fatal("release failure was swallowed")
	}
	if store.source.Status != "UPLOADED" || len(store.source.ContentCipher) == 0 || !store.source.NextAttemptAt.After(*now) {
		t.Fatal("release failure discarded recoverable intent")
	}
	state.releaseFails = false
	*now = now.Add(30 * time.Minute)
	if _, err := svc.ProcessImportSource(context.Background(), "tenant-a", allowImportSource); err != nil {
		t.Fatal(err)
	}
	if store.source.Status != "RELEASED" || store.source.ContentCipher != nil || state.releases != 2 || store.job.Status != "COMPLETED" {
		t.Fatal("release did not clear only temporary source")
	}
}

func TestExpiredImportSourceResolvesIdentityWithoutUploadingBytes(t *testing.T) {
	svc, store, now := sourceFixture(t)
	state := &importHTTPState{}
	svc.importGateway = importHTTPGateway(t, state)
	*now = now.Add(time.Hour)
	var cleanupChecks int
	if _, err := svc.ProcessImportSource(context.Background(), "tenant-a", func(_ context.Context, cleanup bool) error {
		if !cleanup {
			t.Error("expired source used business upload authorization")
		}
		cleanupChecks++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if store.source.Status != "RELEASED" || state.sessions != 1 || state.uploads != 0 || state.releases != 1 || cleanupChecks != 2 {
		t.Fatalf("expired source state=%s sessions=%d uploads=%d releases=%d checks=%d", store.source.Status, state.sessions, state.uploads, state.releases, cleanupChecks)
	}
}

func TestImportSourceFailsClosedForLicenseTenantAndCorruptContent(t *testing.T) {
	for _, kind := range []string{"license", "tenant", "corrupt content"} {
		t.Run(kind, func(t *testing.T) {
			svc, store, _ := sourceFixture(t)
			state := &importHTTPState{}
			svc.importGateway = importHTTPGateway(t, state)
			authorize := allowImportSource
			switch kind {
			case "license":
				authorize = func(context.Context, bool) error { return errors.New("denied") }
			case "tenant":
				store.wrongTenant = true
				store.source.TenantID = "tenant-b"
			case "corrupt content":
				store.source.ContentCipher = []byte(strings.Repeat("x", 40))
			}
			if _, err := svc.ProcessImportSource(context.Background(), "tenant-a", authorize); err == nil {
				t.Fatal("unsafe task accepted")
			}
			if state.sessions != 0 || state.uploads != 0 || state.releases != 0 {
				t.Fatal("unsafe task escaped to gateway")
			}
		})
	}
}

// 在任一拒绝检查之前上传，都会产生没有对应任务的 ACTIVE 绑定。
func TestRejectedImportNeverCreatesExternalSource(t *testing.T) {
	for _, tc := range []struct {
		name string
		scan error
		want error
	}{
		{"scanner rejected", ErrImportFileUnsafe, ErrImportScanFailed},
		{"scanner unavailable", errors.New("scanner unavailable"), ErrImportScannerUnavailable},
		{"workbook invalid", nil, ErrImportInvalidFile},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gateway := &importSideEffectGateway{}
			svc := &Service{imports: &GORMRepository{}, scanner: importScannerStub{err: tc.scan}, importGateway: gateway}
			ctx := auth.WithPrincipal(context.Background(), auth.Principal{TenantID: "tenant-a", UserID: "actor-a"})
			_, err := svc.PreviewImport(ctx, []byte("PK\x03\x04invalid-workbook"), "approved import")
			if !errors.Is(err, tc.want) {
				t.Fatalf("error=%v, want %v", err, tc.want)
			}
			if gateway.calls != 0 {
				t.Fatalf("rejected preview created %d external sources before a durable job existed", gateway.calls)
			}
		})
	}
}
