package customer

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/requestbody"
)

// updateContactsRepoStub 覆盖客户更新的写入面：FindByID 返回带联系人快照的当前客户，
// 并记录 Update/ReplaceContacts 的调用次数与替换后的联系人集合，用于证明
// “未提交联系人”时不会触碰联系人表。
type updateContactsRepoStub struct {
	Repository
	current              *Customer
	duplicates           []DuplicateCandidate
	updateCalls          int
	replaceContactsCalls int
	replaced             []Contact
	changeLogs           []ChangeLog
}

func (r *updateContactsRepoStub) FindByID(context.Context, auth.Principal, uint64, bool) (*Customer, error) {
	value := *r.current
	value.Contacts = append([]Contact(nil), r.current.Contacts...)
	return &value, nil
}

func (r *updateContactsRepoStub) FindDuplicates(context.Context, string, string, string, uint64) ([]DuplicateCandidate, error) {
	return append([]DuplicateCandidate(nil), r.duplicates...), nil
}

func (r *updateContactsRepoStub) Update(context.Context, *Customer, uint64) error {
	r.updateCalls++
	return nil
}

func (r *updateContactsRepoStub) ReplaceContacts(_ context.Context, model *Customer) error {
	r.replaceContactsCalls++
	r.replaced = append([]Contact(nil), model.Contacts...)
	return nil
}

func (r *updateContactsRepoStub) CreateChangeLog(_ context.Context, log *ChangeLog) error {
	r.changeLogs = append(r.changeLogs, *log)
	return nil
}

// newUpdateContactsTestService 复用生产构造与假审计器；事务接缝只去掉真实 GORM 事务，
// 让假仓储能验证更新路径上的仓储调用序列（与 opportunity.Service 的事务接缝同约定）。
func newUpdateContactsTestService(t *testing.T, repo *updateContactsRepoStub, writer *createAuditStub) *Service {
	t.Helper()
	service := NewService(nil, repo, writer, profileCodec(t)).UseFreezeChecker(allowAllFreezeChecker{})
	service.updateTransaction = func(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) }
	return service
}

// updateContactsTestCustomer 对应现场客户 65：两个联系人，其中 ID=11 是注册联系人。
func updateContactsTestCustomer() *Customer {
	return &Customer{
		Model:          database.Model{ID: 65, TenantID: "tenant-a", CreatedBy: "sales-a", UpdatedBy: "sales-a", Version: 3},
		CustomerNo:     "KH202608010001",
		Name:           "本人客户",
		NormalizedName: normalizeName("本人客户"),
		CustomerType:   "企业", Industry: "软件", Region: "华东",
		OwnerUserID: "sales-a", OwnerOrgID: "org-a", Status: StatusActive, CreditLevel: "B",
		Contacts: []Contact{
			{Model: database.Model{ID: 11, TenantID: "tenant-a", Version: 1}, CustomerID: 65, Name: "张三", PhoneMasked: "138****8000", IsRegistration: true, SortOrder: 0},
			{Model: database.Model{ID: 12, TenantID: "tenant-a", Version: 1}, CustomerID: 65, Name: "李四", PhoneMasked: "139****9000", SortOrder: 1},
		},
	}
}

func updateContactsTestRequest(contacts *[]UpdateContactInput) UpdateRequest {
	return UpdateRequest{
		Name: "本人客户", CustomerType: "企业", Industry: "软件", Region: "华东",
		OwnerUserID: "sales-a", OwnerOrgID: "org-a",
		Contacts: contacts, Version: 3, Reason: "更新客户资料",
	}
}

func updateContactsTestPrincipal() auth.Principal { return duplicateSalesPrincipal("sales-a") }

// assertContactsPreserved 校验响应中的联系人仍是数据库既有行（ID、注册标记、顺序不变）。
func assertContactsPreserved(t *testing.T, contacts []ContactResponse) {
	t.Helper()
	if len(contacts) != 2 {
		t.Fatalf("contacts=%#v, want 2 preserved contacts", contacts)
	}
	if contacts[0].ID != 11 || contacts[0].Name != "张三" || !contacts[0].IsRegistration {
		t.Fatalf("registration contact changed: %#v", contacts[0])
	}
	if contacts[1].ID != 12 || contacts[1].Name != "李四" || contacts[1].IsRegistration {
		t.Fatalf("secondary contact changed: %#v", contacts[1])
	}
}

// contacts 缺省（nil 指针）时更新成功，且联系人集合完全不变：不强求重传联系人，
// 也不重建联系人行（不调用 ReplaceContacts）。
func TestCustomerUpdateWithoutContactsKeepsExistingContacts(t *testing.T) {
	repo := &updateContactsRepoStub{current: updateContactsTestCustomer()}
	writer := &createAuditStub{}
	service := newUpdateContactsTestService(t, repo, writer)

	result, err := service.Update(auth.WithPrincipal(context.Background(), updateContactsTestPrincipal()), 65, updateContactsTestRequest(nil))
	if err != nil {
		t.Fatalf("update without contacts rejected: %v", err)
	}
	if repo.updateCalls != 1 {
		t.Fatalf("update calls=%d, want 1", repo.updateCalls)
	}
	if repo.replaceContactsCalls != 0 || repo.replaced != nil {
		t.Fatalf("contacts rewritten: calls=%d replaced=%#v", repo.replaceContactsCalls, repo.replaced)
	}
	assertContactsPreserved(t, result.Contacts)
	assertContactsPreserved(t, toResponse(repo.current).Contacts)
	for _, log := range repo.changeLogs {
		if log.FieldName == "contacts" {
			t.Fatalf("unchanged contacts produced change log: %#v", log)
		}
	}
	if writer.writes != 1 {
		t.Fatalf("audit writes=%d, want 1", writer.writes)
	}
}

// 空数组同样按“保留既有联系人”处理：既兼容尚未回填联系人的旧前端，也避免空集合被
// 误读成“清空联系人”。这里走 HTTP 解码 + handler + service 全链路，锁住“不再 400”的现场缺陷。
func TestCustomerUpdateWithOmittedOrEmptyContactsReturnsOK(t *testing.T) {
	cases := map[string]string{
		"contacts omitted": `{"name":"本人客户","customer_type":"企业","industry":"软件","region":"华东","owner_user_id":"sales-a","owner_org_id":"org-a","version":3,"reason":"更新客户资料"}`,
		"contacts empty":   `{"name":"本人客户","customer_type":"企业","industry":"软件","region":"华东","owner_user_id":"sales-a","owner_org_id":"org-a","version":3,"reason":"更新客户资料","contacts":[]}`,
		"contacts null":    `{"name":"本人客户","customer_type":"企业","industry":"软件","region":"华东","owner_user_id":"sales-a","owner_org_id":"org-a","version":3,"reason":"更新客户资料","contacts":null}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			repo := &updateContactsRepoStub{current: updateContactsTestCustomer()}
			service := newUpdateContactsTestService(t, repo, &createAuditStub{})
			recorder := httptest.NewRecorder()
			ginContext, _ := gin.CreateTestContext(recorder)
			ginContext.Request = httptest.NewRequest(http.MethodPut, "/customer-opportunity/api/v1/customers/65", strings.NewReader(body))
			ginContext.Request = ginContext.Request.WithContext(auth.WithPrincipal(ginContext.Request.Context(), updateContactsTestPrincipal()))
			ginContext.Params = gin.Params{{Key: "id", Value: "65"}}

			NewHandler(service).Update(ginContext)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			if repo.updateCalls != 1 || repo.replaceContactsCalls != 0 {
				t.Fatalf("updates=%d replaceContacts=%d", repo.updateCalls, repo.replaceContactsCalls)
			}
			if !strings.Contains(recorder.Body.String(), `"is_registration":true`) {
				t.Fatalf("preserved contacts missing from response: %s", recorder.Body.String())
			}
			assertContactsPreserved(t, toResponse(repo.current).Contacts)
		})
	}
}

// 提交非空数组仍走整表替换：保留既有敏感字段复用、软删除重建与审计 diff 语义。
func TestCustomerUpdateWithContactsArrayStillReplacesWholeCollection(t *testing.T) {
	repo := &updateContactsRepoStub{current: updateContactsTestCustomer()}
	service := newUpdateContactsTestService(t, repo, &createAuditStub{})
	contacts := []UpdateContactInput{{ID: 11, Name: "张三", IsRegistration: true}}

	result, err := service.Update(auth.WithPrincipal(context.Background(), updateContactsTestPrincipal()), 65, updateContactsTestRequest(&contacts))
	if err != nil {
		t.Fatalf("contact replacement rejected: %v", err)
	}
	if repo.replaceContactsCalls != 1 {
		t.Fatalf("replace contacts calls=%d, want 1", repo.replaceContactsCalls)
	}
	if len(repo.replaced) != 1 || repo.replaced[0].Name != "张三" || !repo.replaced[0].IsRegistration {
		t.Fatalf("replacement contacts=%#v", repo.replaced)
	}
	// 未提交 phone 时沿用原密文/掩码，既有敏感字段语义不变。
	if repo.replaced[0].PhoneMasked != "138****8000" {
		t.Fatalf("phone mask=%q, want preserved mask", repo.replaced[0].PhoneMasked)
	}
	if len(result.Contacts) != 1 || result.Contacts[0].Name != "张三" {
		t.Fatalf("response contacts=%#v", result.Contacts)
	}
	contactsLogged := false
	for _, log := range repo.changeLogs {
		if log.FieldName == "contacts" {
			contactsLogged = true
		}
	}
	if !contactsLogged {
		t.Fatalf("contact replacement missing audit change log: %#v", repo.changeLogs)
	}
}

// 整表替换的既有校验不放宽：缺注册联系人、重复注册联系人、超出集合上限都在落库前拒绝。
func TestCustomerUpdateWithContactsArrayKeepsExistingContactValidation(t *testing.T) {
	cases := map[string]struct {
		contacts []UpdateContactInput
		wantErr  error
	}{
		"missing registration contact": {
			contacts: []UpdateContactInput{{Name: "李四", Phone: stringPointer("13900139000")}},
			wantErr:  ErrInvalidContact,
		},
		"multiple registration contacts": {
			contacts: []UpdateContactInput{{Name: "张三", IsRegistration: true}, {Name: "李四", IsRegistration: true}},
			wantErr:  ErrInvalidContact,
		},
		"blank contact name": {
			contacts: []UpdateContactInput{{Name: "  ", IsRegistration: true}},
			wantErr:  ErrInvalidContact,
		},
		"over limit contacts": {
			contacts: overLimitUpdateContactInputs(),
			wantErr:  ErrContactsLimitExceeded,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			repo := &updateContactsRepoStub{current: updateContactsTestCustomer()}
			service := newUpdateContactsTestService(t, repo, &createAuditStub{})
			contacts := tc.contacts
			result, err := service.Update(auth.WithPrincipal(context.Background(), updateContactsTestPrincipal()), 65, updateContactsTestRequest(&contacts))
			if result != nil || !errors.Is(err, tc.wantErr) {
				t.Fatalf("result=%#v err=%v, want %v", result, err, tc.wantErr)
			}
			if repo.updateCalls != 0 || repo.replaceContactsCalls != 0 {
				t.Fatalf("rejected contacts reached persistence: %#v", repo)
			}
		})
	}
}

// DTO 的 max=50 绑定标签必须继续作用于指针化后的切片，避免“可选”变成“无上限”。
func TestCustomerUpdateRequestContactsBindingStillLimitsCollectionSize(t *testing.T) {
	gin.SetMode(gin.TestMode)
	decode := func(contacts string) error {
		body := `{"name":"本人客户","customer_type":"企业","industry":"软件","region":"华东","owner_user_id":"sales-a","version":3,"reason":"更新客户资料","contacts":` + contacts + `}`
		recorder := httptest.NewRecorder()
		ginContext, _ := gin.CreateTestContext(recorder)
		ginContext.Request = httptest.NewRequest(http.MethodPut, "/customer-opportunity/api/v1/customers/65", strings.NewReader(body))
		var input UpdateRequest
		return requestbody.DecodeJSON(ginContext, &input)
	}
	items := make([]string, maxContacts+1)
	for index := range items {
		items[index] = `{"name":"联系人","is_registration":true}`
	}
	if err := decode("[" + strings.Join(items, ",") + "]"); err == nil {
		t.Fatalf("%d contacts accepted, want binding failure", len(items))
	}
	if err := decode("[" + strings.Join(items[:maxContacts], ",") + "]"); err != nil {
		t.Fatalf("%d contacts rejected: %v", maxContacts, err)
	}
}
