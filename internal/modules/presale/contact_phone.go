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

// ContactPhone decrypts the phone only for an explicit, independently
// authorized request. Ordinary detail and list projections remain masked.
// The audit write deliberately happens before the plaintext is returned so an
// unavailable audit store closes the sensitive-data path.
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

// canViewContactPhone is safe to include in the ordinary detail DTO: it only
// reports the server-side capability and never decrypts or returns the phone.
func (s *Service) canViewContactPhone(ctx context.Context, actor Actor, value *PresaleRequest) (bool, error) {
	if !actor.Can("presale.contact_phone.read") {
		return false, nil
	}
	relation, err := s.contactPhoneRelation(ctx, actor, value)
	return relation != "", err
}

func (s *Service) contactPhoneRelation(ctx context.Context, actor Actor, value *PresaleRequest) (string, error) {
	// Auditor is an explicit deny and wins over any additional role or stale PMS
	// assignment attached to the same principal.
	if actor.HasRole("auditor") {
		return "", nil
	}
	if actor.HasRole("sales_director") || actor.HasRole("team_lead") || actor.HasRole("technical_lead") {
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
