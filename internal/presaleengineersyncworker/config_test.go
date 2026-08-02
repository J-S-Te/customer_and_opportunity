package presaleengineersyncworker

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestLoadConfigRejectsPartialPMSMTLS(t *testing.T) {
	t.Setenv("MYSQL_DSN", "user:pass@tcp(mysql:3306)/crm")
	t.Setenv("PMS_ENGINEER_TOKEN_URL", "https://identity.example/oauth2/token")
	t.Setenv("PMS_ENGINEER_CLIENT_ID", "pms-reader")
	t.Setenv("PMS_ENGINEER_CLIENT_SECRET", "secret")
	t.Setenv("PMS_ENGINEER_URL", "https://pms.example/technicians")
	t.Setenv("PMS_ENGINEER_SYNC_ENCRYPTION_KEY_BASE64", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))))
	t.Setenv("PMS_ENGINEER_TLS_CLIENT_CERT_FILE", "/run/secrets/client.pem")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("partial PMS mTLS identity was accepted")
	}
}

func TestLoadConfigRejectsClearTextPMSEndpoint(t *testing.T) {
	t.Setenv("MYSQL_DSN", "dsn")
	t.Setenv("PMS_ENGINEER_TOKEN_URL", "https://identity.example/oauth2/token")
	t.Setenv("PMS_ENGINEER_CLIENT_ID", "pms-reader")
	t.Setenv("PMS_ENGINEER_CLIENT_SECRET", "secret")
	t.Setenv("PMS_ENGINEER_URL", "http://pms.example/technicians")
	t.Setenv("PMS_ENGINEER_SYNC_ENCRYPTION_KEY_BASE64", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))))
	if _, err := LoadConfig(); err == nil {
		t.Fatal("clear-text PMS endpoint was accepted")
	}
}
