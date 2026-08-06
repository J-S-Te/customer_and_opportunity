package capability

import (
	"reflect"
	"testing"
)

func TestDefaultOptionsAllEnabled(t *testing.T) {
	options := DefaultOptions()
	for _, key := range AllKeys {
		if !options.ToMap()[key] {
			t.Fatalf("default option %s must be enabled", key)
		}
	}
}

func TestOptionsFromMapKeepsDefaultsForMissingKeys(t *testing.T) {
	options := OptionsFromMap(map[string]bool{ReportEnabled: false})
	if !options.ProjectEnabled || options.ReportEnabled || !options.FilingEnabled {
		t.Fatalf("OptionsFromMap()=%+v", options)
	}
	options = OptionsFromMap(nil)
	if !reflect.DeepEqual(options, DefaultOptions()) {
		t.Fatalf("OptionsFromMap(nil)=%+v", options)
	}
}

func TestIntersectPermissionsAppliesCustomerServiceOptions(t *testing.T) {
	permissions := []string{
		"project.read", "report.read", "report.download",
		"filing.create", "feedback.create", "evaluation.create", "account.security.manage",
	}
	options := DefaultOptions()
	options.ReportEnabled = false
	options.FeedbackEnabled = false
	got := IntersectPermissions(permissions, options)
	want := []string{"project.read", "filing.create", "evaluation.create", "account.security.manage"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("IntersectPermissions()=%v want %v", got, want)
	}
}
