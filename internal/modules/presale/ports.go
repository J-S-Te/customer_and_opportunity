package presale

import (
	"context"
	"time"
)

// OpportunityReader is the only allowed dependency from presale to the
// opportunity module. Implementations must enforce the supplied actor's scope.
type OpportunityReader interface {
	GetAccessible(ctx context.Context, actor Actor, opportunityID uint64) (OpportunitySnapshot, error)
}

// PhoneProtector keeps encryption and masking policy outside the domain module.
type PhoneProtector interface {
	Encrypt(ctx context.Context, plaintext string) ([]byte, error)
	Decrypt(ctx context.Context, ciphertext []byte) (string, error)
	Mask(plaintext string) string
}

type Clock interface{ Now() time.Time }

type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now().UTC() }

// IDGenerator must return collision-resistant opaque identifiers.
type IDGenerator interface{ NewID() string }

// ActorResolver bridges the host application's authenticated context to this module.
type ActorResolver interface {
	Resolve(ctx context.Context) (Actor, error)
}

// ApprovalCommandPort represents the real approval engine adapter. The module
// records commands in outbox; a worker may invoke this port after commit.
type ApprovalCommandPort interface {
	Start(ctx context.Context, event OutboxEvent) (ApprovalStartResult, error)
	Act(ctx context.Context, event OutboxEvent) error
}

type ApprovalStartResult struct {
	EngineInstanceID string
	EventSequence    uint64
}

// PMSPublisher is intentionally only a port. Production infrastructure owns
// authentication, timeouts and the actual MQ/HTTP delivery.
type PMSPublisher interface {
	PublishWorklog(ctx context.Context, event OutboxEvent) (responseCode string, err error)
}
