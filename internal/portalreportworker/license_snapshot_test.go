package portalreportworker

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"path/filepath"
	"testing"
	"time"

	core "github.com/J-S-Te/license-core"
	runtime "github.com/J-S-Te/license-core/runtime"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/report"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/workerlicense"
)

func TestSignedPortalExpiryDefersTasksAndResumesSameEventAfterRenewal(t *testing.T) {
	for _, state := range []runtime.EnforcementState{runtime.Pending, runtime.Applying, runtime.Enforced} {
		t.Run(string(state), func(t *testing.T) {
			platformPublic, platformPrivate, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			vendorPublic, vendorPrivate, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Unix(1800000000, 0).UTC()
			expires := now.Add(time.Minute)
			binding := runtime.Binding{InstanceID: "portal-worker-test", Environment: "production", Application: "customer_portal", ServiceID: "portal-report-worker"}
			path := filepath.Join(t.TempDir(), "state.json")
			platformKeys := map[string]ed25519.PublicKey{"platform-test": platformPublic}
			vendorKeys := map[string]ed25519.PublicKey{"vendor-test": vendorPublic}
			reopen := func() *runtime.FileRuntime {
				t.Helper()
				rt, err := runtime.NewFileRuntime(path, binding, platformKeys, vendorKeys)
				if err != nil {
					t.Fatal(err)
				}
				return rt
			}
			rt := reopen()
			apply := func(version uint64, until time.Time) {
				t.Helper()
				license, err := core.Sign(core.License{
					ProtocolVersion: core.ProtocolVersion, Issuer: core.Issuer, ID: "portal-report-test", Version: version,
					CustomerID: "test-customer", ProductID: core.Product, Environment: binding.Environment, InstanceID: binding.InstanceID,
					IssuedAt: now.Add(-time.Minute).Unix(), NotBefore: now.Add(-time.Minute).Unix(),
					Applications: []core.Application{{Code: binding.Application, Kind: "FULL", NotBefore: now.Add(-time.Minute).Unix(), ExpiresAt: until.Unix()}},
				}, "vendor-test", vendorPrivate)
				if err != nil {
					t.Fatal(err)
				}
				snapshot, err := runtime.SignSnapshot(runtime.PlatformSnapshot{
					Protocol: runtime.Protocol, InstanceID: binding.InstanceID, Environment: binding.Environment,
					Application: binding.Application, ServiceID: binding.ServiceID, Revision: version,
					EnforcementState: state, MigrationEligible: state == runtime.Pending,
					CurrentLicenseJWS: license, HighestLicenseVersion: version, SnapshotIssuedAt: now.Unix(),
				}, "platform-test", platformPrivate)
				if err != nil {
					t.Fatal(err)
				}
				if err := rt.ApplySnapshot(context.Background(), snapshot, now); err != nil {
					t.Fatal(err)
				}
			}
			apply(1, expires)
			ctx := workerlicense.WithCheck(context.Background(), func(ctx context.Context, operation core.Operation) error {
				return rt.Evaluate(ctx, operation, now)
			})
			initial := report.Outbox{ID: 1, EventID: "before-expiry", TenantID: "tenant-test", EventType: "PORTAL_REPORT_SUBMITTED", AggregateID: 41}
			store := &memoryStore{events: []report.Outbox{initial}}
			projection := &projectionStub{}
			project := &projectStub{downstream: "report-approval"}
			worker := testWorker(store, projection, project)
			worker.now = func() time.Time { return now }
			if count, err := worker.RunOnce(ctx); err != nil || count != 1 || project.calls != 1 || store.sentCount != 1 {
				t.Fatalf("valid signed license did not allow delivery: count=%d err=%v", count, err)
			}
			pending := report.Outbox{ID: 2, EventID: "stable-renewal-event", TenantID: "tenant-test", EventType: "PORTAL_REPORT_SUBMITTED", AggregateID: 42}
			store.events = []report.Outbox{pending}
			for _, stage := range []string{"expiry-boundary", "offline-after-expiry", "reopened-after-expiry"} {
				switch stage {
				case "expiry-boundary":
					now = expires
				case "offline-after-expiry":
					now = expires.Add(time.Hour)
				case "reopened-after-expiry":
					rt = reopen()
				}
				if count, err := worker.RunOnce(ctx); count != 0 || err != nil {
					t.Fatalf("%s acquired expired tasks: count=%d err=%v", stage, count, err)
				}
				if err := worker.dispatch(ctx, pending); !errors.Is(err, workerlicense.ErrDenied) {
					t.Fatalf("%s did not defer a previously leased task: %v", stage, err)
				}
				// 摄取流程也在访问任何依赖或租约前检查签名权益；
				// nil 依赖使意外绕过能够被发现。
				ingest := &IngestWorker{}
				if count, err := ingest.RunOnce(ctx); count != 0 || err != nil {
					t.Fatalf("%s acquired expired ingest: count=%d err=%v", stage, count, err)
				}
				if err := ingest.dispatch(ctx, report.IngestJob{}); !errors.Is(err, workerlicense.ErrDenied) {
					t.Fatalf("%s did not defer leased ingest: %v", stage, err)
				}
				if len(store.events) != 1 || store.events[0].EventID != pending.EventID || store.failedCount != 0 || store.sentCount != 1 || project.calls != 1 || len(projection.requests) != 1 {
					t.Fatalf("%s consumed a pending event or performed business work", stage)
				}
			}
			apply(2, now.Add(time.Hour))
			if count, err := worker.RunOnce(ctx); err != nil || count != 1 || project.calls != 2 || store.sentCount != 2 || store.failedCount != 0 || len(projection.requests) != 2 {
				t.Fatalf("signed renewal did not resume the unchanged event: count=%d err=%v", count, err)
			}
			if len(project.eventIDs) != 2 || project.eventIDs[1] != pending.EventID {
				t.Fatal("renewal changed the pending event identity")
			}
		})
	}
}
