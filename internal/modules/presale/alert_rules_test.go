package presale

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAlertRuleDefinitionsAreEditableWithoutEnablingPolicies(t *testing.T) {
	values := completeAlertRules(nil)
	if len(values) != 5 {
		t.Fatalf("expected five supported rule definitions, got %d", len(values))
	}
	for i, value := range values {
		if value.Type != alertTypes[i] || value.Enabled || value.Configured || value.ConfigVersion != 0 || value.Version != 1 || !value.UpdatedAt.IsZero() {
			t.Fatalf("unconfigured definition must not claim a saved policy: %+v", value)
		}
	}
}

func TestAlertRuleDefinitionsPreservePersistedValuesAndSeparateVersions(t *testing.T) {
	now := time.Now().UTC()
	rule := AlertRule{BaseModel: BaseModel{Version: 7, UpdatedBy: "admin", UpdatedAt: now}, Type: AlertAssignmentOverdue, ThresholdHours: 24, Enabled: true, ConfigVersion: 3}
	values := completeAlertRules([]AlertRule{rule})
	configured := 0
	for _, value := range values {
		if !value.Configured {
			continue
		}
		configured++
		if value.Type != rule.Type || value.ThresholdHours != 24 || !value.Enabled || value.Version != 7 || value.ConfigVersion != 3 || value.UpdatedBy != "admin" || !value.UpdatedAt.Equal(now) {
			t.Fatalf("persisted rule changed: %+v", value)
		}
	}
	if configured != 1 {
		t.Fatalf("expected only existing rule configured, got %d", configured)
	}
}

func TestAlertRuleReadAndWriteEnforceConfigurationPermission(t *testing.T) {
	s := NewAlertService(nil, nil)
	if _, err := s.ListRules(context.Background(), Actor{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("read permission not enforced: %v", err)
	}
	if _, err := s.UpdateRule(context.Background(), Actor{}, AlertAssignmentOverdue, UpdateAlertRuleInput{Version: 1}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("write permission not enforced: %v", err)
	}
	actor := Actor{Permissions: map[string]bool{"presale.alert.config": true}}
	for _, input := range []UpdateAlertRuleInput{{Version: 0}, {Version: 1, ThresholdHours: 8761}} {
		if _, err := s.UpdateRule(context.Background(), actor, AlertAssignmentOverdue, input); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("invalid input accepted: %+v, %v", input, err)
		}
	}
}
