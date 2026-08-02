package customer

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/security"
)

func profileCodec(t *testing.T) *security.SensitiveCodec {
	t.Helper()
	codec, err := security.NewSensitiveCodec([]byte("0123456789abcdef0123456789abcdef"), []byte("abcdef0123456789abcdef0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	return codec
}

func stringPointer(value string) *string { return &value }

func TestBuildStakeholdersEncryptsSensitiveValuesAndResponsesAreMasked(t *testing.T) {
	service := &Service{codec: profileCodec(t)}
	phone, email := "13800136789", "chen@example.com"
	models, err := service.buildStakeholders("tenant-a", "user-a", 8, []StakeholderInput{{Name: "陈志远", RoleTitle: "信息技术部总监", Influence: "high", RelationshipSummary: "决策人", Phone: &phone, Email: &email}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || string(models[0].PhoneCipher) == phone || string(models[0].EmailCipher) == email || len(models[0].PhoneCipher) == 0 || len(models[0].EmailCipher) == 0 {
		t.Fatalf("sensitive fields not encrypted: %#v", models)
	}
	response := stakeholderResponses(models)
	encoded, _ := json.Marshal(response)
	if strings.Contains(string(encoded), phone) || strings.Contains(string(encoded), email) || response[0].PhoneMasked != "138****6789" || response[0].EmailMasked != "c***@example.com" {
		t.Fatalf("response leaked plaintext: %s", encoded)
	}
}

func TestBuildStakeholdersPreservesCipherOnlyByOwnedID(t *testing.T) {
	service := &Service{codec: profileCodec(t)}
	existing := []Stakeholder{{Model: database.Model{ID: 4}, PhoneCipher: []byte("cipher-phone"), PhoneMasked: "138****6789", EmailCipher: []byte("cipher-email"), EmailMasked: "c***@example.com"}}
	models, err := service.buildStakeholders("tenant-a", "user-a", 8, []StakeholderInput{{ID: 4, Name: "陈志远", RoleTitle: "总监", Influence: "HIGH"}}, existing)
	if err != nil || string(models[0].PhoneCipher) != "cipher-phone" || string(models[0].EmailCipher) != "cipher-email" {
		t.Fatalf("existing cipher was not preserved: models=%#v err=%v", models, err)
	}
	if _, err = service.buildStakeholders("tenant-a", "user-a", 8, []StakeholderInput{{ID: 99, Name: "陈志远", RoleTitle: "总监", Influence: "HIGH"}}, existing); err != ErrInvalidStakeholders {
		t.Fatalf("foreign child ID accepted: %v", err)
	}
}

func TestProfileValidationRejectsMaskedSentinelScriptAndInvalidEnums(t *testing.T) {
	phone := "138****6789"
	if err := validateStakeholderInputs([]StakeholderInput{{Name: "陈", RoleTitle: "总监", Influence: "HIGH", Phone: &phone}}); err != ErrInvalidStakeholders {
		t.Fatalf("masked phone accepted: %v", err)
	}
	if err := validateStakeholderInputs([]StakeholderInput{{Name: "<script>", RoleTitle: "总监", Influence: "HIGH"}}); err != ErrInvalidStakeholders {
		t.Fatalf("script accepted: %v", err)
	}
	badDate := "2026-02-30"
	if _, err := buildInformationSystems("tenant", "user", 1, []InformationSystemInput{{Name: "核心系统", ProtectionLevel: "CREDIT_A", FilingStatus: "FILED", GradingDate: &badDate}}); err != ErrInvalidSystems {
		t.Fatalf("credit-like level or invalid date accepted: %v", err)
	}
	validDate := "2026-03-12"
	models, err := buildInformationSystems("tenant", "user", 1, []InformationSystemInput{{Name: "核心系统", ProtectionLevel: "level_3", ApplicationScenario: "交易", FilingNo: "440300-00001", FilingStatus: "filed", GradingDate: &validDate}})
	if err != nil || models[0].ProtectionLevel != "LEVEL_3" || models[0].GradingDate.Format("2006-01-02") != validDate {
		t.Fatalf("valid system rejected: %#v err=%v", models, err)
	}
}

func TestInformationSystemResponseUsesDateOnly(t *testing.T) {
	date := time.Date(2026, 3, 12, 23, 0, 0, 0, time.FixedZone("CST", 8*3600))
	result := informationSystemResponses([]InformationSystem{{GradingDate: &date}})
	if result[0].GradingDate == nil || *result[0].GradingDate != "2026-03-12" {
		t.Fatalf("date semantics changed: %#v", result)
	}
}
