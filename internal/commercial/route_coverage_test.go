package commercial_test

import (
	core "github.com/J-S-Te/license-core"
	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/commercial"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/credit"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/customer"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/notification"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/opportunity"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/presale"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/portalbootstrap"
	"strings"
	"testing"
)

func TestRegisteredHistoricalGETsAreNotSilentlyBlocked(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	api := r.Group("/api/v1")
	internal := api.Group("/internal")
	customer.RegisterRoutes(api, nil)
	opportunity.RegisterRoutes(api, nil)
	presale.RegisterRoutes(api, nil)
	credit.RegisterRoutes(api, nil)
	credit.RegisterInternalRoutes(internal, nil)
	notification.RegisterRoutes(api, nil)
	for _, route := range r.Routes() {
		if route.Method == "GET" && commercial.Operation("crm", route.Method, route.Path) == core.MUTATE_BUSINESS {
			t.Errorf("unclassified historical CRM GET %s", route.Path)
		}
	}
	p := portalbootstrap.NewRouter(portalbootstrap.RouterDependencies{Config: portalbootstrap.Config{PathPrefix: "/customer-portal"}})
	for _, route := range p.Routes() {
		path := strings.TrimPrefix(route.Path, "/customer-portal")
		if route.Method == "GET" && path != "/activate" && commercial.Operation("portal", route.Method, path) == core.MUTATE_BUSINESS {
			t.Errorf("unclassified historical Portal GET %s", path)
		}
	}
}
