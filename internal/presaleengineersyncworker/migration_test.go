package presaleengineersyncworker

import (
	"os"
	"strings"
	"testing"
)

func TestMigrationContainsDurableIdempotencyLeaseAndNoPlainContact(t *testing.T) {
	data, err := os.ReadFile("../../migrations/000030_presale_engineer_sync.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(data)
	for _, required := range []string{"crm_presale_engineer_sync_requests", "uq_presale_engineer_sync_request", "locked_until", "idx_presale_engineer_sync_claim", "next_sync_at"} {
		if !strings.Contains(sql, required) {
			t.Fatalf("missing %s", required)
		}
	}
	if strings.Contains(sql, "contact VARCHAR") {
		t.Fatal("sync migration must not add plaintext contact")
	}
}

func TestScheduleDiscoversTenantFromCRMCustomersBeforeFirstPresaleRequest(t *testing.T) {
	data, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(data)
	if !strings.Contains(sql, "FROM crm_customers WHERE deleted_at IS NULL") {
		t.Fatal("customer tenant discovery is missing")
	}
	if strings.Contains(sql, "customer_portal") {
		t.Fatal("worker must not discover tenants from the Portal schema")
	}
}
