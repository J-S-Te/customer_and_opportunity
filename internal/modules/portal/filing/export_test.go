package filing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

type exportRendererStub struct {
	rows    []string
	content []byte
	err     error
}

func (r *exportRendererStub) RenderRows(_ context.Context, rows []string) ([]byte, error) {
	r.rows = rows
	return r.content, r.err
}

type exportAuditFailRepo struct{ *memoryRepository }

func (r exportAuditFailRepo) CreateAction(context.Context, *Action) error {
	return errors.New("audit database unavailable")
}

func TestFilingExportIsOwnedVersionedAndAuditedBeforeReturningBytes(t *testing.T) {
	service, repo, _ := testService()
	view, err := service.Create(context.Background(), actorA(), CreateCommand{IdempotencyKey: "export-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	renderer := &exportRendererStub{content: []byte("%PDF-1.7\nfixture")}
	service.UseExportRenderer(renderer)
	actor := actorA()
	actor.CustomerID++
	if _, err := service.Export(context.Background(), actor, view.ID, view.Version); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	actor = actorA()
	actor.TenantID = "other"
	if _, err := service.Export(context.Background(), actor, view.ID, view.Version); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := service.Export(context.Background(), actorA(), view.ID, view.Version+1); !errors.Is(err, ErrVersionConflict) {
		t.Fatal(err)
	}
	result, err := service.Export(context.Background(), actorA(), view.ID, view.Version)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(result.Content)
	if result.SHA256 != hex.EncodeToString(digest[:]) || !strings.Contains(strings.Join(renderer.rows, "\n"), "不等同于公安受理") {
		t.Fatalf("result=%+v rows=%v", result, renderer.rows)
	}
	if repo.actions[len(repo.actions)-1].Action != "EXPORT_PDF" || repo.actions[len(repo.actions)-1].ActorID != actorA().AccountID {
		t.Fatal("missing export audit")
	}
	service.repo = exportAuditFailRepo{repo}
	if value, err := service.Export(context.Background(), actorA(), view.ID, view.Version); err == nil || value != nil {
		t.Fatalf("audit fail returned PDF: %v %v", value, err)
	}
}

func TestFilingExportFailsClosedOnUnavailableMalformedPDFOrRenderError(t *testing.T) {
	service, _, _ := testService()
	view, err := service.Create(context.Background(), actorA(), CreateCommand{IdempotencyKey: "export-unavailable"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Export(context.Background(), actorA(), view.ID, view.Version); !errors.Is(err, ErrExportUnavailable) {
		t.Fatal(err)
	}
	for _, renderer := range []*exportRendererStub{{content: []byte("not pdf")}, {content: []byte("%PDF-" + strings.Repeat("x", 2<<20))}, {err: errors.New("renderer failed")}} {
		service.UseExportRenderer(renderer)
		if value, err := service.Export(context.Background(), actorA(), view.ID, view.Version); err == nil || value != nil {
			t.Fatalf("value=%v err=%v", value, err)
		}
	}
	service.UseExportRenderer(&exportRendererStub{content: []byte("%PDF-fixture")})
	service.exportSlots <- struct{}{}
	service.exportSlots <- struct{}{}
	if _, err := service.Export(context.Background(), actorA(), view.ID, view.Version); err == nil {
		t.Fatal("concurrency bound ignored")
	}
}
