package portalprojectworker

import (
	"testing"
	"time"
)

func TestConfigRejectsScopeAndUnsafeEndpointContracts(t *testing.T) {
	base := Config{MySQLDSN: "dsn", TenantID: "tenant", WorkerID: "worker", PollInterval: time.Second, SyncInterval: 5 * time.Minute, LeaseDuration: time.Minute, RetryInterval: time.Minute, PageSize: 100, TokenURL: "https://identity.example/oauth2/token", ClientID: "id", ClientSecret: "secret", Scope: requiredProjectScope, SnapshotsURL: "https://project.example/api/v1/projects/snapshots", TokenTimeout: 5 * time.Second, RequestTimeout: 10 * time.Second}
	for name, mutate := range map[string]func(*Config){
		"scope":    func(value *Config) { value.Scope = "report.request.write" },
		"userinfo": func(value *Config) { value.SnapshotsURL = "https://user:secret@project.example/snapshots" },
		"lease":    func(value *Config) { value.LeaseDuration = time.Second },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := base
			mutate(&cfg)
			if err := cfg.validate(); err == nil {
				t.Fatal("expected configuration rejection")
			}
		})
	}
}

func TestConfigRejectsIncompleteOrClearTextMTLS(t *testing.T) {
	base := Config{MySQLDSN: "dsn", TenantID: "tenant", WorkerID: "worker", PollInterval: time.Second, SyncInterval: 5 * time.Minute, LeaseDuration: time.Minute, RetryInterval: time.Minute, PageSize: 100, TokenURL: "https://identity.example/oauth2/token", ClientID: "id", ClientSecret: "secret", Scope: requiredProjectScope, SnapshotsURL: "https://project.example/api/v1/projects/snapshots", TokenTimeout: 5 * time.Second, RequestTimeout: 10 * time.Second}
	base.TLS.ClientCertFile = "/run/secrets/client.pem"
	base.TLS.RequireMTLS = true
	if err := base.validate(); err == nil {
		t.Fatal("partial mTLS identity was accepted")
	}
	base.TLS.ClientKeyFile = "/run/secrets/client-key.pem"
	base.SnapshotsURL = "http://project.example/api/v1/projects/snapshots"
	if err := base.validate(); err == nil {
		t.Fatal("clear-text project endpoint was accepted with mTLS")
	}
}
