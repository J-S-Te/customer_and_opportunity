package bootstrap

import (
	"context"
	"log/slog"
	"time"

	core "github.com/J-S-Te/license-core"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/commercial"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/customer"
)

func runCustomerImportSources(ctx context.Context, tenantID string, service *customer.Service, check commercial.Check) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		for i := 0; i < 10; i++ {
			attemptCtx, cancel := context.WithTimeout(ctx, 75*time.Second)
			worked, err := service.ProcessImportSource(attemptCtx, tenantID, func(callCtx context.Context, release bool) error {
				if check == nil {
					return customer.ErrImportGatewayUnavailable
				}
				operation := core.MUTATE_BUSINESS
				if release {
					operation = core.ESSENTIAL_SERVICE
				}
				return check(callCtx, operation)
			})
			cancel()
			if err != nil {
				if ctx.Err() == nil {
					slog.Warn("customer import source reconciliation deferred")
				}
				break
			}
			if !worked {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
