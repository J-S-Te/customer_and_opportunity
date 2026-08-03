// Command migration-plan 生成不可变、按 schema 区分的发布清单；不连接数据库，也不执行 DDL。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/migrationplan"
)

func main() {
	schemaFlag := flag.String("schema", "", "required target schema: crm or portal")
	directory := flag.String("dir", "migrations", "migration directory")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "migration-plan accepts flags only")
		os.Exit(2)
	}
	schema := migrationplan.Schema(*schemaFlag)
	if schema != migrationplan.CRM && schema != migrationplan.Portal {
		fmt.Fprintln(os.Stderr, "-schema must be crm or portal")
		os.Exit(2)
	}
	plan, err := migrationplan.Build(*directory, schema)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	payload := struct {
		migrationplan.Plan
		CombinedChecksum string `json:"combined_checksum"`
	}{Plan: plan, CombinedChecksum: migrationplan.CombinedChecksum(plan)}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(payload); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
