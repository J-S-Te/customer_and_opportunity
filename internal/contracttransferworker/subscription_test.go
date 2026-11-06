package contracttransferworker

import (
	"context"
	"testing"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/opportunity"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// TestUnsubscribedWorkerDoesNotRequireContractCredentials 验证停订阅不要求合同凭据。
// 参数：t 为当前测试上下文。
// 返回值：无；未订阅仍依赖合同时测试失败。
func TestUnsubscribedWorkerDoesNotRequireContractCredentials(t *testing.T) {
	t.Setenv("CRM_CONTRACT_TRANSFER_SUBSCRIBED", "false")
	t.Setenv("MYSQL_DSN", "crm:test@tcp(localhost:3306)/crm")
	t.Setenv("CONTRACT_CLIENT_ID", "")
	t.Setenv("CONTRACT_CLIENT_SECRET", "")
	t.Setenv("CONTRACT_TOKEN_URL", "")
	t.Setenv("CONTRACT_OPPORTUNITY_INTAKE_URL", "")
	if _, err := LoadConfig(); err != nil {
		t.Fatalf("未订阅仍要求合同配置: %v", err)
	}
}

// TestUnsubscribedWorkerPreservesBacklog 验证停订阅不领取、不投递且不改历史任务。
// 参数：t 为当前测试上下文。
// 返回值：无；任意处理副作用发生时测试失败。
func TestUnsubscribedWorkerPreservesBacklog(t *testing.T) {
	store := &transferStoreStub{events: []opportunity.OutboxEvent{{EventID: "pending-1", Status: "PENDING"}}}
	client := &deliveryClientStub{}
	app := &App{store: store, client: client, config: Config{SubscriptionDisabled: true}}
	count, err := app.RunOnce(context.Background())
	if err != nil || count != 0 || client.calls != 0 || len(store.trace) != 0 || len(store.events) != 1 || store.events[0].Status != "PENDING" {
		t.Fatalf("停订阅改变积压: count=%d err=%v store=%+v", count, err, store)
	}
}

// TestUnsubscribedWorkerBuildsWithoutContractClient 验证停订阅不装配合同网络客户端。
// 参数：t 为当前测试上下文。
// 返回值：无；装配仍要求远端合同时失败。
func TestUnsubscribedWorkerBuildsWithoutContractClient(t *testing.T) {
	original := mysqlDialector
	t.Cleanup(func() { mysqlDialector = original })
	mysqlDialector = func(dsn string) gorm.Dialector {
		return mysql.New(mysql.Config{DSN: dsn, SkipInitializeWithVersion: true})
	}
	app, err := New(Config{SubscriptionDisabled: true, MySQLDSN: "test:test@tcp(127.0.0.1:1)/unused"})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if app.client != nil || app.db == nil {
		t.Fatal("停订阅未保留自有存储或仍装配合同客户端")
	}
}
