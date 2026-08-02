// Command seed-demo-data inserts the demo customer and opportunity dataset into
// the CRM schema using the same repositories, encryption codec and audit writer
// as the production server. It is idempotent: rows already present under the
// same tenant and normalized names are skipped, and all creates use stable
// actor-bound idempotency keys. It never deletes or rewrites existing rows.
package main

import (
	"context"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/seeddemo"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/security"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func main() {
	tenantID := flag.String("tenant-id", "", "target tenant; defaults to OIDC_TENANT_ID or tenant-demo")
	actorID := flag.String("actor-id", "", "seed actor OIDC subject; defaults to oidc-sub-demo-seed")
	actorName := flag.String("actor-name", "", "seed actor display name; defaults to 演示数据初始化")
	flag.Parse()

	tenant := strings.TrimSpace(firstNonEmpty(*tenantID, os.Getenv("CRM_SEED_TENANT_ID"), os.Getenv("OIDC_TENANT_ID"), seeddemo.DefaultTenantID))
	actor := strings.TrimSpace(firstNonEmpty(*actorID, seeddemo.DefaultActorID))
	actorDisplayName := strings.TrimSpace(firstNonEmpty(*actorName, seeddemo.DefaultActorName))

	dsn := strings.TrimSpace(os.Getenv("MYSQL_DSN"))
	if dsn == "" {
		fatalf("MYSQL_DSN is required (same DSN as crm-server)")
	}
	codec, err := sensitiveCodec()
	if err != nil {
		fatalf("sensitive codec: %v", err)
	}

	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		fatalf("open database: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err = db.WithContext(ctx).Exec("SELECT 1").Error; err != nil {
		fatalf("connect to database: %v", err)
	}

	summary, err := seeddemo.Run(ctx, seeddemo.Dependencies{
		DB: db, Codec: codec, TenantID: tenant, ActorID: actor, ActorName: actorDisplayName,
	})
	if err != nil {
		fatalf("seed demo data: %v", err)
	}

	fmt.Printf("tenant=%s actor=%s\n", tenant, actor)
	fmt.Printf("customers created=%d skipped=%d\n", summary.CustomersCreated, summary.CustomersSkipped)
	for _, item := range summary.Customers {
		action := "skipped"
		if item.Created {
			action = "created"
		}
		fmt.Printf("  [%s] %s (%s) id=%d %s\n", item.Key, item.Name, item.CustomerNo, item.ID, action)
	}
	fmt.Printf("opportunities created=%d skipped=%d\n", summary.OpportunitiesCreated, summary.OpportunitiesSkipped)
	for _, item := range summary.Opportunities {
		action := "skipped"
		if item.Created {
			action = "created"
		}
		fmt.Printf("  [%s] %s (%s) id=%d stage=%s %s\n", item.Key, item.Name, item.OpportunityNo, item.ID, item.Stage, action)
	}
	fmt.Println("演示数据初始化完成；重复执行不会产生重复客户或商机。")
}

func sensitiveCodec() (*security.SensitiveCodec, error) {
	encryptionKey, err := base64.StdEncoding.DecodeString(strings.TrimSpace(os.Getenv("SENSITIVE_ENCRYPTION_KEY_BASE64")))
	if err != nil {
		return nil, fmt.Errorf("SENSITIVE_ENCRYPTION_KEY_BASE64 must be valid base64: %w", err)
	}
	hmacKey, err := base64.StdEncoding.DecodeString(strings.TrimSpace(os.Getenv("SENSITIVE_HMAC_KEY_BASE64")))
	if err != nil {
		return nil, fmt.Errorf("SENSITIVE_HMAC_KEY_BASE64 must be valid base64: %w", err)
	}
	return security.NewSensitiveCodec(encryptionKey, hmacKey)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
