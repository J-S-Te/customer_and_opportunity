package notificationdeliveryworker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/notification"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
)

func TestPriorityFor(t *testing.T) {
	if got := priorityFor(notification.TypePresaleApprovalPending); got != "HIGH" {
		t.Fatalf("pending priority=%q, want HIGH", got)
	}
	if got := priorityFor(notification.TypeOpportunityOwnerChanged); got != "NORMAL" {
		t.Fatalf("owner priority=%q, want NORMAL", got)
	}
}

func TestDeliverPostsIngestionEvent(t *testing.T) {
	var received ingestionEventPayload
	var authHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth2/token":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"tok","token_type":"bearer","scope":"notification.ingest"}`))
		case "/api/v1/notifications/events":
			authHeader = r.Header.Get("Authorization")
			body, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(body, &received); err != nil {
				t.Fatalf("bad payload: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"status":"ACCEPTED"}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	app := &App{
		config: Config{
			PlatformBaseURL:  server.URL,
			PlatformTokenURL: server.URL + "/oauth2/token",
			ApplicationCode:  "customer_and_opportunity",
			EnvironmentCode:  "dev",
			ClientID:         "client", ClientSecret: "secret",
		},
		client: &http.Client{Timeout: 5 * time.Second},
	}

	item := notification.Notification{Model: database.Model{CreatedAt: time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)}, SourceEventID: "evt-1", Type: notification.TypePresaleAssigneeAdded, Title: "售前已指派", Body: "您已被指派", RecipientID: "u-1", OpportunityID: 42, TargetPath: "/customer-opportunity/opportunities?opportunity_id=42"}
	if err := app.deliver(context.Background(), item); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if authHeader != "Bearer tok" {
		t.Fatalf("auth header=%q", authHeader)
	}
	if received.EventType != notification.TypePresaleAssigneeAdded || received.NotificationScope != "CROSS_SYSTEM" ||
		received.EventID != "evt-1" || received.IdempotencyKey != "evt-1" ||
		len(received.Recipients) != 1 || received.Recipients[0] != "u-1" ||
		received.TargetURL != "/customer-opportunity/opportunities?opportunity_id=42" ||
		received.ReferenceType != "OPPORTUNITY" || received.ReferenceID != "42" {
		t.Fatalf("unexpected payload: %+v", received)
	}
}

func TestDeliverRejectsSuccessfulHTTPWithoutAcceptedReceipt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth2/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"tok","token_type":"bearer"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"status":"DEAD"}}`))
	}))
	defer server.Close()

	app := &App{config: Config{PlatformBaseURL: server.URL, PlatformTokenURL: server.URL + "/oauth2/token", ClientID: "client", ClientSecret: "secret"}, client: server.Client()}
	item := notification.Notification{Model: database.Model{CreatedAt: time.Now().UTC()}, SourceEventID: "evt-1", Type: notification.TypePresaleAssigneeAdded, Title: "title", Body: "body", RecipientID: "u-1", OpportunityID: 1}
	if err := app.deliver(context.Background(), item); err == nil || !strings.Contains(err.Error(), "not accepted") {
		t.Fatalf("deliver error=%v, want non-accepted receipt error", err)
	}
}

func TestPlatformEventCodeNormalizesNumericAndOversizedSourceIDs(t *testing.T) {
	numeric := platformEventCode("customer_and_opportunity", "dev", "73e7c3cd10ec711ff714ccf6eb1d21ae981e6eb34bc846270880c2a70d57efe0")
	if !platformEventCodePattern.MatchString(numeric) || len(numeric) != 68 {
		t.Fatalf("numeric source normalized to invalid code %q", numeric)
	}
	if got := platformEventCode("customer_and_opportunity", "dev", "evt-1"); got != "evt-1" {
		t.Fatalf("valid source changed to %q", got)
	}
	oversized := platformEventCode("customer_and_opportunity", "dev", strings.Repeat("A", 120))
	if !platformEventCodePattern.MatchString(oversized) || len(oversized) != 68 {
		t.Fatalf("oversized source normalized to invalid code %q", oversized)
	}
}

func TestReferenceForPresaleRequestTakesPrecedence(t *testing.T) {
	typeName, id, err := referenceFor(notification.Notification{RequestID: 17, OpportunityID: 42})
	if err != nil {
		t.Fatalf("referenceFor: %v", err)
	}
	if typeName != "PRESALE_REQUEST" || id != "17" {
		t.Fatalf("reference=%s/%s, want PRESALE_REQUEST/17", typeName, id)
	}
}

func TestReferenceForRejectsMissingReference(t *testing.T) {
	if _, _, err := referenceFor(notification.Notification{}); err == nil {
		t.Fatal("expected missing reference error")
	}
}

func TestPermanentDeliveryErrorOnlyClassifiesUnprocessableEntity(t *testing.T) {
	if !isPermanentDeliveryError(&platformAPIError{statusCode: http.StatusUnprocessableEntity}) {
		t.Fatal("HTTP 422 must be treated as a permanent payload or recipient validation failure")
	}
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests, http.StatusInternalServerError} {
		if isPermanentDeliveryError(&platformAPIError{statusCode: status}) {
			t.Fatalf("HTTP %d must remain retryable", status)
		}
	}
}
