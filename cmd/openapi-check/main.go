package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/openapicheck"
)

func main() {
	root := flag.String("root", ".", "customer_and_opportunity repository root")
	flag.Parse()
	absolute, err := filepath.Abs(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	for _, item := range []struct{ application, file string }{{"crm", "crm.yaml"}, {"portal", "portal.yaml"}} {
		if err = openapicheck.Check(absolute, item.application, filepath.Join(absolute, "api/openapi", item.file)); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	fmt.Println("CRM and Portal OpenAPI route contracts match production Gin registration")
}
