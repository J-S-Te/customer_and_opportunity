package presale

import (
	"context"
	"errors"
	"testing"
	"time"
)

type reopenRepository struct {
	Repository
	request        *PresaleRequest
	approval       *ApprovalInstance
	requestFields  map[string]any
	approvalFields map[string]any
	statusLog      *StatusLog
}

func (r *reopenRepository) WithTransaction(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (r *reopenRepository) FindRequestForUpdate(context.Context, string, uint64) (*PresaleRequest, error) {
	copyValue := *r.request
	return &copyValue, nil
}

func (r *reopenRepository) FindApprovalInstanceForUpdate(context.Context, string, uint64) (*ApprovalInstance, error) {
	copyValue := *r.approval
	return &copyValue, nil
}

func (r *reopenRepository) UpdateRequestVersioned(_ context.Context, request *PresaleRequest, version uint64, fields map[string]any) error {
	if request.Version != version {
		return ErrVersionConflict
	}
	r.requestFields = fields
	request.Version++
	return nil
}

func (r *reopenRepository) UpdateApprovalInstance(_ context.Context, _ *ApprovalInstance, fields map[string]any) error {
	r.approvalFields = fields
	return nil
}

func (r *reopenRepository) CreateStatusLog(_ context.Context, value *StatusLog) error {
	copyValue := *value
	r.statusLog = &copyValue
	return nil
}

func validReopenInput() ReopenRequestInput {
	start := time.Date(2026, 8, 8, 2, 0, 0, 0, time.UTC)
	return ReopenRequestInput{
		Venue: VenueOnsite, ServiceAddress: " 新地址 ", ContactName: " 新联系人 ",
		ContactPhone: "13900000000", Description: "更新后的售前支持需求说明",
		ExpectedStart: start, ExpectedEnd: start.Add(2 * time.Hour), Urgency: UrgencyUrgent,
	}
}

func TestReopenRequestPersistsEditedFieldsAndKeepsIdentity(t *testing.T) {
	actor := Actor{TenantID: "tenant-a", UserID: "sales-a", Permissions: map[string]bool{"presale.create": true}, Roles: map[string]bool{}}
	repo := &reopenRepository{
		request:  &PresaleRequest{BaseModel: BaseModel{ID: 9, TenantID: actor.TenantID, Version: 3}, RequestNo: "TS202608080001", ApplicantID: actor.UserID, Status: StatusRejected},
		approval: &ApprovalInstance{BaseModel: BaseModel{ID: 21, TenantID: actor.TenantID, Version: 2}, RequestID: 9, Status: "REJECTED"},
	}
	service := NewService(repo, nil, testPhoneProtector{}, fixedClock{at: time.Date(2026, 8, 8, 1, 0, 0, 0, time.UTC)}, fixedIDs{})
	result, err := service.ReopenRequest(context.Background(), actor, 9, 3, validReopenInput())
	if err != nil {
		t.Fatal(err)
	}
	if result.ID != 9 || result.RequestNo != "TS202608080001" || result.Status != StatusPendingApproval || result.Version != 4 {
		t.Fatalf("unexpected reopened request: %#v", result)
	}
	if result.ContactName != "新联系人" || result.ServiceAddress != "新地址" || result.ContactPhoneMasked != "138****0000" || result.Urgency != UrgencyUrgent {
		t.Fatalf("edited fields were not projected: %#v", result)
	}
	if string(repo.requestFields["contact_phone_cipher"].([]byte)) != "cipher" || repo.requestFields["description"] != "更新后的售前支持需求说明" {
		t.Fatalf("edited fields were not persisted: %#v", repo.requestFields)
	}
	if repo.statusLog == nil || repo.statusLog.FromStatus != StatusRejected || repo.statusLog.ToStatus != StatusPendingApproval {
		t.Fatalf("unexpected status log: %#v", repo.statusLog)
	}
}

func TestReopenRequestRejectsInvalidEditBeforePersistence(t *testing.T) {
	actor := Actor{TenantID: "tenant-a", UserID: "sales-a", Permissions: map[string]bool{"presale.create": true}, Roles: map[string]bool{}}
	repo := &reopenRepository{}
	service := NewService(repo, nil, testPhoneProtector{}, fixedClock{}, fixedIDs{})
	input := validReopenInput()
	input.ContactPhone = ""
	if _, err := service.ReopenRequest(context.Background(), actor, 9, 3, input); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("error=%v", err)
	}
	if repo.requestFields != nil {
		t.Fatal("invalid edit reached persistence")
	}
}
