package filing

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

const materialTestSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type materialStoreStub struct {
	keys []string
}

func (*materialStoreStub) Available() bool { return true }
func (s *materialStoreStub) CreateUpload(_ context.Context, key, _ string, _ uint64, _ string, _ string) (string, time.Time, error) {
	s.keys = append(s.keys, key)
	return "https://objects.example.test/upload", time.Date(2026, 8, 1, 2, 2, 3, 0, time.UTC), nil
}
func (*materialStoreStub) Finalize(context.Context, string) (MaterialObjectMetadata, error) {
	return MaterialObjectMetadata{}, nil
}
func (*materialStoreStub) OpenVerified(context.Context, string, string, string, uint64) (io.ReadCloser, error) {
	return nil, nil
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
