package notification

import (
	"reflect"
	"strings"
	"testing"
)

func TestNotificationSourceEventIDCapacityMatchesRecipientScopedKey(t *testing.T) {
	field, ok := reflect.TypeOf(Notification{}).FieldByName("SourceEventID")
	if !ok {
		t.Fatal("Notification.SourceEventID is missing")
	}
	if tag := field.Tag.Get("gorm"); !strings.Contains(tag, "size:256") {
		t.Fatalf("Notification.SourceEventID gorm tag = %q, want size:256", tag)
	}
}
