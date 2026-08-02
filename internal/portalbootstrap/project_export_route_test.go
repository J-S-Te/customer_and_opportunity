package portalbootstrap

import (
	"os"
	"strings"
	"testing"
)

func TestProjectExportRoutesUseDedicatedTokenHeaderAndRejectQuery(t *testing.T) {
	value, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(value)
	for _, expected := range []string{
		`api.POST("/projects/:projectID/exports"`,
		`api.GET("/project-exports/:id"`,
		`api.POST("/project-exports/:id/download-grants"`,
		`api.POST("/project-exports/:id/downloads"`,
		`c.GetHeader("X-Project-Export-Download-Token")`,
		`if !onlyProjectQueryKeys(c)`,
	} {
		if !strings.Contains(source, expected) {
			t.Fatalf("router missing %q", expected)
		}
	}
	if strings.Contains(source, `project-exports/:id/downloads/:token`) {
		t.Fatal("project export token must not be in URL")
	}
}
