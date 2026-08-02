package contracttransferworker

import "testing"

func TestLoadConfigRequiresDedicatedExactScope(t *testing.T) {
	t.Setenv("MYSQL_DSN", "dsn")
	t.Setenv("CONTRACT_CLIENT_ID", "client")
	t.Setenv("CONTRACT_CLIENT_SECRET", "secret")
	t.Setenv("CONTRACT_TOKEN_URL", "https://identity.example.com/oauth2/token")
	t.Setenv("CONTRACT_OPPORTUNITY_INTAKE_URL", "https://contract.example.com/internal/opportunity-signed-events")
	t.Setenv("CONTRACT_SCOPE", "contract.create")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("non-integration scope was accepted")
	}
	t.Setenv("CONTRACT_SCOPE", "opportunity.signed.write")
	if _, err := LoadConfig(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

func TestLoadConfigRequiresCompleteMTLSAndHTTPS(t *testing.T) {
	t.Setenv("MYSQL_DSN", "dsn")
	t.Setenv("CONTRACT_CLIENT_ID", "client")
	t.Setenv("CONTRACT_CLIENT_SECRET", "secret")
	t.Setenv("CONTRACT_TOKEN_URL", "https://identity.example.com/oauth2/token")
	t.Setenv("CONTRACT_OPPORTUNITY_INTAKE_URL", "https://contract.example.com/internal/opportunity-signed-events")
	t.Setenv("CONTRACT_TLS_REQUIRE_MTLS", "true")
	t.Setenv("CONTRACT_TLS_CLIENT_CERT_FILE", "/run/secrets/client.pem")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("partial mTLS identity was accepted")
	}
	t.Setenv("CONTRACT_TLS_CLIENT_KEY_FILE", "/run/secrets/client-key.pem")
	t.Setenv("CONTRACT_OPPORTUNITY_INTAKE_URL", "http://contract.example.com/internal/opportunity-signed-events")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("clear-text intake was accepted with mTLS enabled")
	}
}

func TestStableEventIDMatchesCRMProducerContract(t *testing.T) {
	if got := stableEventID("tenant-a", 7, 9); got != "opp-signed-d8f26ae604169f5f93c89a9ec96b56a9" {
		t.Fatalf("stableEventID() = %q", got)
	}
}

func TestNormalizeAmountReturnsTwoDecimals(t *testing.T) {
	for input, want := range map[string]string{"7": "7.00", "7.1": "7.10", "7.12": "7.12"} {
		if got := normalizeAmount(input); got != want {
			t.Fatalf("normalizeAmount(%q)=%q want %q", input, got, want)
		}
	}
}
