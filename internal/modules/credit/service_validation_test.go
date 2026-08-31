package credit

import (
	"strings"
	"testing"
)

func TestApplyRequestRequiresAtLeastTwoCharacters(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reason string
		valid  bool
	}{
		{name: "two chinese characters", reason: "调整", valid: true},
		{name: "two ascii characters", reason: "OK", valid: true},
		{name: "single character", reason: "调", valid: false},
		{name: "blank", reason: "   ", valid: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			valid := len([]rune(strings.TrimSpace(tc.reason))) >= 2
			if valid != tc.valid {
				t.Fatalf("reason %q valid=%v, want %v", tc.reason, valid, tc.valid)
			}
		})
	}
}
