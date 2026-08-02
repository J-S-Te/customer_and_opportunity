package contracttransferworker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/opportunity"
)

type transferStoreStub struct {
	events              []opportunity.OutboxEvent
	trace               []string
	claimTokens         []string
	authorityErr        error
	permanentReason     string
	retryAt             time.Time
	retryToken          string
	retrySummary        string
	finishedStatus      string
	finishedToken       string
	finishedAt          time.Time
	finishedIntakeID    string
	finishedSummary     string
	activeClaim         bool
	claimedBeforeFinish bool
}

func (s *transferStoreStub) claimOne(_ context.Context, _ time.Time, _ time.Duration, token string) (opportunity.OutboxEvent, bool, error) {
	if s.activeClaim {
		s.claimedBeforeFinish = true
	}
	s.trace = append(s.trace, "claim:"+token)
	s.claimTokens = append(s.claimTokens, token)
	if len(s.events) == 0 {
		return opportunity.OutboxEvent{}, false, nil
	}
	event := s.events[0]
	s.events = s.events[1:]
	s.activeClaim = true
	return event, true, nil
}

func (s *transferStoreStub) authoritativeCommand(_ context.Context, event opportunity.OutboxEvent, _ time.Time, token string) (signedCommand, string, error) {
	s.trace = append(s.trace, "authority:"+token)
	if s.authorityErr != nil {
		return signedCommand{}, "", s.authorityErr
	}
	return signedCommand{EventID: event.EventID}, s.permanentReason, nil
}

func (s *transferStoreStub) finish(_ context.Context, _ opportunity.OutboxEvent, now time.Time, token, status, intakeID, summary string) error {
	s.trace = append(s.trace, "finish:"+token)
	s.activeClaim = false
	s.finishedAt, s.finishedToken, s.finishedStatus = now, token, status
	s.finishedIntakeID, s.finishedSummary = intakeID, summary
	return nil
}

func (s *transferStoreStub) retry(_ context.Context, _ opportunity.OutboxEvent, now time.Time, token, summary string) error {
	s.trace = append(s.trace, "retry:"+token)
	s.activeClaim = false
	s.retryAt, s.retryToken, s.retrySummary = now, token, summary
	return nil
}

type deliveryClientStub struct {
	trace  *[]string
	result deliveryResult
	err    error
	calls  int
}

func (c *deliveryClientStub) deliver(_ context.Context, command signedCommand) (deliveryResult, error) {
	c.calls++
	if c.trace != nil {
		*c.trace = append(*c.trace, "deliver:"+command.EventID)
	}
	return c.result, c.err
}

func TestRunOnceClaimsAndProcessesOneEventAtATime(t *testing.T) {
	store := &transferStoreStub{events: []opportunity.OutboxEvent{{EventID: "event-1"}, {EventID: "event-2"}, {EventID: "event-3"}}}
	client := &deliveryClientStub{trace: &store.trace, result: deliveryResult{IntakeID: "intake"}}
	nextToken := 0
	app := &App{
		store: store, client: client,
		config: Config{WorkerID: "shared-hostname", BatchSize: 3, LeaseDuration: time.Minute},
		now:    func() time.Time { return time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC) },
		claimToken: func(string) (string, error) {
			nextToken++
			return fmt.Sprintf("claim-%d", nextToken), nil
		},
	}

	count, err := app.RunOnce(context.Background())
	if err != nil || count != 3 {
		t.Fatalf("RunOnce() count=%d err=%v", count, err)
	}
	if store.claimedBeforeFinish {
		t.Fatal("next event was claimed before the current claim was finalized")
	}
	if got := strings.Join(store.trace, ","); got != "claim:claim-1,authority:claim-1,deliver:event-1,finish:claim-1,claim:claim-2,authority:claim-2,deliver:event-2,finish:claim-2,claim:claim-3,authority:claim-3,deliver:event-3,finish:claim-3" {
		t.Fatalf("unexpected operation order: %s", got)
	}
	if client.calls != 3 || store.claimTokens[0] == store.claimTokens[1] || store.claimTokens[1] == store.claimTokens[2] {
		t.Fatalf("calls=%d claim tokens=%v", client.calls, store.claimTokens)
	}
}

func TestLeaseLossBeforeAuthorityPreventsDuplicateDeliveryAndWriteback(t *testing.T) {
	store := &transferStoreStub{events: []opportunity.OutboxEvent{{EventID: "event-1"}}, authorityErr: errLeaseLost}
	client := &deliveryClientStub{}
	app := &App{
		store: store, client: client,
		config:     Config{WorkerID: "shared-hostname", BatchSize: 1, LeaseDuration: time.Minute},
		now:        time.Now,
		claimToken: func(string) (string, error) { return "claim-current", nil },
	}

	count, err := app.RunOnce(context.Background())
	if count != 0 || !errors.Is(err, errLeaseLost) {
		t.Fatalf("RunOnce() count=%d err=%v", count, err)
	}
	if client.calls != 0 || store.finishedStatus != "" || store.retryToken != "" {
		t.Fatalf("lost lease caused side effects: deliveries=%d finish=%q retry=%q", client.calls, store.finishedStatus, store.retryToken)
	}
}

func TestDeliveryTimeoutUsesCompletionTimeAndCurrentFencingToken(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	finish := start.Add(7 * time.Second)
	times := []time.Time{start, start, finish}
	store := &transferStoreStub{events: []opportunity.OutboxEvent{{EventID: "event-1"}}}
	client := &deliveryClientStub{err: context.DeadlineExceeded}
	app := &App{
		store: store, client: client,
		config: Config{WorkerID: "worker", BatchSize: 1, LeaseDuration: time.Minute},
		now: func() time.Time {
			value := times[0]
			times = times[1:]
			return value
		},
		claimToken: func(string) (string, error) { return "claim-timeout", nil },
	}

	count, err := app.RunOnce(context.Background())
	if err != nil || count != 1 {
		t.Fatalf("RunOnce() count=%d err=%v", count, err)
	}
	if store.retryToken != "claim-timeout" || !store.retryAt.Equal(finish) || !errors.Is(client.err, context.DeadlineExceeded) {
		t.Fatalf("retry token=%q at=%v summary=%q", store.retryToken, store.retryAt, store.retrySummary)
	}
}

func TestClaimContractIsSingleEventAndOnlyTargetsContractTransferEvent(t *testing.T) {
	query := claimSQL()
	for _, required := range []string{"event_type=?", "status IN (?,?)", "locked_until<?", "LIMIT 1", "FOR UPDATE SKIP LOCKED"} {
		if !strings.Contains(query, required) {
			t.Fatalf("claim query missing %q: %s", required, query)
		}
	}
}

func TestClaimTokenFencesWorkersWithSameConfiguredID(t *testing.T) {
	first, err := integrationClaimToken("same-hostname")
	if err != nil {
		t.Fatal(err)
	}
	second, err := integrationClaimToken("same-hostname")
	if err != nil {
		t.Fatal(err)
	}
	if first == second || len(first) > 128 || !strings.HasPrefix(first, "ctw-") {
		t.Fatalf("unsafe claim tokens first=%q second=%q", first, second)
	}
}
