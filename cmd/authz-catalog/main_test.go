package main

import "testing"

func TestParseArguments(t *testing.T) {
	for _, test := range []struct {
		arguments           []string
		wantAction, wantApp string
	}{
		{[]string{"crm"}, "print", "crm"},
		{[]string{"print", "portal"}, "print", "portal"},
		{[]string{"publish", "crm"}, "publish", "crm"},
		{[]string{"publish", "unknown"}, "", ""},
	} {
		action, application := parseArguments(test.arguments)
		if action != test.wantAction || application != test.wantApp {
			t.Fatalf("parseArguments(%v) = (%q, %q), want (%q, %q)", test.arguments, action, application, test.wantAction, test.wantApp)
		}
	}
}
