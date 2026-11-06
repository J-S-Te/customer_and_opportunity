package customer

import (
	"context"
	"errors"
	"testing"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/ownerdirectory"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
)

// ownerDirectorylessUpdateRepo 只实现 Update 预检需要的读取：负责人目录未接线时
// 拒绝必须发生在任何写入之前，因此其余仓储方法故意不实现（误调用会 panic）。
type ownerDirectorylessUpdateRepo struct {
	Repository
	current *Customer
	updates int
}

func (r *ownerDirectorylessUpdateRepo) FindByID(context.Context, auth.Principal, uint64, bool) (*Customer, error) {
	value := *r.current
	return &value, nil
}

func (r *ownerDirectorylessUpdateRepo) Update(context.Context, *Customer, uint64) error {
	r.updates++
	return nil
}

func ownerDirectorylessUpdateService(t *testing.T, current *Customer) (*Service, *ownerDirectorylessUpdateRepo) {
	t.Helper()
	repository := &ownerDirectorylessUpdateRepo{current: current}
	service := NewService(nil, repository, &createAuditStub{}, profileCodec(t)).UseFreezeChecker(allowAllFreezeChecker{})
	return service, repository
}

func activeCustomerForOwnerUpdate() *Customer {
	return &Customer{
		Model:        database.Model{ID: 7, TenantID: "tenant-a", Version: 2},
		Name:         "客户甲",
		CustomerType: "企业", Industry: "软件", Region: "华东",
		OwnerUserID: "sales-a", OwnerOrgID: "org-a", Status: StatusActive,
	}
}

// OWNER_DIRECTORY_ENABLED=false（目录适配器未接线）时，改变负责人的更新必须失败关闭：
// 没有权威目录就无法证明目标用户/组织是有效任职关系，不能写入任意字符串。
func TestCustomerUpdateWithoutOwnerDirectoryRejectsOwnerChange(t *testing.T) {
	cases := map[string]struct {
		ownerUserID string
		ownerOrgID  string
	}{
		"owner user changed": {ownerUserID: "attacker", ownerOrgID: "org-a"},
		"owner org changed":  {ownerUserID: "sales-a", ownerOrgID: "org-attacker"},
		"both changed":       {ownerUserID: "attacker", ownerOrgID: "org-attacker"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			service, repository := ownerDirectorylessUpdateService(t, activeCustomerForOwnerUpdate())
			ctx := auth.WithPrincipal(context.Background(), duplicateSalesPrincipal("sales-a"))
			result, err := service.Update(ctx, 7, UpdateRequest{
				Name: "客户甲", CustomerType: "企业", Industry: "软件", Region: "华东",
				OwnerUserID: tc.ownerUserID, OwnerOrgID: tc.ownerOrgID,
				Contacts: []UpdateContactInput{{Name: "张三", IsRegistration: true}},
				Version:  2, Reason: "调整负责人",
			})
			if result != nil || !errors.Is(err, ownerdirectory.ErrUnavailable) {
				t.Fatalf("result=%#v err=%v, want %v", result, err, ownerdirectory.ErrUnavailable)
			}
			if repository.updates != 0 {
				t.Fatalf("rejected owner change reached persistence: %d updates", repository.updates)
			}
		})
	}
}

// 不涉及负责人变更的更新保持原行为：目录未接线不再额外拦截，继续走既有校验。
func TestCustomerUpdateWithoutOwnerDirectoryKeepsNonOwnerUpdates(t *testing.T) {
	current := activeCustomerForOwnerUpdate()
	current.Status = StatusVoid
	service, repository := ownerDirectorylessUpdateService(t, current)
	ctx := auth.WithPrincipal(context.Background(), duplicateSalesPrincipal("sales-a"))
	_, err := service.Update(ctx, 7, UpdateRequest{
		Name: "客户甲", CustomerType: "企业", Industry: "软件", Region: "华东",
		OwnerUserID: "sales-a", OwnerOrgID: "org-a",
		Contacts: []UpdateContactInput{{Name: "张三", IsRegistration: true}},
		Version:  2, Reason: "更新客户资料",
	})
	// 已作废客户在负责人门禁之后的既有校验处被拒绝，证明未因目录缺失提前 fail-closed。
	if !errors.Is(err, ErrInactive) {
		t.Fatalf("err=%v, want %v", err, ErrInactive)
	}
	if repository.updates != 0 {
		t.Fatalf("inactive customer reached persistence: %d updates", repository.updates)
	}
}
