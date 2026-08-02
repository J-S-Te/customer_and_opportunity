package presale

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/audit"
)

type contactPhoneProtector struct {
	plaintext    string
	decryptErr   error
	decryptCalls int
}

func (*contactPhoneProtector) Encrypt(context.Context, string) ([]byte, error) {
	return []byte("cipher"), nil
}

func (p *contactPhoneProtector) Decrypt(context.Context, []byte) (string, error) {
	p.decryptCalls++
	if p.decryptErr != nil {
		return "", p.decryptErr
	}
	return p.plaintext, nil
}

func (*contactPhoneProtector) Mask(value string) string {
	value = strings.TrimSpace(value)
	if len(value) < 7 {
		return "****"
	}
	return value[:3] + "****" + value[len(value)-4:]
}

type contactPhoneAuditWriter struct {
	events []audit.Event
	err    error
}

func (w *contactPhoneAuditWriter) Write(_ context.Context, event audit.Event) error {
	if w.err != nil {
		return w.err
	}
	w.events = append(w.events, event)
	return nil
}

func contactPhoneRequest() *PresaleRequest {
	return &PresaleRequest{
		BaseModel:   BaseModel{ID: 7, TenantID: "tenant-1", Version: 1},
		ApplicantID: "sales-owner", Status: StatusExecuting,
		ContactPhoneCipher: []byte("authenticated-ciphertext"), ContactPhoneMasked: "138****0000",
	}
}

func contactPhoneActor(personID string) Actor {
	return Actor{
		TenantID: "tenant-1", UserID: "viewer-1", UserName: "Viewer", PersonID: personID,
		Permissions: map[string]bool{"presale.read": true, "presale.contact_phone.read": true}, Roles: map[string]bool{},
	}
}

func contactPhoneService(repo Repository) (*Service, *contactPhoneProtector, *contactPhoneAuditWriter) {
	protector := &contactPhoneProtector{plaintext: "13800000000"}
	writer := &contactPhoneAuditWriter{}
	return NewService(repo, nil, protector, fixedClock{}, fixedIDs{}).UseAuditWriter(writer), protector, writer
}

func TestContactPhoneAllowsCurrentAndHistoricalRealAssignees(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		isCurrent bool
	}{
		{name: "current", isCurrent: true},
		{name: "historical", isCurrent: false},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			repo := &queryRepository{request: contactPhoneRequest(), assignments: []Assignment{{AssigneeID: "person-1", IsCurrent: test.isCurrent}}}
			service, protector, writer := contactPhoneService(repo)
			value, err := service.ContactPhone(context.Background(), contactPhoneActor("person-1"), repo.request.ID)
			if err != nil || value.ContactPhone != "13800000000" || protector.decryptCalls != 1 || len(writer.events) != 1 {
				t.Fatalf("ContactPhone() value=%+v error=%v decrypts=%d events=%d", value, err, protector.decryptCalls, len(writer.events))
			}
			encoded, marshalErr := json.Marshal(writer.events[0])
			if marshalErr != nil || strings.Contains(string(encoded), value.ContactPhone) || writer.events[0].Operation != "CONTACT_PHONE_VIEW" {
				t.Fatalf("unsafe or invalid audit event: %s error=%v", encoded, marshalErr)
			}
		})
	}
}

func TestContactPhoneAllowsOnlyExplicitManagerRoles(t *testing.T) {
	t.Parallel()
	for _, role := range []string{"sales_director", "team_lead", "technical_lead"} {
		role := role
		t.Run(role, func(t *testing.T) {
			t.Parallel()
			repo := &queryRepository{request: contactPhoneRequest()}
			service, _, writer := contactPhoneService(repo)
			actor := contactPhoneActor("")
			actor.Roles[role] = true
			if _, err := service.ContactPhone(context.Background(), actor, repo.request.ID); err != nil || len(writer.events) != 1 {
				t.Fatalf("ContactPhone() role=%s error=%v events=%d", role, err, len(writer.events))
			}
		})
	}
}

func TestContactPhoneRejectsCrossTenantGuessedIDUnassignedAndAuditor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		actor   Actor
		wantErr error
	}{
		{name: "cross tenant", actor: func() Actor { value := contactPhoneActor("person-1"); value.TenantID = "tenant-2"; return value }(), wantErr: ErrNotFound},
		{name: "guessed ID without assignment", actor: contactPhoneActor("unassigned"), wantErr: ErrForbidden},
		{name: "missing separate permission", actor: func() Actor {
			value := contactPhoneActor("person-1")
			delete(value.Permissions, "presale.contact_phone.read")
			return value
		}(), wantErr: ErrForbidden},
		{name: "auditor with assignment and manager role", actor: func() Actor {
			value := contactPhoneActor("person-1")
			value.Roles["auditor"], value.Roles["team_lead"] = true, true
			return value
		}(), wantErr: ErrForbidden},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			repo := &queryRepository{request: contactPhoneRequest(), assignments: []Assignment{{AssigneeID: "person-1", IsCurrent: true}}}
			service, protector, writer := contactPhoneService(repo)
			value, err := service.ContactPhone(context.Background(), test.actor, repo.request.ID)
			if !errors.Is(err, test.wantErr) || value.ContactPhone != "" || protector.decryptCalls != 0 || len(writer.events) != 0 {
				t.Fatalf("ContactPhone() value=%+v error=%v decrypts=%d events=%d", value, err, protector.decryptCalls, len(writer.events))
			}
		})
	}
}

func TestContactPhoneFailsClosedForAuditAndCiphertextFailures(t *testing.T) {
	t.Parallel()
	repo := &queryRepository{request: contactPhoneRequest(), assignments: []Assignment{{AssigneeID: "person-1", IsCurrent: true}}}
	for _, test := range []struct {
		name     string
		setup    func(*contactPhoneProtector, *contactPhoneAuditWriter)
		wantLogs int
	}{
		{name: "audit failure", setup: func(_ *contactPhoneProtector, writer *contactPhoneAuditWriter) {
			writer.err = errors.New("audit unavailable")
		}},
		{name: "damaged ciphertext", setup: func(protector *contactPhoneProtector, _ *contactPhoneAuditWriter) {
			protector.decryptErr = errors.New("cipher authentication failed")
		}},
		{name: "mask binding mismatch", setup: func(protector *contactPhoneProtector, _ *contactPhoneAuditWriter) {
			protector.plaintext = "13900000000"
		}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			service, protector, writer := contactPhoneService(repo)
			test.setup(protector, writer)
			value, err := service.ContactPhone(context.Background(), contactPhoneActor("person-1"), repo.request.ID)
			if !errors.Is(err, ErrContactPhoneUnavailable) || value.ContactPhone != "" || len(writer.events) != 0 {
				t.Fatalf("ContactPhone() value=%+v error=%v events=%d", value, err, len(writer.events))
			}
		})
	}
}

func TestContactPhoneHTTPResponseIsNeverCacheable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &queryRepository{request: contactPhoneRequest(), assignments: []Assignment{{AssigneeID: "person-1", IsCurrent: true}}}
	service, _, _ := contactPhoneService(repo)
	handler := NewHandler(service, nil, fixedHandlerActorResolver{actor: contactPhoneActor("person-1")})
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "id", Value: "7"}}
	ctx.Request = httptest.NewRequest(http.MethodGet, "/presale/requests/7/contact-phone", nil)
	handler.ContactPhone(ctx)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Cache-Control") != "no-store, private" || recorder.Header().Get("Pragma") != "no-cache" || !strings.Contains(recorder.Body.String(), "13800000000") {
		t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func TestRequestDetailDoesNotDecryptContactPhone(t *testing.T) {
	repo := &queryRepository{request: contactPhoneRequest(), assignments: []Assignment{{AssigneeID: "person-1", IsCurrent: true}}}
	service, protector, _ := contactPhoneService(repo)
	actor := contactPhoneActor("person-1")
	value, err := service.RequestDetail(context.Background(), actor, repo.request.ID)
	if err != nil || !value.CanViewContactPhone || value.Request.ContactPhoneMasked != "138****0000" || protector.decryptCalls != 0 {
		t.Fatalf("RequestDetail() value=%+v error=%v decrypts=%d", value, err, protector.decryptCalls)
	}
	encoded, marshalErr := json.Marshal(value)
	if marshalErr != nil || strings.Contains(string(encoded), "13800000000") || strings.Contains(string(encoded), "authenticated-ciphertext") {
		t.Fatalf("ordinary detail leaked contact phone: %s error=%v", encoded, marshalErr)
	}
}

func TestContactPhoneHTTPAuditFailureIsStandardizedAndNeverCacheable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &queryRepository{request: contactPhoneRequest(), assignments: []Assignment{{AssigneeID: "person-1", IsCurrent: true}}}
	service, _, writer := contactPhoneService(repo)
	writer.err = errors.New("audit unavailable")
	handler := NewHandler(service, nil, fixedHandlerActorResolver{actor: contactPhoneActor("person-1")})
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "id", Value: "7"}}
	ctx.Request = httptest.NewRequest(http.MethodGet, "/presale/requests/7/contact-phone", nil)
	handler.ContactPhone(ctx)
	if recorder.Code != http.StatusServiceUnavailable || recorder.Header().Get("Cache-Control") != "no-store, private" ||
		!strings.Contains(recorder.Body.String(), `"code":"CRM_PRESALE_CONTACT_PHONE_UNAVAILABLE"`) || strings.Contains(recorder.Body.String(), "13800000000") {
		t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func TestContactPhoneRouteUsesIndependentPermission(t *testing.T) {
	raw, err := os.ReadFile("routes.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.Contains(line, `"/requests/:id/contact-phone"`) {
			continue
		}
		if !strings.Contains(line, `RequirePermission("presale.contact_phone.read")`) || strings.Contains(line, `RequirePermission("presale.read")`) {
			t.Fatalf("contact-phone route is not independently protected: %s", line)
		}
		return
	}
	t.Fatal("contact-phone route is missing")
}

func TestContactPhoneNoStoreRunsBeforeAuthenticationRejection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(ContactPhoneNoStore())
	router.GET("/api/v1/presale/requests/:id/contact-phone", func(c *gin.Context) {
		c.JSON(http.StatusUnauthorized, map[string]string{"code": "COMMON_UNAUTHENTICATED"})
	})
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/presale/requests/7/contact-phone", nil))
	if recorder.Code != http.StatusUnauthorized || recorder.Header().Get("Cache-Control") != "no-store, private" || recorder.Header().Get("Pragma") != "no-cache" {
		t.Fatalf("status=%d headers=%v", recorder.Code, recorder.Header())
	}
}
