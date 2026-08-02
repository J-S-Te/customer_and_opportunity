package portalbootstrap

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestBindProjectMessagePaginationUsesOpaqueAnchorAndRejectsOffset(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		query      string
		wantBefore string
		wantOK     bool
	}{
		{query: "page=1&page_size=100", wantOK: true},
		{query: "before=opaque-cursor&page_size=25", wantBefore: "opaque-cursor", wantOK: true},
		{query: "page=2&page_size=25", wantOK: false},
		{query: "before=a&before=b", wantOK: false},
		{query: "unknown=value", wantOK: false},
	} {
		probe := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(probe)
		context.Request = httptest.NewRequest("GET", "/messages?"+tc.query, nil)
		before, _, ok := bindProjectMessagePagination(context)
		if ok != tc.wantOK || before != tc.wantBefore {
			t.Fatalf("query=%q before=%q ok=%v", tc.query, before, ok)
		}
	}
}
