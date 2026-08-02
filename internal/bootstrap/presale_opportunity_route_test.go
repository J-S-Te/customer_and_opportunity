package bootstrap

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestOpportunityPresaleSummaryRouteRequiresOnlyParentPermission(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("app.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.Contains(line, `"/opportunities/:id/presale-requests"`) {
			continue
		}
		if !strings.Contains(line, `RequirePermission("opportunity.read")`) || strings.Contains(line, `RequirePermission("presale.read")`) {
			t.Fatalf("unexpected route permission chain: %s", line)
		}
		return
	}
	t.Fatal("opportunity presale summary route missing")
}

func TestOpportunityPresaleSummaryRejectsUnknownAndDuplicateQueryParameters(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		url  string
		want bool
	}{
		{url: "/opportunities/1/presale-requests?page=1&page_size=10", want: true},
		{url: "/opportunities/1/presale-requests?page=1&page=2", want: false},
		{url: "/opportunities/1/presale-requests?scope=all", want: false},
		{url: "/opportunities/1/presale-requests?page=1;scope=all", want: false},
	} {
		recorder := httptest.NewRecorder()
		contextValue, _ := gin.CreateTestContext(recorder)
		contextValue.Request = httptest.NewRequest(http.MethodGet, test.url, nil)
		if got := onlyOpportunityPresaleQueryKeys(contextValue); got != test.want {
			t.Errorf("onlyOpportunityPresaleQueryKeys(%q)=%v, want %v", test.url, got, test.want)
		}
	}
}
