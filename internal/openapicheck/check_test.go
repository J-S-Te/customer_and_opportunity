package openapicheck

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestProductionRoutesMatchOpenAPI(t *testing.T) {
	_, current, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(current), "../.."))
	for _, test := range []struct{ application, spec string }{
		{"crm", "crm.yaml"},
		{"portal", "portal.yaml"},
	} {
		t.Run(test.application, func(t *testing.T) {
			if err := Check(root, test.application, filepath.Join(root, "api/openapi", test.spec)); err != nil {
				t.Fatal(err)
			}
		})
	}
}
