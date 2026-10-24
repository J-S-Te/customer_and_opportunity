package portalreportworker

import "testing"

func TestLoadConfigRejectsPartialProjectMTLS(t *testing.T) {
	t.Setenv("PORTAL_REPORT_WORKER_INGEST_ONLY", "false")
	t.Setenv("PORTAL_OIDC_TENANT_ID", "tenant-fixture")
	t.Setenv("PORTAL_REPORT_STORE_ROOT", t.TempDir())
	t.Setenv("PORTAL_REPORT_CONTENT_KEY_BASE64", "eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHg=")
	t.Setenv("PORTAL_REPORT_SOURCE_BASE_URL", "https://gateway.example")
	t.Setenv("PORTAL_REPORT_SOURCE_TOKEN_URL", "https://identity.example/oauth2/token")
	t.Setenv("PORTAL_REPORT_SOURCE_CLIENT_ID", "portal-report-source")
	t.Setenv("PORTAL_REPORT_SOURCE_CLIENT_SECRET", "fixture")
	t.Setenv("PORTAL_REPORT_INGEST_DESCRIPTOR_KEY_BASE64", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	t.Setenv("PORTAL_REPORT_WORKER_MYSQL_DSN", "user:pass@tcp(mysql:3306)/customer_portal")
	t.Setenv("PORTAL_REPORT_PROJECT_TOKEN_URL", "https://identity.example/oauth2/token")
	t.Setenv("PORTAL_REPORT_PROJECT_CLIENT_ID", "report-writer")
	t.Setenv("PORTAL_REPORT_PROJECT_CLIENT_SECRET", "secret")
	t.Setenv("PORTAL_REPORT_PROJECT_REQUEST_URL", "https://project.example/report-requests")
	t.Setenv("PORTAL_REPORT_PROJECT_TLS_CLIENT_CERT_FILE", "/run/secrets/client.pem")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("partial project-service mTLS identity was accepted")
	}
}

func TestIngestOnlyModeUsesDedicatedSourceWithoutAdvertisingProjectRequestDelivery(t *testing.T) {
	t.Setenv("PORTAL_REPORT_WORKER_INGEST_ONLY", "true")
	t.Setenv("PORTAL_REPORT_INGEST_DESCRIPTOR_KEY_BASE64", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	t.Setenv("PORTAL_REPORT_CONTENT_KEY_BASE64", "eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHg=")
	t.Setenv("PORTAL_REPORT_WORKER_MYSQL_DSN", "fixture@tcp(mysql:3306)/customer_portal")
	t.Setenv("PORTAL_REPORT_STORE_ROOT", t.TempDir())
	t.Setenv("PORTAL_OIDC_TENANT_ID", "tenant-fixture")
	t.Setenv("PORTAL_REPORT_SOURCE_BASE_URL", "https://gateway.example")
	t.Setenv("PORTAL_REPORT_SOURCE_TOKEN_URL", "https://platform.example/oauth2/token")
	t.Setenv("PORTAL_REPORT_SOURCE_CLIENT_ID", "portal-report-source")
	t.Setenv("PORTAL_REPORT_SOURCE_CLIENT_SECRET", "fixture")
	t.Setenv("PORTAL_REPORT_PROJECT_TLS_REQUIRE_MTLS", "unused-in-ingest-only-mode")
	cfg, err := LoadConfig()
	if err != nil || !cfg.IngestOnly {
		t.Fatal("valid ingest-only configuration failed", err)
	}
	t.Setenv("PORTAL_REPORT_WORKER_INGEST_ONLY", "false")
	t.Setenv("PORTAL_REPORT_PROJECT_TLS_REQUIRE_MTLS", "false")
	t.Setenv("PORTAL_REPORT_PROJECT_CLIENT_ID", "")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("request delivery enabled without a real Project adapter")
	}
}

func TestIngestOnlyRejectsUnsupportedSourceScheme(t *testing.T) {
	t.Setenv("PORTAL_REPORT_WORKER_INGEST_ONLY", "true")
	t.Setenv("PORTAL_REPORT_INGEST_DESCRIPTOR_KEY_BASE64", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	t.Setenv("PORTAL_REPORT_CONTENT_KEY_BASE64", "eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHg=")
	t.Setenv("PORTAL_REPORT_WORKER_MYSQL_DSN", "fixture@tcp(mysql:3306)/customer_portal")
	t.Setenv("PORTAL_REPORT_STORE_ROOT", t.TempDir())
	t.Setenv("PORTAL_OIDC_TENANT_ID", "tenant-fixture")
	t.Setenv("PORTAL_REPORT_SOURCE_BASE_URL", "ftp://gateway.example")
	t.Setenv("PORTAL_REPORT_SOURCE_TOKEN_URL", "https://platform.example/oauth2/token")
	t.Setenv("PORTAL_REPORT_SOURCE_CLIENT_ID", "portal-report-source")
	t.Setenv("PORTAL_REPORT_SOURCE_CLIENT_SECRET", "fixture")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("unsupported source URL scheme accepted")
	}
}
