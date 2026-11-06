package customer

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
)

func overLimitContactInputs() []ContactInput {
	contacts := make([]ContactInput, maxContacts+1)
	for index := range contacts {
		contacts[index] = ContactInput{Name: "联系人", Phone: "13800138000", IsRegistration: index == 0}
	}
	return contacts
}

func overLimitUpdateContactInputs() []UpdateContactInput {
	contacts := make([]UpdateContactInput, maxContacts+1)
	for index := range contacts {
		phone := "13800138000"
		contacts[index] = UpdateContactInput{Name: "联系人", Phone: &phone, IsRegistration: index == 0}
	}
	return contacts
}

// 联系人集合上限在服务层再校验一次（与 DTO 的 max=50 绑定标签同口径），
// 覆盖绕过 HTTP 绑定的内部调用。超限按请求校验失败（422）拒绝。
func TestCustomerContactCollectionLimitIsEnforcedByService(t *testing.T) {
	if status := apperror.As(ErrContactsLimitExceeded).HTTPStatus; status != http.StatusUnprocessableEntity {
		t.Fatalf("limit error status=%d, want 422", status)
	}
	if err := validateRegistrationContacts(overLimitContactInputs()); !errors.Is(err, ErrContactsLimitExceeded) {
		t.Fatalf("create contacts over limit err=%v", err)
	}
	if err := validateRegistrationContacts(overLimitContactInputs()[:maxContacts]); err != nil {
		t.Fatalf("contacts at limit rejected: %v", err)
	}
	if err := validateUpdateRegistrationContacts(overLimitUpdateContactInputs()); !errors.Is(err, ErrContactsLimitExceeded) {
		t.Fatalf("update contacts over limit err=%v", err)
	}
	if err := validateUpdateRegistrationContacts(overLimitUpdateContactInputs()[:maxContacts]); err != nil {
		t.Fatalf("update contacts at limit rejected: %v", err)
	}
}

// 创建路径同样以 422 暴露上限错误，且在联系人校验阶段就失败、不落库。
func TestCustomerCreateRejectsContactsOverLimitBeforePersistence(t *testing.T) {
	repository := &createIdempotencyRepoStub{}
	service := newCreateTestService(t, repository, &createAuditStub{})
	request := createTestRequest("contacts-limit")
	request.Contacts = overLimitContactInputs()
	result, err := service.Create(auth.WithPrincipal(context.Background(), createTestPrincipal("tenant-a", "user-a")), request)
	if result != nil || !errors.Is(err, ErrContactsLimitExceeded) {
		t.Fatalf("result=%#v err=%v, want %v", result, err, ErrContactsLimitExceeded)
	}
	if status := apperror.As(err).HTTPStatus; status != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d, want 422", status)
	}
	if repository.findReplayCalls != 0 || repository.nextNumberCalls != 0 || repository.createCalls != 0 {
		t.Fatalf("over-limit contacts reached persistence: %#v", repository)
	}
}
