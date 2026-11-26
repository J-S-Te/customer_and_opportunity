package credit

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"io"
	"reflect"
	"testing"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/ownerdirectory"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type pendingApplicantRows struct{ sent bool }

// Columns 返回脚本数据库中的申请标识与申请人标识列。
// 参数：无。
// 返回值：与真实查询同名的列。
func (*pendingApplicantRows) Columns() []string { return []string{"id", "applicant_id"} }

// Close 结束一次脚本查询。
// 参数：无。
// 返回值：无错误。
func (*pendingApplicantRows) Close() error { return nil }

// Next 提供预设的历史信用申请行。
// 参数：values 为数据库读取目标缓冲区。
// 返回值：一行成功或查询结束。
func (r *pendingApplicantRows) Next(values []driver.Value) error {
	if r.sent {
		return io.EOF
	}
	r.sent = true
	values[0], values[1] = int64(7), "sales-a"
	return nil
}

type pendingApplicantConnection struct{ driver.Conn }

// Close 结束脚本连接。
// 参数：无。
// 返回值：无错误。
func (pendingApplicantConnection) Close() error { return nil }

// QueryContext 提供待办查询的持久化输入，不建立网络连接。
// 参数：上下文、SQL 及绑定参数均由真实 GORM 查询提供。
// 返回值：一行历史申请。
func (pendingApplicantConnection) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return &pendingApplicantRows{}, nil
}

type pendingApplicantConnector struct{ driver.Connector }

// Connect 建立脚本连接以执行真实待办读取。
// 参数：上下文来自服务调用。
// 返回值：本地测试替身或连接错误。
func (pendingApplicantConnector) Connect(context.Context) (driver.Conn, error) {
	return pendingApplicantConnection{}, nil
}

type pendingApplicantDirectory struct {
	ownerdirectory.UnavailableCatalog
	ids []string
	err error
}

// Resolve 为测试中的已授权申请人提供可信姓名。
// 参数：上下文来自待办读取，ids 为当前已返回记录的申请人。
// 返回值：确定的目录结果或故障。
func (d *pendingApplicantDirectory) Resolve(_ context.Context, ids []string) (map[string]ownerdirectory.User, error) {
	d.ids = append([]string(nil), ids...)
	return map[string]ownerdirectory.User{"sales-a": {ID: "sales-a", DisplayName: "历史信用提交人"}}, d.err
}

// TestPendingReturnsAuthorizedApplicantName 验证历史申请通过可信目录返回姓名。
// 参数：t 为执行真实 GORM 扫描与响应序列化的测试上下文。
// 返回值：无；姓名缺失或目录故障阻断待办时测试失败。
func TestPendingReturnsAuthorizedApplicantName(t *testing.T) {
	pool := sql.OpenDB(pendingApplicantConnector{})
	t.Cleanup(func() { pool.Close() })
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: pool, SkipInitializeWithVersion: true}), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	directory := &pendingApplicantDirectory{}
	service := NewService(db).UseOwnerDirectory(directory)
	ctx := auth.WithPrincipal(context.Background(), creditApprover("director-a", auth.ScopeAll))
	items, err := service.Pending(ctx)
	if err != nil || len(items) != 1 {
		t.Fatalf("待办未返回：%v %v", items, err)
	}
	got, err := json.Marshal(items[0])
	if err != nil || !containsApplicantName(got, "历史信用提交人") {
		t.Fatalf("申请人姓名未出现在待办：%s %v", got, err)
	}
	if !reflect.DeepEqual(directory.ids, []string{"sales-a"}) {
		t.Fatalf("查询了非待办申请人：%v", directory.ids)
	}
	directory.err = ownerdirectory.ErrUnavailable
	items, err = service.Pending(ctx)
	if err != nil || len(items) != 1 {
		t.Fatalf("目录故障不应阻断待办：%v %v", items, err)
	}
}

// containsApplicantName 核对 HTTP 序列化使用的申请人姓名字段。
// 参数：encoded 为待办 JSON，want 为预期姓名。
// 返回值：字段真实匹配时返回 true。
func containsApplicantName(encoded []byte, want string) bool {
	var row struct {
		Name string `json:"applicant_name"`
	}
	return json.Unmarshal(encoded, &row) == nil && row.Name == want
}
