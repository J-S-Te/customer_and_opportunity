package portalreportworker

import "testing"

func TestLoadConfigRejectsPartialProjectMTLS(t *testing.T) {
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
