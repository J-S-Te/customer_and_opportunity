package presale

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/audit"
)

const (
	contactPhoneRelationManager    = "MANAGER"
	contactPhoneRelationAssignment = "ASSIGNMENT"
)

// 电话明文只在独立敏感信息请求中解密，普通列表和详情始终保持脱敏。
// 返回明文前必须先成功写入隐私审计，因此审计不可用时该数据路径失败关闭。
func (s *Service) ContactPhone(ctx context.Context, actor Actor, id uint64) (ContactPhoneView, error) {
	if !actor.Can("presale.contact_phone.read") {
		return ContactPhoneView{}, ErrForbidden
	}
	requestValue, err := s.repo.FindRequest(ctx, actor.TenantID, id)
	if err != nil {
		return ContactPhoneView{}, err
	}
	relation, err := s.contactPhoneRelation(ctx, actor, requestValue)
	if err != nil {
		return ContactPhoneView{}, err
	}
	if relation == "" {
		return ContactPhoneView{}, ErrForbidden
	}
	if s.phones == nil || s.auditWriter == nil {
		return ContactPhoneView{}, ErrContactPhoneUnavailable
	}
	plaintext, err := s.phones.Decrypt(ctx, requestValue.ContactPhoneCipher)
	if err != nil {
		return ContactPhoneView{}, ErrContactPhoneUnavailable
	}
	plaintext = strings.TrimSpace(plaintext)
	if !validDecryptedContactPhone(plaintext) || s.phones.Mask(plaintext) != requestValue.ContactPhoneMasked {
		return ContactPhoneView{}, ErrContactPhoneUnavailable
	}
	if err = s.auditWriter.Write(ctx, audit.Event{
		TenantID: actor.TenantID, Module: "presale", Operation: "CONTACT_PHONE_VIEW",
		ResourceType: "presale_request", ResourceID: fmt.Sprint(requestValue.ID),
		ActorID: actor.UserID, ActorNameSnapshot: actor.UserName,
		AfterJSON: audit.JSON(map[string]string{"authorization_relation": relation}), Result: "SUCCESS",
	}); err != nil {
		return ContactPhoneView{}, ErrContactPhoneUnavailable
	}
	return ContactPhoneView{RequestID: requestValue.ID, ContactPhone: plaintext}, nil
}

// 普通详情只返回服务端计算的“是否可查看”能力，不触发解密，也不携带电话号码。
func (s *Service) canViewContactPhone(ctx context.Context, actor Actor, value *PresaleRequest) (bool, error) {
	if !actor.Can("presale.contact_phone.read") {
		return false, nil
	}
	relation, err := s.contactPhoneRelation(ctx, actor, value)
	return relation != "", err
}

func (s *Service) contactPhoneRelation(ctx context.Context, actor Actor, value *PresaleRequest) (string, error) {
	// 审计员是显式拒绝，优先于同一主体可能附带的其他角色或过期 PMS 分派关系。
	if actor.HasRole("auditor") {
		return "", nil
	}
	if actor.HasRole("sales_director") || actor.HasRole("technical_director") || actor.HasRole("team_lead") {
		return contactPhoneRelationManager, nil
	}
	if strings.TrimSpace(actor.PersonID) == "" {
		return "", nil
	}
	assignments, err := s.repo.ListAssignments(ctx, actor.TenantID, value.ID)
	if err != nil {
		return "", err
	}
	for _, assignment := range assignments {
		if assignment.AssigneeID == actor.PersonID {
			return contactPhoneRelationAssignment, nil
		}
	}
	return "", nil
}

func validDecryptedContactPhone(value string) bool {
	runes := []rune(value)
	if len(runes) == 0 || len(runes) > 64 {
		return false
	}
	for _, current := range runes {
		if unicode.IsControl(current) {
			return false
		}
	}
	return true
}
