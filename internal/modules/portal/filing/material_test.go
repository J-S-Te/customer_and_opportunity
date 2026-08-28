package filing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

const materialTestSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type materialStoreStub struct {
	keys     []string
	contents map[string][]byte
	metadata map[string]MaterialObjectMetadata
	putCalls int
}

func (*materialStoreStub) Available() bool { return true }
func (s *materialStoreStub) CreateUpload(_ context.Context, key, _ string, _ uint64, _ string, _ string) (string, time.Time, error) {
	s.keys = append(s.keys, key)
	return "https://objects.example.test/upload", time.Date(2026, 8, 1, 2, 2, 3, 0, time.UTC), nil
}
func (s *materialStoreStub) PutVerified(_ context.Context, key string, body io.Reader, size uint64, digest, media string) error {
	s.putCalls++
	content, err := io.ReadAll(body)
	actual := sha256.Sum256(content)
	if err != nil || uint64(len(content)) != size || !strings.EqualFold(hex.EncodeToString(actual[:]), digest) {
		return ErrMaterialContentInvalid
	}
	if s.contents == nil {
		s.contents = make(map[string][]byte)
		s.metadata = make(map[string]MaterialObjectMetadata)
	}
	if _, exists := s.contents[key]; exists {
		return ErrMaterialContentInvalid
	}
	s.contents[key] = append([]byte(nil), content...)
	s.metadata[key] = MaterialObjectMetadata{ObjectVersion: hex.EncodeToString(actual[:]), SizeBytes: size, MIMEType: media, SHA256: hex.EncodeToString(actual[:])}
	return nil
}
func (s *materialStoreStub) Finalize(_ context.Context, key string) (MaterialObjectMetadata, error) {
	return s.metadata[key], nil
}
func (s *materialStoreStub) OpenVerified(_ context.Context, key, version, digest string, size uint64) (io.ReadCloser, error) {
	metadata, ok := s.metadata[key]
	if !ok || metadata.ObjectVersion != version || metadata.SizeBytes != size || !strings.EqualFold(metadata.SHA256, digest) {
		return nil, ErrMaterialContentInvalid
	}
	return io.NopCloser(bytes.NewReader(s.contents[key])), nil
}

type materialScannerStub struct{}

func (materialScannerStub) Available() bool { return true }
func (materialScannerStub) Submit(context.Context, string, string, string, string, uint64, string) (string, error) {
	return "scan-reference", nil
}

type materialCreateRaceRepository struct {
	*memoryRepository
	createErr       error
	winnerTransform func(Material) Material
}

func (r *materialCreateRaceRepository) CreateMaterial(_ context.Context, value *Material) error {
	winner := *value
	if r.winnerTransform != nil {
		winner = r.winnerTransform(winner)
	}
	winner.ID = uint64(len(r.materials) + 1)
	r.materials = append(r.materials, winner)
	return r.createErr
}

func TestCreateUploadRecoversExactConcurrentIdempotencyWinner(t *testing.T) {
	repo := &materialCreateRaceRepository{
		memoryRepository: materialRepositoryFixture(),
		createErr:        &mysqlDriver.MySQLError{Number: 1062, Message: "Duplicate entry for key 'uq_portal_filing_material_create'"},
	}
	store := &materialStoreStub{}
	service := materialServiceFixture(repo, store)

	grant, err := service.CreateUpload(context.Background(), actorA(), "filing-public-1", materialUploadCommand("material-key-1"))
	if err != nil {
		t.Fatal(err)
	}
	if grant.Material.Code != "NETWORK_TOPOLOGY" || len(repo.materials) != 1 || len(store.keys) != 1 || store.keys[0] != "portal/filings/tenant-a/filing-public-1/0123456789abcdef0123456789abc1" {
		t.Fatalf("grant=%#v materials=%d upload keys=%v", grant, len(repo.materials), store.keys)
	}
}

func TestCreateUploadRejectsConcurrentIdempotencyKeyWithDifferentPayload(t *testing.T) {
	repo := &materialCreateRaceRepository{
		memoryRepository: materialRepositoryFixture(),
		createErr:        gorm.ErrDuplicatedKey,
		winnerTransform: func(winner Material) Material {
			winner.FileName = "other.pdf"
			winner.CreateRequestHash = materialDigest("different-payload")
			return winner
		},
	}
	store := &materialStoreStub{}
	service := materialServiceFixture(repo, store)

	_, err := service.CreateUpload(context.Background(), actorA(), "filing-public-1", materialUploadCommand("material-key-1"))
	if !errors.Is(err, ErrIdempotencyConflict) || len(store.keys) != 0 {
		t.Fatalf("err=%v upload keys=%v", err, store.keys)
	}
}

func TestCreateUploadMapsConcurrentMaterialCodeWinnerToConflict(t *testing.T) {
	repo := &materialCreateRaceRepository{
		memoryRepository: materialRepositoryFixture(),
		createErr:        &mysqlDriver.MySQLError{Number: 1062, Message: "Duplicate entry for key 'uq_portal_filing_material_code'"},
		winnerTransform: func(winner Material) Material {
			winner.CreateKeyHash = materialDigest("another-material-key")
			return winner
		},
	}
	store := &materialStoreStub{}
	service := materialServiceFixture(repo, store)

	_, err := service.CreateUpload(context.Background(), actorA(), "filing-public-1", materialUploadCommand("material-key-1"))
	if !errors.Is(err, ErrVersionConflict) || len(store.keys) != 0 {
		t.Fatalf("err=%v upload keys=%v", err, store.keys)
	}
}

func TestDuplicateMaterialCreateRecognition(t *testing.T) {
	for name, test := range map[string]struct {
		err       error
		duplicate bool
	}{
		"gorm translated": {err: gorm.ErrDuplicatedKey, duplicate: true},
		"mysql 1062":      {err: &mysqlDriver.MySQLError{Number: 1062}, duplicate: true},
		"mysql lock wait": {err: &mysqlDriver.MySQLError{Number: 1205}},
		"unrelated":       {err: errors.New("storage unavailable")},
	} {
		t.Run(name, func(t *testing.T) {
			if got := isDuplicateMaterialCreate(test.err); got != test.duplicate {
				t.Fatalf("isDuplicateMaterialCreate()=%v want %v", got, test.duplicate)
			}
		})
	}
}

func TestUploadContentValidatesOwnershipMetadataAndDuplicateWrite(t *testing.T) {
	content := []byte("%PDF-1.4\n%%EOF")
	digest := sha256.Sum256(content)
	command := MaterialUploadCommand{
		MaterialCode: "NETWORK_TOPOLOGY", FileName: "topology.pdf", MIMEType: "application/pdf",
		SizeBytes: uint64(len(content)), SHA256: hex.EncodeToString(digest[:]), IdempotencyKey: "material-content-1",
	}
	repository := materialRepositoryFixture()
	store := &materialStoreStub{}
	service := materialServiceFixture(repository, store)
	grant, err := service.CreateUpload(context.Background(), actorA(), "filing-public-1", command)
	if err != nil {
		t.Fatal(err)
	}
	if grant.UploadMode != "INTERNAL" || !strings.HasSuffix(grant.UploadURL, "/materials/"+grant.Material.ID+"/content") {
		t.Fatalf("unexpected controlled upload grant: %#v", grant)
	}

	uploaded, err := service.UploadContent(context.Background(), actorA(), "filing-public-1", grant.Material.ID, grant.Material.Version, bytes.NewReader(content), "application/pdf; charset=binary", int64(len(content)))
	if err != nil {
		t.Fatal(err)
	}
	if uploaded.UploadedAt == nil || uploaded.Version != grant.Material.Version+2 || store.putCalls != 1 {
		t.Fatalf("uploaded=%#v putCalls=%d", uploaded, store.putCalls)
	}
	if _, err = service.UploadContent(context.Background(), actorA(), "filing-public-1", grant.Material.ID, uploaded.Version, bytes.NewReader(content), "application/pdf", int64(len(content))); !errors.Is(err, ErrMaterialNotReady) || store.putCalls != 1 {
		t.Fatalf("duplicate content write err=%v putCalls=%d", err, store.putCalls)
	}
}

func TestUploadContentRejectsInvalidBoundaryBeforeStorage(t *testing.T) {
	content := []byte("%PDF-1.4\n%%EOF")
	digest := sha256.Sum256(content)
	command := MaterialUploadCommand{MaterialCode: "NETWORK_TOPOLOGY", FileName: "topology.pdf", MIMEType: "application/pdf", SizeBytes: uint64(len(content)), SHA256: hex.EncodeToString(digest[:]), IdempotencyKey: "material-content-2"}
	repository := materialRepositoryFixture()
	store := &materialStoreStub{}
	service := materialServiceFixture(repository, store)
	grant, err := service.CreateUpload(context.Background(), actorA(), "filing-public-1", command)
	if err != nil {
		t.Fatal(err)
	}
	for name, test := range map[string]struct {
		actor       Actor
		contentType string
		length      int64
		content     []byte
		want        error
	}{
		"cross tenant": {actor: Actor{TenantID: "tenant-b", CustomerID: 7, AccountID: "sub-a"}, contentType: "application/pdf", length: int64(len(content)), content: content, want: ErrNotFound},
		"wrong size":   {actor: actorA(), contentType: "application/pdf", length: int64(len(content) - 1), content: content, want: ErrMaterialContentInvalid},
		"wrong header": {actor: actorA(), contentType: "image/png", length: int64(len(content)), content: content, want: ErrMaterialContentInvalid},
		"spoofed mime": {actor: actorA(), contentType: "application/pdf", length: int64(len(content)), content: []byte("plain text body"), want: ErrMaterialContentInvalid},
	} {
		t.Run(name, func(t *testing.T) {
			_, uploadErr := service.UploadContent(context.Background(), test.actor, "filing-public-1", grant.Material.ID, grant.Material.Version, bytes.NewReader(test.content), test.contentType, test.length)
			if !errors.Is(uploadErr, test.want) {
				t.Fatalf("UploadContent() error=%v want=%v", uploadErr, test.want)
			}
		})
	}
	if store.putCalls != 0 {
		t.Fatalf("invalid boundary reached storage %d times", store.putCalls)
	}
}

func TestMaterialIndexesMatchMigration(t *testing.T) {
	parsed, err := schema.Parse(&Material{}, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatal(err)
	}
	indexes := parsed.ParseIndexes()
	for name, expected := range map[string][]string{
		"uq_portal_filing_material_public": {"tenant_id", "public_id"},
		"uq_portal_filing_material_code":   {"tenant_id", "filing_id", "material_code"},
		"uq_portal_filing_material_create": {"tenant_id", "create_actor_id", "create_key_hash"},
	} {
		index, ok := indexes[name]
		if !ok || index.Class != "UNIQUE" || len(index.Fields) != len(expected) {
			t.Fatalf("index %s=%#v", name, index)
		}
		for position, column := range expected {
			if index.Fields[position].DBName != column {
				t.Fatalf("index %s column %d=%q want %q", name, position, index.Fields[position].DBName, column)
			}
		}
	}
}

func materialRepositoryFixture() *memoryRepository {
	return &memoryRepository{filings: []Filing{{
		ID: 1, TenantID: "tenant-a", CustomerID: 7, AccountID: "sub-a", PublicID: "filing-public-1", Status: StatusDraft, Version: 1,
	}}}
}

func materialServiceFixture(repo Repository, store *materialStoreStub) *MaterialService {
	return NewMaterialService(repo, &spyProtector{}, store, materialScannerStub{}, fixedClock{now: time.Date(2026, 8, 1, 1, 2, 3, 0, time.UTC)}, &sequenceIDs{})
}

func materialUploadCommand(key string) MaterialUploadCommand {
	return MaterialUploadCommand{MaterialCode: "NETWORK_TOPOLOGY", FileName: "topology.pdf", MIMEType: "application/pdf", SizeBytes: 4, SHA256: materialTestSHA256, IdempotencyKey: key}
}
