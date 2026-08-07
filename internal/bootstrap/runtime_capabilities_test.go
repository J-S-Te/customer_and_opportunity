package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type runtimeReadinessStub struct {
	ready     bool
	err       error
	worker    string
	notBefore time.Time
}

func (s *runtimeReadinessStub) HasFreshHeartbeat(_ context.Context, worker string, notBefore time.Time) (bool, error) {
	s.worker, s.notBefore = worker, notBefore
	return s.ready, s.err
}

func TestRuntimeCapabilitiesFailClosedWithoutOptionalAdapters(t *testing.T) {
	values := runtimeCapabilities(Config{})
	for _, key := range []string{
		"owner_directory", "portal_account_provision", "portal_access_disable", "approval_task_query",
		"qb_launch_quotation", "qb_launch_bid", "customer_import_scan",
		"opportunity_attachment_upload", "opportunity_attachment_download",
	} {
		value, ok := values[key]
		if !ok || value.Available || value.ReasonCode == "" {
			t.Fatalf("capability %s must fail closed: %#v", key, value)
		}
	}
	if value := values["presale_request_submission"]; !value.Available || value.Mode != capabilityModeReady {
		t.Fatalf("internal presale submission capability=%#v", value)
	}
	if value := values["qb_active_query"]; value.Available || value.Mode != capabilityModeCallbackOnly {
		t.Fatalf("QB query fallback=%#v", value)
	}
}

func TestRuntimeCapabilitiesDoNotDependOnExternalWorkerEvidence(t *testing.T) {
	now := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name      string
		readiness *runtimeReadinessStub
	}{
		{name: "fresh evidence", readiness: &runtimeReadinessStub{ready: true}},
		{name: "no evidence", readiness: &runtimeReadinessStub{}},
		{name: "query failure", readiness: &runtimeReadinessStub{err: errors.New("database details")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			values := resolveRuntimeCapabilities(context.Background(), Config{}, test.readiness, 15*time.Second, now)
			value := values["presale_request_submission"]
			if !value.Available || value.Mode != capabilityModeReady || value.ReasonCode != "" {
				t.Fatalf("capability=%#v", value)
			}
			if test.readiness.worker != "" || !test.readiness.notBefore.IsZero() {
				t.Fatalf("internal approval unexpectedly queried worker=%q", test.readiness.worker)
			}
		})
	}
}

func TestRuntimeCapabilitiesExposeFlagsWithoutConfigurationSecrets(t *testing.T) {
	config := Config{
		OwnerDirectoryEnabled: true,
		PortalInviteEnabled:   true, PlatformExternalIdentityEnabled: true,
		ApprovalTaskResolverEnabled: true, QBStatusQueryEnabled: true, QBLaunchEnabled: true,
		PortalProjectHistoryEnabled: true,
		OIDCClientSecret:            "never-expose-oidc", PlatformExternalUserClientSecret: "never-expose-platform",
		QBStatusURL: "https://private-qb.example/internal", ApprovalTaskClientID: "private-approval-client",
	}
	values := runtimeCapabilities(config)
	for _, key := range []string{"owner_directory", "portal_account_provision", "portal_access_disable", "approval_task_query", "qb_active_query", "qb_launch_quotation", "qb_launch_bid", "customer_project_history"} {
		if !values[key].Available || values[key].Mode != capabilityModeReady || values[key].ReasonCode != "" {
			t.Fatalf("capability %s=%#v", key, values[key])
		}
	}
	raw, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"never-expose", "private-qb.example", "private-approval-client", "client_secret", "token_url", "scope"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("runtime capability response leaked %q: %s", forbidden, raw)
		}
	}
}
