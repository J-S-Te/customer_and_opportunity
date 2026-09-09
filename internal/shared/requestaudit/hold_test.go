package requestaudit

import (
	"context"
	"errors"
	"net/http"
	"os"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type holdTestStore struct {
	values                   []Record
	held, delivered, retried []Record
	holdErr, retryErr        error
}

func (s *holdTestStore) RecoverInterrupted(context.Context, time.Time, time.Time) error { return nil }
func (s *holdTestStore) Claim(context.Context, string, int, time.Time, time.Time) ([]Record, error) {
	return s.values, nil
}
func (s *holdTestStore) HoldSourceMismatch(_ context.Context, _ string, rows []Record, _ time.Time) error {
	s.held = rows
	return s.holdErr
}
func (s *holdTestStore) Retry(_ context.Context, _ string, rows []Record, _ string, _, _ time.Time) error {
	s.retried = rows
	return s.retryErr
}
func (s *holdTestStore) Delivered(_ context.Context, _ string, rows []Record, _ time.Time) error {
	s.delivered = rows
	return nil
}

func TestRunOnceHoldsWrongSourceAndDeliversMatchingRows(t *testing.T) {
	s := &holdTestStore{values: []Record{
		{ID: 1, EventID: "bad", ApplicationCode: "crm", EnvironmentCode: "dev"},
		{ID: 2, EventID: "good", ApplicationCode: "crm", EnvironmentCode: "prod"},
	}}
	d := &Dispatcher{store: s, application: "crm", environment: "prod", workerID: "worker", batchSize: 100,
		baseURL: "https://platform.example", token: "test", tokenExpiresAt: time.Now().Add(time.Hour),
		client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusAccepted, `{"code":"OK","data":[{"event_id":"good","status":"ACCEPTED"}]}`), nil
		})},
	}
	if err := d.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(s.held) != 1 || s.held[0].EventID != "bad" || len(s.delivered) != 1 || s.delivered[0].EventID != "good" || len(s.retried) != 0 {
		t.Fatalf("unexpected partition: held=%v delivered=%v retried=%v", s.held, s.delivered, s.retried)
	}
}

func TestRunOnceHoldFailureIsReported(t *testing.T) {
	want := errors.New("database unavailable")
	s := &holdTestStore{values: []Record{{ApplicationCode: "crm", EnvironmentCode: "dev"}}, holdErr: want}
	d := &Dispatcher{store: s, application: "crm", environment: "prod"}
	if err := d.runOnce(context.Background()); !errors.Is(err, want) {
		t.Fatalf("error=%v", err)
	}
	if len(s.retried) != 0 || len(s.delivered) != 0 {
		t.Fatal("failed hold must not be acknowledged")
	}
}

func TestRunOnceAllMismatchedDoesNotUseNetwork(t *testing.T) {
	s := &holdTestStore{values: []Record{{ApplicationCode: "other", EnvironmentCode: "prod"}}}
	d := &Dispatcher{store: s, application: "crm", environment: "prod"}
	if err := d.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(s.held) != 1 {
		t.Fatal("mismatch not held")
	}
}

func TestRunOnceReportsRetryPersistenceFailure(t *testing.T) {
	want := errors.New("retry persistence failed")
	s := &holdTestStore{values: []Record{{ApplicationCode: "crm", EnvironmentCode: "prod"}}, retryErr: want}
	d := &Dispatcher{store: s, application: "crm", environment: "prod", baseURL: "https://platform.example",
		token: "test", tokenExpiresAt: time.Now().Add(time.Hour), client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusServiceUnavailable, `{}`), nil
		})}}
	if err := d.runOnce(context.Background()); !errors.Is(err, want) {
		t.Fatalf("error=%v", err)
	}
}

// This test requires a disposable MySQL database, never a business database.
func TestMySQLHeldRowsRemainPreservedAndUnclaimed(t *testing.T) {
	dsn := os.Getenv("REQUESTAUDIT_TEST_DSN")
	if dsn == "" {
		t.Skip("REQUESTAUDIT_TEST_DSN must point to a disposable MySQL database")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal("connect to test database failed")
	}
	var dbName string
	if err := db.Raw("SELECT DATABASE()").Scan(&dbName).Error; err != nil || dbName != "requestaudit_test" {
		t.Fatal("requires requestaudit_test database")
	}
	table := "requestaudit_hold_test"
	if err := db.Table(table).AutoMigrate(&Record{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Migrator().DropTable(table); err != nil {
			t.Error(err)
		}
	})
	s := &Store{db: db, tableName: table}
	now := time.Now().UTC().Truncate(time.Second)
	deadline := now.Add(time.Minute)
	row := Record{EventID: "preserved", TenantID: "tenant", ApplicationCode: "crm", EnvironmentCode: "dev", DeliveryStatus: StatusProcessing,
		LockedBy: "owner", LockedUntil: &deadline, NextAttemptAt: &now, Attempts: 1100, OccurredAt: now, CreatedAt: now, UpdatedAt: now}
	if err := db.Table(table).Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.HoldSourceMismatch(ctx, "stale-worker", []Record{row}, now); err != nil {
		t.Fatal(err)
	}
	var stored Record
	if err := db.Table(table).First(&stored, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.DeliveryStatus != StatusProcessing {
		t.Fatal("stale worker changed row")
	}
	if err := s.HoldSourceMismatch(ctx, "owner", []Record{row}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.RecoverInterrupted(ctx, now.Add(time.Hour), now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	rows, err := s.Claim(ctx, "next", 100, now.Add(3*time.Hour), now.Add(4*time.Hour))
	if err != nil || len(rows) != 0 {
		t.Fatalf("held row was reclaimed: %v %v", rows, err)
	}
	stored = Record{}
	if err := db.Table(table).First(&stored, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.EventID != row.EventID || stored.EnvironmentCode != "dev" || stored.Attempts != 1100 || stored.NextAttemptAt != nil || stored.LockedBy != "" || stored.LockedUntil != nil || stored.DeliveryStatus != StatusRetry {
		t.Fatalf("hold changed evidence or retained lease: %+v", stored)
	}
	status, err := s.Status(ctx, "tenant")
	if err != nil || status.HeldCount != 1 || status.RetryCount != 1 {
		t.Fatalf("status=%+v error=%v", status, err)
	}
	other, err := s.Status(ctx, "other-tenant")
	if err != nil || other.HeldCount != 0 {
		t.Fatalf("cross tenant status=%+v error=%v", other, err)
	}
	good := Record{EventID: "valid", TenantID: "tenant", ApplicationCode: "crm", EnvironmentCode: "prod",
		DeliveryStatus: StatusPending, NextAttemptAt: &now, OccurredAt: now, CreatedAt: now, UpdatedAt: now}
	if err := db.Table(table).Create(&good).Error; err != nil {
		t.Fatal(err)
	}
	rows, err = s.Claim(ctx, "next", 100, now.Add(3*time.Hour), now.Add(4*time.Hour))
	if err != nil || len(rows) != 1 || rows[0].EventID != "valid" {
		t.Fatalf("normal row not claimed: %v %v", rows, err)
	}
	if err := s.Delivered(ctx, "next", rows, now.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	status, err = s.Status(ctx, "tenant")
	if err != nil || status.DeliveredCount != 1 || status.HeldCount != 1 {
		t.Fatalf("normal delivery affected held row: %+v %v", status, err)
	}
}

func TestCRMAndPortalHoldUseSeparateTables(t *testing.T) {
	if NewStore(nil).tableName != "application_request_audit_outbox" || NewPortalStore(nil).tableName != "portal_application_request_audit_outbox" {
		t.Fatal("CRM and Portal must not share an outbox table")
	}
}
