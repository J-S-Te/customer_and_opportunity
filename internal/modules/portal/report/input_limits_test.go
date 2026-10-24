package report

import (
	"strings"
	"testing"
)

func TestReceiveEmailRejectsOverlongAddress(t *testing.T) {
	if validEmail("a@example.com") != true {
		t.Fatal("valid email rejected")
	}
	if validEmail(strings.Repeat("a", 310) + "@example.com") {
		t.Fatal("overlong email accepted")
	}
}
