package filing

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

type memoryRepository struct {
	filings     []Filing
	sections    []Section
	matrices    []MatrixSelection
	submissions []SubmissionSnapshot
	outbox      []SubmissionOutbox
	materials   []Material
	actions     []Action
}

func (r *memoryRepository) WithTransaction(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}
func (r *memoryRepository) Create(_ context.Context, v *Filing) error {
	v.ID = uint64(len(r.filings) + 1)
	r.filings = append(r.filings, *v)
	return nil
}
func (r *memoryRepository) FindCreateAction(_ context.Context, a Actor, key string) (*Filing, error) {
	for i := range r.filings {
		v := &r.filings[i]
		if v.TenantID == a.TenantID && v.CustomerID == a.CustomerID && v.AccountID == a.AccountID && v.CreateIdempotencyKey == key && !v.DeletedAt.Valid {
			return v, nil
		}
	}
	return nil, ErrNotFound
}
func (r *memoryRepository) ListOwned(_ context.Context, a Actor, _, _ int) ([]Filing, int64, error) {
	var out []Filing
	for _, v := range r.filings {
		if v.TenantID == a.TenantID && v.CustomerID == a.CustomerID && !v.DeletedAt.Valid {
			out = append(out, v)
		}
	}
	return out, int64(len(out)), nil
}
func (r *memoryRepository) FindOwned(_ context.Context, a Actor, id string) (*Filing, error) {
	for i := range r.filings {
		v := &r.filings[i]
		if v.TenantID == a.TenantID && v.CustomerID == a.CustomerID && v.PublicID == id && !v.DeletedAt.Valid {
			return v, nil
		}
	}
	return nil, ErrNotFound
}
func (r *memoryRepository) FindOwnedForUpdate(ctx context.Context, a Actor, id string) (*Filing, error) {
	return r.FindOwned(ctx, a, id)
}
func (r *memoryRepository) DeleteDraftData(_ context.Context, tenant string, filingID uint64) error {
	var sections []Section
	for _, v := range r.sections {
		if v.TenantID != tenant || v.FilingID != filingID {
			sections = append(sections, v)
		}
	}
	r.sections = sections
	var matrices []MatrixSelection
	for _, v := range r.matrices {
		if v.TenantID != tenant || v.FilingID != filingID {
			matrices = append(matrices, v)
		}
	}
	r.matrices = matrices
	var materials []Material
	for _, v := range r.materials {
		if v.TenantID != tenant || v.FilingID != filingID {
			materials = append(materials, v)
		}
	}
	r.materials = materials
	return nil
}
func (r *memoryRepository) SoftDeleteFiling(_ context.Context, tenant string, filingID uint64, actor string, at time.Time) error {
	for i := range r.filings {
		v := &r.filings[i]
		if v.TenantID == tenant && v.ID == filingID {
			v.DeletedAt = gorm.DeletedAt{Time: at, Valid: true}
			v.UpdatedBy = actor
			v.UpdatedAt = at
			return nil
		}
	}
	return ErrNotFound
}

func (r *memoryRepository) FindInternalForUpdate(_ context.Context, tenant string, customer uint64, id string) (*Filing, error) {
	for i := range r.filings {
		v := &r.filings[i]
		if v.TenantID == tenant && v.CustomerID == customer && v.PublicID == id {
			return v, nil
		}
	}
	return nil, ErrNotFound
}
func (r *memoryRepository) UpdateFiling(_ context.Context, v *Filing, version uint64, fields map[string]any) error {
	if v.Version != version {
		return ErrVersionConflict
	}
	if x, ok := fields["status"].(string); ok {
		v.Status = x
	}
	if x, ok := fields["current_step"].(uint8); ok {
		v.CurrentStep = x
	}
	if x, ok := fields["completion_pct"].(uint8); ok {
		v.CompletionPct = x
	}
	if x, ok := fields["submitted_at"].(time.Time); ok {
		v.SubmittedAt = &x
	}
	if _, ok := fields["locked_at"]; ok {
		if x, valid := fields["locked_at"].(time.Time); valid {
			v.LockedAt = &x
		} else {
			v.LockedAt = nil
		}
	}
	if x, ok := fields["unlocked_at"].(time.Time); ok {
		v.UnlockedAt = &x
	}
	if x, ok := fields["unlock_reason_cipher"].([]byte); ok {
		v.UnlockReasonCipher = x
	}
	if x, ok := fields["updated_at"].(time.Time); ok {
		v.UpdatedAt = x
	}
	v.Version++
	return nil
}
func (r *memoryRepository) FindSection(_ context.Context, t string, id uint64, code string) (*Section, error) {
	for i := range r.sections {
		v := &r.sections[i]
		if v.TenantID == t && v.FilingID == id && v.SectionCode == code {
			return v, nil
		}
	}
	return nil, ErrNotFound
}
func (r *memoryRepository) ListSections(_ context.Context, t string, id uint64) ([]Section, error) {
	var out []Section
	for _, v := range r.sections {
		if v.TenantID == t && v.FilingID == id {
			out = append(out, v)
		}
	}
	return out, nil
}
func (r *memoryRepository) ListSectionsByFilingIDs(_ context.Context, t string, ids []uint64) ([]Section, error) {
	var out []Section
	byID := make(map[uint64]struct{}, len(ids))
	for _, id := range ids {
		byID[id] = struct{}{}
	}
	for _, v := range r.sections {
		if v.TenantID == t {
			if _, ok := byID[v.FilingID]; ok {
				out = append(out, v)
			}
		}
	}
	return out, nil
}
func (r *memoryRepository) CreateSection(_ context.Context, v *Section) error {
	v.ID = uint64(len(r.sections) + 1)
	r.sections = append(r.sections, *v)
	return nil
}
func (r *memoryRepository) UpdateSection(_ context.Context, v *Section, version uint64, data []byte, status, actor string, at time.Time) error {
	if v.Version != version {
		return ErrVersionConflict
	}
	v.DataCipher = data
	v.ValidationStatus = status
	v.UpdatedBy = actor
	v.UpdatedAt = at
	return nil
}
func (r *memoryRepository) FindMatrix(_ context.Context, t string, id uint64, code string) (*MatrixSelection, error) {
	for i := range r.matrices {
		v := &r.matrices[i]
		if v.TenantID == t && v.FilingID == id && v.MatrixCode == code {
			return v, nil
		}
	}
	return nil, ErrNotFound
}
func (r *memoryRepository) ListMatrices(_ context.Context, t string, id uint64) ([]MatrixSelection, error) {
	var out []MatrixSelection
	for _, v := range r.matrices {
		if v.TenantID == t && v.FilingID == id {
			out = append(out, v)
		}
	}
	return out, nil
}
func (r *memoryRepository) CreateMatrix(_ context.Context, v *MatrixSelection) error {
	v.ID = uint64(len(r.matrices) + 1)
	r.matrices = append(r.matrices, *v)
	return nil
}
func (r *memoryRepository) UpdateMatrix(_ context.Context, v *MatrixSelection, version uint64, row, column string, selected bool, actor string, at time.Time) error {
	if v.Version != version {
		return ErrVersionConflict
	}
	v.RowCode = row
	v.ColumnCode = column
	v.Selected = selected
	v.UpdatedBy = actor
	v.UpdatedAt = at
	return nil
}
func (r *memoryRepository) NextSubmissionSequence(_ context.Context, t string, id uint64) (uint64, error) {
	var n uint64
	for _, v := range r.submissions {
		if v.TenantID == t && v.FilingID == id && v.Sequence > n {
			n = v.Sequence
		}
	}
	return n + 1, nil
}
func (r *memoryRepository) CreateSubmission(_ context.Context, v *SubmissionSnapshot) error {
	v.ID = uint64(len(r.submissions) + 1)
	r.submissions = append(r.submissions, *v)
	return nil
}
func (r *memoryRepository) CreateSubmissionOutbox(_ context.Context, v *SubmissionOutbox) error {
	v.ID = uint64(len(r.outbox) + 1)
	r.outbox = append(r.outbox, *v)
	return nil
}
func (r *memoryRepository) CancelWaitingSubmissionOutbox(_ context.Context, tenant string, filingID uint64, at time.Time) error {
	for i := range r.outbox {
		if r.outbox[i].TenantID == tenant && r.outbox[i].FilingID == filingID && r.outbox[i].Status == "WAITING_CONTRACT" {
			r.outbox[i].Status = "CANCELED"
			r.outbox[i].SentAt = &at
		}
	}
	return nil
}
func (r *memoryRepository) CreateMaterial(_ context.Context, v *Material) error {
	v.ID = uint64(len(r.materials) + 1)
	r.materials = append(r.materials, *v)
	return nil
}
func (r *memoryRepository) FindMaterial(_ context.Context, tenant string, filingID uint64, code string) (*Material, error) {
	for i := range r.materials {
		value := &r.materials[i]
		if value.TenantID == tenant && value.FilingID == filingID && value.MaterialCode == code {
			return value, nil
		}
	}
	return nil, ErrMaterialNotFound
}
func (r *memoryRepository) FindMaterialByCreate(_ context.Context, tenant, actor, keyHash string) (*Material, error) {
	for i := range r.materials {
		value := &r.materials[i]
		if value.TenantID == tenant && value.CreateActorID == actor && value.CreateKeyHash == keyHash {
			return value, nil
		}
	}
	return nil, ErrMaterialNotFound
}
func (r *memoryRepository) FindMaterialByPublicIDForUpdate(_ context.Context, tenant string, filingID uint64, publicID string) (*Material, error) {
	for i := range r.materials {
		value := &r.materials[i]
		if value.TenantID == tenant && value.FilingID == filingID && value.PublicID == publicID {
			return value, nil
		}
	}
	return nil, ErrMaterialNotFound
}
func (r *memoryRepository) FindMaterialForScanUpdate(_ context.Context, tenant, publicID string) (*Material, error) {
	for i := range r.materials {
		value := &r.materials[i]
		if value.TenantID == tenant && value.PublicID == publicID {
			return value, nil
		}
	}
	return nil, ErrMaterialNotFound
}
func (r *memoryRepository) ListMaterials(_ context.Context, tenant string, filingID uint64) ([]Material, error) {
	var result []Material
	for _, value := range r.materials {
		if value.TenantID == tenant && value.FilingID == filingID {
			result = append(result, value)
		}
	}
	return result, nil
}
func (r *memoryRepository) UpdateMaterial(_ context.Context, value *Material, version uint64, fields map[string]any) error {
	if value.Version != version {
		return ErrVersionConflict
	}
	if next, ok := fields["object_version"].(string); ok {
		value.ObjectVersion = next
	}
	if next, ok := fields["scan_reference"].(string); ok {
		value.ScanReference = next
	}
	if next, ok := fields["scan_status"].(string); ok {
		value.ScanStatus = next
	}
	if next, ok := fields["uploaded_at"].(time.Time); ok {
		value.UploadedAt = &next
	}
	if next, ok := fields["scanned_at"].(time.Time); ok {
		value.ScannedAt = &next
	}
	if next, ok := fields["finalize_lease_until"].(time.Time); ok {
		value.FinalizeLeaseUntil = &next
	} else if _, ok := fields["finalize_lease_until"]; ok {
		value.FinalizeLeaseUntil = nil
	}
	value.Version++
	return nil
}
func (r *memoryRepository) LatestSubmission(_ context.Context, t string, id uint64) (*SubmissionSnapshot, error) {
	for i := len(r.submissions) - 1; i >= 0; i-- {
		v := &r.submissions[i]
		if v.TenantID == t && v.FilingID == id {
			return v, nil
		}
	}
	return nil, ErrNotFound
}
func (r *memoryRepository) FindAction(_ context.Context, t string, id uint64, actor, action, key string) (*Action, error) {
	for i := range r.actions {
		v := &r.actions[i]
		if v.TenantID == t && v.FilingID == id && v.ActorID == actor && v.Action == action && v.IdempotencyKey == key {
			return v, nil
		}
	}
	return nil, ErrNotFound
}
func (r *memoryRepository) CreateAction(_ context.Context, v *Action) error {
	v.ID = uint64(len(r.actions) + 1)
	r.actions = append(r.actions, *v)
	return nil
}

type spyProtector struct{ encrypts, decrypts int }

func (p *spyProtector) Encrypt(_ context.Context, v []byte) ([]byte, error) {
	p.encrypts++
	result := make([]byte, len(v)+1)
	result[0] = 0xA5
	for index := range v {
		result[index+1] = v[index] ^ 0xAA
	}
	return result, nil
}
func (p *spyProtector) Decrypt(_ context.Context, v []byte) ([]byte, error) {
	p.decrypts++
	if len(v) == 0 || v[0] != 0xA5 {
		return nil, errors.New("not ciphertext")
	}
	result := make([]byte, len(v)-1)
	for index := range result {
		result[index] = v[index+1] ^ 0xAA
	}
	return result, nil
}

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

type sequenceIDs struct{ n int }

func (i *sequenceIDs) NewID() string {
	i.n++
	return "0123456789abcdef0123456789abc" + string(rune('0'+i.n))
}

type projectAccessStub struct {
	allowed bool
	err     error
}

func (p projectAccessStub) Accessible(context.Context, string, uint64, string) (bool, error) {
	return p.allowed, p.err
}

func testService() (*Service, *memoryRepository, *spyProtector) {
	repo := &memoryRepository{}
	protector := &spyProtector{}
	return NewService(repo, protector, projectAccessStub{allowed: true}, fixedClock{time.Date(2026, 8, 1, 1, 2, 3, 0, time.UTC)}, &sequenceIDs{}), repo, protector
}
func actorA() Actor { return Actor{TenantID: "tenant-a", CustomerID: 7, AccountID: "sub-a"} }

func TestSchemaRejectsUnknownTrailingTypeAndConditionalFields(t *testing.T) {
	for name, raw := range map[string]string{
		"unknown": `{"unknown":"x"}`, "trailing": `{"system_name":"x"} garbage`, "type": `{"system_name":9}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := parseAndValidateSection(SectionClassifiedObject, []byte(raw)); err == nil && name != "type" {
				t.Fatalf("accepted %s", raw)
			} else if name == "type" && err != nil {
				t.Fatal(err)
			}
		})
	}
	_, issues, err := parseAndValidateSection(SectionClassifiedObject, mustJSON(map[string]any{"system_name": "x", "object_types": []string{"DATA_RESOURCE"}, "business_type": "PUBLIC_SERVICE", "business_description": "x", "service_scope": "NATIONAL", "service_audience": "PUBLIC", "deployment_scope": "WAN", "network_nature": "INTERNET", "launched_on": "2026-01-01", "is_subsystem": true}))
	if err != nil || !hasIssue(issues, "parent_system_name", "REQUIRED") || !hasIssue(issues, "parent_organization_name", "REQUIRED") {
		t.Fatalf("issues=%#v err=%v", issues, err)
	}
}

func TestCreateScopeReplayAndEncryptedSection(t *testing.T) {
	service, repo, protector := testService()
	created, err := service.Create(context.Background(), actorA(), CreateCommand{ProjectID: "P-1", IdempotencyKey: "create-key-1"})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := service.Create(context.Background(), actorA(), CreateCommand{ProjectID: "P-1", IdempotencyKey: "create-key-1"})
	if err != nil || replay.ID != created.ID || len(repo.filings) != 1 {
		t.Fatalf("replay=%#v err=%v", replay, err)
	}
	if _, err = service.Create(context.Background(), actorA(), CreateCommand{ProjectID: "P-2", IdempotencyKey: "create-key-1"}); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("err=%v", err)
	}
	if _, err = service.Get(context.Background(), Actor{TenantID: "tenant-a", CustomerID: 8, AccountID: "sub-x"}, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("IDOR err=%v", err)
	}
	data := validSection(SectionOrganization)
	section, err := service.SaveSection(context.Background(), actorA(), created.ID, SectionOrganization, SaveSectionCommand{Data: mustJSON(data), IdempotencyKey: "section-key-1"})
	if err != nil || section.Version != 1 {
		t.Fatalf("section=%#v err=%v", section, err)
	}
	if strings.Contains(string(repo.sections[0].DataCipher), "91440300100008888K") || len(repo.sections[0].DataCipher) == 0 || repo.sections[0].DataCipher[0] != 0xA5 {
		t.Fatalf("cipher=%q", repo.sections[0].DataCipher)
	}
	before := protector.encrypts
	replayed, err := service.SaveSection(context.Background(), actorA(), created.ID, SectionOrganization, SaveSectionCommand{Data: mustJSON(data), IdempotencyKey: "section-key-1"})
	if err != nil || replayed.Version != 1 || protector.decrypts == 0 || protector.encrypts != before {
		t.Fatalf("replay=%#v err=%v protector=%#v", replayed, err, protector)
	}
	if _, err = service.SaveSection(context.Background(), actorA(), created.ID, SectionOrganization, SaveSectionCommand{ExpectedVersion: 0, Data: mustJSON(data), IdempotencyKey: "section-key-2"}); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("version err=%v", err)
	}
}

func TestCreateRejectsProjectOutsideCustomerScope(t *testing.T) {
	repo, protector := &memoryRepository{}, &spyProtector{}
	service := NewService(repo, protector, projectAccessStub{allowed: false}, fixedClock{time.Date(2026, 8, 1, 1, 2, 3, 0, time.UTC)}, &sequenceIDs{})
	if _, err := service.Create(context.Background(), actorA(), CreateCommand{ProjectID: "P-OTHER", IdempotencyKey: "create-project-key"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("project IDOR err=%v", err)
	}
	if len(repo.filings) != 0 {
		t.Fatal("unauthorized project created filing")
	}
}

func TestCreateReplayDoesNotDependOnProjectAdapter(t *testing.T) {
	repo, protector := &memoryRepository{}, &spyProtector{}
	service := NewService(repo, protector, projectAccessStub{allowed: true}, fixedClock{time.Date(2026, 8, 1, 1, 2, 3, 0, time.UTC)}, &sequenceIDs{})
	first, err := service.Create(context.Background(), actorA(), CreateCommand{ProjectID: "P-1", IdempotencyKey: "project-replay-key"})
	if err != nil {
		t.Fatal(err)
	}
	service.projects = projectAccessStub{err: errors.New("project service down")}
	replay, err := service.Create(context.Background(), actorA(), CreateCommand{ProjectID: "P-1", IdempotencyKey: "project-replay-key"})
	if err != nil || replay.ID != first.ID {
		t.Fatalf("replay=%#v err=%v", replay, err)
	}
	if _, err = service.Create(context.Background(), actorA(), CreateCommand{ProjectID: "P-2", IdempotencyKey: "project-replay-key"}); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflict err=%v", err)
	}
}

func TestMatrixSingleSelectionSubmitSnapshotLockAndMachineUnlock(t *testing.T) {
	service, repo, protector := testService()
	view, err := service.Create(context.Background(), actorA(), CreateCommand{IdempotencyKey: "create-key-2"})
	if err != nil {
		t.Fatal(err)
	}
	for i, code := range SectionCodes {
		if _, err = service.SaveSection(context.Background(), actorA(), view.ID, code, SaveSectionCommand{Data: mustJSON(validSection(code)), IdempotencyKey: "section-all-" + string(rune('a'+i))}); err != nil {
			t.Fatalf("save %s: %v", code, err)
		}
	}
	current := repo.filings[0].Version
	first, err := service.SaveMatrix(context.Background(), actorA(), view.ID, MatrixBusinessInformation, SaveMatrixCommand{ExpectedFilingVersion: current, RowCode: "PUBLIC_INTEREST", ColumnCode: "SERIOUS_DAMAGE", Selected: true, IdempotencyKey: "matrix-key-1"})
	if err != nil {
		t.Fatal(err)
	}
	current = repo.filings[0].Version
	replaced, err := service.SaveMatrix(context.Background(), actorA(), view.ID, MatrixBusinessInformation, SaveMatrixCommand{ExpectedFilingVersion: current, ExpectedMatrixVersion: first.Version, RowCode: "PUBLIC_INTEREST", ColumnCode: "SERIOUS_DAMAGE", Selected: true, IdempotencyKey: "matrix-key-2"})
	if err != nil || replaced.Version != 2 || len(repo.matrices) != 1 {
		t.Fatalf("replace=%#v err=%v rows=%d", replaced, err, len(repo.matrices))
	}
	current = repo.filings[0].Version
	if _, err = service.SaveMatrix(context.Background(), actorA(), view.ID, MatrixSystemService, SaveMatrixCommand{ExpectedFilingVersion: current, RowCode: "PUBLIC_INTEREST", ColumnCode: "SERIOUS_DAMAGE", Selected: true, IdempotencyKey: "matrix-key-3"}); err != nil {
		t.Fatal(err)
	}
	current = repo.filings[0].Version
	scannedAt := time.Date(2026, 8, 1, 1, 1, 0, 0, time.UTC)
	repo.materials = append(repo.materials, Material{ID: 1, TenantID: "tenant-a", PublicID: "material-classification-report", FilingID: repo.filings[0].ID, MaterialCode: "CLASSIFICATION_REPORT", ObjectVersion: "version-1", FileName: "classification.pdf", MIMEType: "application/pdf", SizeBytes: 3, SHA256: strings.Repeat("a", 64), ScanStatus: MaterialClean, ScanReference: "scan-1", ScannedAt: &scannedAt, Version: 1})
	submitted, err := service.Submit(context.Background(), actorA(), view.ID, SubmitCommand{ExpectedVersion: current, IdempotencyKey: "submit-key-1"})
	if err != nil {
		t.Fatal(err)
	}
	if submitted.Status != StatusWaitingContract || len(repo.submissions) != 1 || strings.Contains(string(repo.submissions[0].CanonicalCipher), "91440300100008888K") || repo.submissions[0].SnapshotSHA256 == "" {
		t.Fatalf("submitted=%#v snapshot=%#v", submitted, repo.submissions)
	}
	if len(repo.outbox) != 1 || repo.outbox[0].Status != "WAITING_CONTRACT" || repo.outbox[0].ContractVersion != "portal.filing.submission-reference.v1" || repo.outbox[0].SubmissionID != repo.submissions[0].ID || repo.outbox[0].PayloadSHA256 == "" || strings.Contains(string(repo.outbox[0].Payload), "91440300100008888K") {
		t.Fatalf("unsafe or missing submission outbox: %+v", repo.outbox)
	}
	plain, err := protector.Decrypt(context.Background(), repo.submissions[0].CanonicalCipher)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Index(string(plain), SectionOrganization) > strings.Index(string(plain), SectionClassificationReport) || strings.Index(string(plain), MatrixBusinessInformation) > strings.Index(string(plain), MatrixSystemService) {
		t.Fatalf("snapshot order=%s", plain)
	}
	if _, err = service.SaveSection(context.Background(), actorA(), view.ID, SectionOrganization, SaveSectionCommand{ExpectedVersion: 1, Data: mustJSON(validSection(SectionOrganization)), IdempotencyKey: "locked-key-1"}); !errors.Is(err, ErrLocked) {
		t.Fatalf("locked err=%v", err)
	}
	if _, err = service.Unlock(context.Background(), InternalActor{TenantID: "tenant-a", ActorID: "machine:admin"}, view.ID, UnlockCommand{CustomerID: 8, ExpectedVersion: submitted.Version, Reason: "customer request", IdempotencyKey: "unlock-key-1"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("customer boundary err=%v", err)
	}
	unlocked, err := service.Unlock(context.Background(), InternalActor{TenantID: "tenant-a", ActorID: "machine:admin"}, view.ID, UnlockCommand{CustomerID: 7, ExpectedVersion: submitted.Version, Reason: "customer correction requested", IdempotencyKey: "unlock-key-1"})
	if err != nil || unlocked.Status != StatusDraft {
		t.Fatalf("unlock=%#v err=%v", unlocked, err)
	}
	if repo.outbox[0].Status != "CANCELED" {
		t.Fatalf("unlock left stale outbox claimable: %#v", repo.outbox[0])
	}
	replayed, err := service.Unlock(context.Background(), InternalActor{TenantID: "tenant-a", ActorID: "machine:admin"}, view.ID, UnlockCommand{CustomerID: 7, ExpectedVersion: submitted.Version, Reason: "customer correction requested", IdempotencyKey: "unlock-key-1"})
	if err != nil || replayed.Status != StatusDraft {
		t.Fatalf("unlock replay=%#v err=%v", replayed, err)
	}
	if len(repo.submissions) != 1 || repo.submissions[0].SnapshotSHA256 != submitted.Submission.SnapshotSHA256 {
		t.Fatal("unlock mutated immutable snapshot")
	}
	var unlockAction *Action
	for i := range repo.actions {
		if repo.actions[i].Action == "UNLOCK" {
			unlockAction = &repo.actions[i]
		}
	}
	if unlockAction == nil || strings.Contains(string(unlockAction.RequestCipher), "customer correction requested") {
		t.Fatalf("unlock audit request=%#v", unlockAction)
	}
	requestPlain, err := protector.Decrypt(context.Background(), unlockAction.RequestCipher)
	if err != nil || !strings.Contains(string(requestPlain), "customer correction requested") || !strings.Contains(string(requestPlain), `"customer_id":7`) {
		t.Fatalf("unlock request plain=%s err=%v", requestPlain, err)
	}
}

func TestAggregateRejectsMatrixAndComponentLevelMismatch(t *testing.T) {
	service, repo, protector := testService()
	view, err := service.Create(context.Background(), actorA(), CreateCommand{IdempotencyKey: "aggregate-mismatch-create"})
	if err != nil {
		t.Fatal(err)
	}
	for index, code := range SectionCodes {
		data := validSection(code)
		if code == SectionClassification {
			data["business_information_level"] = 2
		}
		if _, err = service.SaveSection(context.Background(), actorA(), view.ID, code, SaveSectionCommand{Data: mustJSON(data), IdempotencyKey: "aggregate-section-" + string(rune('a'+index))}); err != nil {
			t.Fatal(err)
		}
	}
	current := repo.filings[0].Version
	if _, err = service.SaveMatrix(context.Background(), actorA(), view.ID, MatrixBusinessInformation, SaveMatrixCommand{ExpectedFilingVersion: current, RowCode: "LEGAL_RIGHTS", ColumnCode: "GENERAL_DAMAGE", Selected: true, IdempotencyKey: "aggregate-matrix-1"}); err != nil {
		t.Fatal(err)
	}
	current = repo.filings[0].Version
	if _, err = service.SaveMatrix(context.Background(), actorA(), view.ID, MatrixSystemService, SaveMatrixCommand{ExpectedFilingVersion: current, RowCode: "LEGAL_RIGHTS", ColumnCode: "GENERAL_DAMAGE", Selected: true, IdempotencyKey: "aggregate-matrix-2"}); err != nil {
		t.Fatal(err)
	}
	result, err := service.Validate(context.Background(), actorA(), view.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []struct{ path, code string }{{"sections.CLASSIFICATION_REPORT.business_information_level", "CROSS_SECTION_MISMATCH"}, {"matrices.BUSINESS_INFORMATION.row_code", "REPORT_MISMATCH"}, {"matrices.BUSINESS_INFORMATION.column_code", "REPORT_MISMATCH"}, {"matrices.BUSINESS_INFORMATION", "LEVEL_MISMATCH"}, {"matrices.SYSTEM_SERVICE.row_code", "REPORT_MISMATCH"}, {"matrices.SYSTEM_SERVICE.column_code", "REPORT_MISMATCH"}, {"matrices.SYSTEM_SERVICE", "LEVEL_MISMATCH"}} {
		if !hasIssue(result.Issues, expected.path, expected.code) {
			t.Errorf("missing %s/%s issues=%#v", expected.path, expected.code, result.Issues)
		}
	}
	_ = protector
}

func TestMigrationStoresOnlyCiphertextBodies(t *testing.T) {
	raw, err := os.ReadFile("../../../../migrations/000031_portal_filing.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, required := range []string{"unlock_reason_cipher MEDIUMBLOB", "data_cipher MEDIUMBLOB", "canonical_cipher LONGBLOB", "request_cipher MEDIUMBLOB", "response_cipher MEDIUMBLOB", "uq_portal_filing_matrix", "uq_portal_filing_action"} {
		if !strings.Contains(text, required) {
			t.Fatalf("missing %q", required)
		}
	}
	for _, forbidden := range []string{"unlock_reason VARCHAR", "data_json", "canonical_json", "response_json", "portal_filing_materials", "portal_filing_exports"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("plaintext/deferred table present %q", forbidden)
		}
	}
}

func validSection(code string) map[string]any {
	switch code {
	case SectionOrganization:
		return map[string]any{"unit_name": "华兴证券股份有限公司", "social_credit_code": "91440300100008888K", "province": "广东省", "city": "深圳市", "district": "福田区", "address": "福华路1号", "organization_leader_name": "周明", "security_department": "信息部", "security_contact_name": "陈工", "affiliation": "CITY", "organization_type": "ENTERPRISE", "industry_code": "27", "level2_object_count": 0, "level3_object_count": 1, "level4_object_count": 0, "level5_object_count": 0}
	case SectionClassifiedObject:
		return map[string]any{"system_name": "核心系统", "object_types": []string{"DATA_RESOURCE"}, "business_type": "PUBLIC_SERVICE", "business_description": "核心业务", "service_scope": "NATIONAL", "service_audience": "PUBLIC", "deployment_scope": "WAN", "network_nature": "INTERNET", "launched_on": "2026-01-01", "is_subsystem": false}
	case SectionClassification:
		return map[string]any{"business_information_level": 3, "system_service_level": 3, "final_level": 3, "classified_on": "2026-01-01", "classification_report_available": true, "expert_reviewed": true, "has_industry_authority": false, "industry_authority_reviewed": false, "form_filler_name": "陈工", "form_filled_on": "2026-01-02"}
	case SectionNewTechnology:
		return map[string]any{"cloud_used": false, "mobile_used": false, "iot_used": false, "industrial_control_used": false, "big_data_used": false}
	case SectionMaterials:
		return map[string]any{"topology_available": false, "security_governance_available": false, "security_design_available": false, "security_products_available": false, "security_services_available": false, "authority_guidance_available": false}
	case SectionDataInventory:
		return map[string]any{"data_name": "交易数据", "proposed_data_level": "GENERAL", "data_category": "交易", "responsible_department": "数据部", "responsible_person": "孙工", "personal_information_types": []string{"NONE"}, "data_sources": []string{"GENERATED"}, "processor_interaction": "NONE", "storage_locations": []string{"DOMESTIC"}}
	default:
		return map[string]any{"responsible_entity_description": "责任主体", "object_composition_description": "对象构成", "business_description": "承载业务", "data_description": "承载数据", "security_responsibility_description": "安全责任", "business_information_description": "业务信息", "business_impact_object": "PUBLIC_INTEREST", "business_damage_degree": "SERIOUS", "business_information_level": 3, "system_service_description": "系统服务", "service_impact_object": "PUBLIC_INTEREST", "service_damage_degree": "SERIOUS", "system_service_level": 3, "final_level": 3}
	}
}
func mustJSON(value any) []byte {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return raw
}
func hasIssue(values []ValidationIssue, path, code string) bool {
	for _, value := range values {
		if value.Path == path && value.Code == code {
			return true
		}
	}
	return false
}

func TestDeleteDraftRemovesOnlyOwnDraftAndKeepsAudit(t *testing.T) {
	service, repo, _ := testService()
	draft, err := service.Create(context.Background(), actorA(), CreateCommand{IdempotencyKey: "create-delete-key"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.SaveSection(context.Background(), actorA(), draft.ID, SectionOrganization, SaveSectionCommand{Data: mustJSON(validSection(SectionOrganization)), IdempotencyKey: "delete-section-key"}); err != nil {
		t.Fatal(err)
	}
	repo.materials = append(repo.materials, Material{ID: 1, TenantID: "tenant-a", PublicID: "material-delete", FilingID: repo.filings[0].ID, MaterialCode: "NETWORK_TOPOLOGY", ObjectVersion: "version-1", FileName: "topology.pdf", MIMEType: "application/pdf", SizeBytes: 3, SHA256: strings.Repeat("a", 64), ScanStatus: MaterialPendingUpload, Version: 1})

	deleted, err := service.DeleteDraft(context.Background(), actorA(), draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	if deleted.ID != draft.ID || !repo.filings[0].DeletedAt.Valid {
		t.Fatalf("deleted=%#v filing=%#v", deleted, repo.filings[0])
	}
	if len(repo.sections) != 0 || len(repo.materials) != 0 {
		t.Fatalf("draft children not removed: sections=%d materials=%d", len(repo.sections), len(repo.materials))
	}
	found := false
	for _, action := range repo.actions {
		if action.Action == "DELETE" && action.ActorID == "sub-a" && action.FilingID == repo.filings[0].ID {
			found = true
		}
	}
	if !found {
		t.Fatal("delete audit action missing")
	}
	if _, err = service.Get(context.Background(), actorA(), draft.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get after delete err=%v", err)
	}
	if _, err = service.DeleteDraft(context.Background(), actorA(), draft.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete err=%v", err)
	}
}

func TestDeleteDraftRejectsLockedForeignAndInvalidActor(t *testing.T) {
	service, repo, _ := testService()
	draft, err := service.Create(context.Background(), actorA(), CreateCommand{IdempotencyKey: "create-locked-key"})
	if err != nil {
		t.Fatal(err)
	}
	repo.filings[0].Status = StatusWaitingContract
	if _, err = service.DeleteDraft(context.Background(), actorA(), draft.ID); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("locked delete err=%v", err)
	}
	repo.filings[0].Status = StatusDraft
	if _, err = service.DeleteDraft(context.Background(), Actor{TenantID: "tenant-b", CustomerID: 8, AccountID: "sub-b"}, draft.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign delete err=%v", err)
	}
	if _, err = service.DeleteDraft(context.Background(), Actor{TenantID: "", CustomerID: 0, AccountID: ""}, draft.ID); !errors.Is(err, ErrValidation) {
		t.Fatalf("invalid actor delete err=%v", err)
	}
}

func TestListIncludesUnitNameAndSystemName(t *testing.T) {
	service, _, _ := testService()
	view, err := service.Create(context.Background(), actorA(), CreateCommand{IdempotencyKey: "create-list-key"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.SaveSection(context.Background(), actorA(), view.ID, SectionOrganization, SaveSectionCommand{Data: mustJSON(validSection(SectionOrganization)), IdempotencyKey: "list-org-key"}); err != nil {
		t.Fatal(err)
	}
	if _, err = service.SaveSection(context.Background(), actorA(), view.ID, SectionClassifiedObject, SaveSectionCommand{Data: mustJSON(validSection(SectionClassifiedObject)), IdempotencyKey: "list-object-key"}); err != nil {
		t.Fatal(err)
	}
	result, err := service.List(context.Background(), actorA(), 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].UnitName != "华兴证券股份有限公司" || result.Items[0].SystemName != "核心系统" {
		t.Fatalf("list summary=%#v", result.Items)
	}
}
