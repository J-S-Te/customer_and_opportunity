package filing

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	SectionOrganization         = "ORGANIZATION"
	SectionClassifiedObject     = "CLASSIFIED_OBJECT"
	SectionClassification       = "CLASSIFICATION"
	SectionNewTechnology        = "NEW_TECHNOLOGY"
	SectionMaterials            = "MATERIALS"
	SectionDataInventory        = "DATA_INVENTORY"
	SectionClassificationReport = "CLASSIFICATION_REPORT"
)

var SectionCodes = []string{
	SectionOrganization, SectionClassifiedObject, SectionClassification,
	SectionNewTechnology, SectionMaterials, SectionDataInventory,
	SectionClassificationReport,
}

const (
	MatrixBusinessInformation = "BUSINESS_INFORMATION"
	MatrixSystemService       = "SYSTEM_SERVICE"
)

var MatrixCodes = []string{MatrixBusinessInformation, MatrixSystemService}

var (
	creditCodePattern = regexp.MustCompile(`^[0-9A-Z]{18}$`)
	postalCodePattern = regexp.MustCompile(`^[0-9]{6}$`)
	divisionPattern   = regexp.MustCompile(`^[0-9]{6}$`)
	domainPattern     = regexp.MustCompile(`^([A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?\.)+[A-Za-z]{2,63}$`)
)

type fieldKind uint8

const (
	kindString fieldKind = iota
	kindBoolean
	kindInteger
	kindStringArray
)

type fieldRule struct {
	kind      fieldKind
	required  bool
	minLength int
	maxLength int
	minInt    int64
	maxInt    int64
	enum      map[string]struct{}
	format    string
}

type conditionRule struct {
	whenField string
	whenValue any
	require   []string
}

type sectionSchema struct {
	fields     map[string]fieldRule
	conditions []conditionRule
}

func stringRule(required bool, max int) fieldRule {
	min := 0
	if required {
		min = 1
	}
	return fieldRule{kind: kindString, required: required, minLength: min, maxLength: max}
}

func enumRule(required bool, values ...string) fieldRule {
	rule := stringRule(required, 64)
	rule.enum = enumSet(values...)
	return rule
}

func boolRule(required bool) fieldRule { return fieldRule{kind: kindBoolean, required: required} }
func intRule(required bool, min, max int64) fieldRule {
	return fieldRule{kind: kindInteger, required: required, minInt: min, maxInt: max}
}
func arrayRule(required bool, max int, values ...string) fieldRule {
	return fieldRule{kind: kindStringArray, required: required, maxInt: int64(max), enum: enumSet(values...)}
}
func enumSet(values ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

var filingSchemas = map[string]sectionSchema{
	SectionOrganization: {
		fields: map[string]fieldRule{
			"social_credit_code": withFormat(stringRule(true, 18), "credit_code"),
			"province":           stringRule(true, 64), "city": stringRule(true, 64), "district": stringRule(true, 64),
			"address": stringRule(true, 300), "postal_code": withFormat(stringRule(false, 6), "postal_code"),
			"administrative_division_code": withFormat(stringRule(false, 6), "division_code"),
			"organization_leader_name":     stringRule(true, 100), "organization_leader_title": stringRule(false, 100),
			"organization_leader_phone": stringRule(false, 50), "organization_leader_email": withFormat(stringRule(false, 200), "email"),
			"security_department": stringRule(true, 200), "security_contact_name": stringRule(true, 100),
			"security_contact_phone": stringRule(false, 50), "security_contact_email": withFormat(stringRule(false, 200), "email"),
			"data_security_department": stringRule(false, 200), "data_security_contact_name": stringRule(false, 100),
			"affiliation":         enumRule(true, "CENTRAL", "PROVINCE", "CITY", "COUNTY", "OTHER"),
			"organization_type":   enumRule(true, "PARTY_ORGAN", "GOVERNMENT", "PUBLIC_INSTITUTION", "ENTERPRISE", "OTHER"),
			"industry_code":       stringRule(true, 16),
			"level2_object_count": intRule(true, 0, 100000), "level3_object_count": intRule(true, 0, 100000),
			"level4_object_count": intRule(true, 0, 100000), "level5_object_count": intRule(true, 0, 100000),
		},
	},
	SectionClassifiedObject: {
		fields: map[string]fieldRule{
			"system_name": stringRule(true, 200), "object_number": stringRule(false, 64),
			"object_types":         arrayRule(true, 8, "COMMUNICATION_NETWORK", "INFORMATION_SYSTEM_CLOUD", "INFORMATION_SYSTEM_MOBILE", "INFORMATION_SYSTEM_IOT", "INFORMATION_SYSTEM_ICS", "INFORMATION_SYSTEM_BIG_DATA", "DATA_RESOURCE"),
			"business_type":        enumRule(true, "PRODUCTION", "COMMAND", "OFFICE", "PUBLIC_SERVICE", "OTHER"),
			"business_description": stringRule(true, 2000),
			"service_scope":        enumRule(true, "NATIONAL", "CROSS_PROVINCE", "PROVINCE", "CROSS_CITY", "CITY", "OTHER"),
			"service_audience":     enumRule(true, "INTERNAL", "PUBLIC", "BOTH", "OTHER"),
			"deployment_scope":     enumRule(true, "LAN", "MAN", "WAN", "OTHER"), "network_nature": enumRule(true, "PRIVATE", "INTERNET"),
			"source_ip_range": stringRule(false, 500), "domain_name": withFormat(stringRule(false, 253), "domain"),
			"protocols_and_ports": stringRule(false, 500), "interconnection": stringRule(false, 1000),
			"launched_on": withFormat(stringRule(true, 10), "date"), "is_subsystem": boolRule(true),
			"parent_system_name": stringRule(false, 200), "parent_organization_name": stringRule(false, 200),
		},
		conditions: []conditionRule{{whenField: "is_subsystem", whenValue: true, require: []string{"parent_system_name", "parent_organization_name"}}},
	},
	SectionClassification: {
		fields: map[string]fieldRule{
			"business_information_level": intRule(true, 1, 5), "system_service_level": intRule(true, 1, 5),
			"final_level": intRule(true, 1, 5), "classified_on": withFormat(stringRule(true, 10), "date"),
			"classification_report_available": boolRule(true), "expert_reviewed": boolRule(true),
			"has_industry_authority": boolRule(true), "industry_authority_name": stringRule(false, 200),
			"industry_authority_reviewed": boolRule(true), "form_filler_name": stringRule(true, 100),
			"form_filled_on": withFormat(stringRule(true, 10), "date"),
		},
		conditions: []conditionRule{{whenField: "has_industry_authority", whenValue: true, require: []string{"industry_authority_name"}}},
	},
	SectionNewTechnology: {
		fields: map[string]fieldRule{
			"cloud_used": boolRule(true), "cloud_responsibility": enumRule(false, "PROVIDER", "CUSTOMER"),
			"cloud_service_model":    enumRule(false, "IAAS", "PAAS", "SAAS", "OTHER"),
			"cloud_deployment_model": enumRule(false, "PRIVATE", "PUBLIC", "HYBRID", "OTHER"),
			"cloud_platform_name":    stringRule(false, 200), "cloud_platform_level": intRule(false, 1, 5), "cloud_filing_number": stringRule(false, 100),
			"mobile_used": boolRule(true), "mobile_application_names": stringRule(false, 1000),
			"iot_used": boolRule(true), "iot_components": arrayRule(false, 16, "SENSOR", "GATEWAY", "RFID_TAG", "RFID_READER", "INTERNET", "PRIVATE_NETWORK", "MOBILE_NETWORK"),
			"industrial_control_used": boolRule(true), "industrial_control_components": arrayRule(false, 16, "SCADA", "DCS", "PLC", "RTU", "MTU", "SC"),
			"big_data_used": boolRule(true), "big_data_components": arrayRule(false, 8, "PLATFORM", "APPLICATION", "RESOURCE"),
			"big_data_cross_border": boolRule(false), "big_data_application_count": intRule(false, 0, 100000), "big_data_platform_name": stringRule(false, 200),
		},
		conditions: []conditionRule{
			{whenField: "cloud_used", whenValue: true, require: []string{"cloud_responsibility", "cloud_service_model", "cloud_deployment_model", "cloud_platform_name", "cloud_platform_level"}},
			{whenField: "mobile_used", whenValue: true, require: []string{"mobile_application_names"}},
			{whenField: "iot_used", whenValue: true, require: []string{"iot_components"}},
			{whenField: "industrial_control_used", whenValue: true, require: []string{"industrial_control_components"}},
			{whenField: "big_data_used", whenValue: true, require: []string{"big_data_components", "big_data_cross_border", "big_data_application_count", "big_data_platform_name"}},
		},
	},
	SectionMaterials: {
		fields: map[string]fieldRule{
			"topology_available": boolRule(true), "topology_file_name": stringRule(false, 255),
			"security_governance_available": boolRule(true), "security_governance_file_name": stringRule(false, 255),
			"security_design_available": boolRule(true), "security_design_file_name": stringRule(false, 255),
			"security_products_available": boolRule(true), "security_products_file_name": stringRule(false, 255),
			"security_services_available": boolRule(true), "security_services_file_name": stringRule(false, 255),
			"authority_guidance_available": boolRule(true), "authority_guidance_file_name": stringRule(false, 255),
		},
		conditions: []conditionRule{
			{whenField: "topology_available", whenValue: true, require: []string{"topology_file_name"}},
			{whenField: "security_governance_available", whenValue: true, require: []string{"security_governance_file_name"}},
			{whenField: "security_design_available", whenValue: true, require: []string{"security_design_file_name"}},
			{whenField: "security_products_available", whenValue: true, require: []string{"security_products_file_name"}},
			{whenField: "security_services_available", whenValue: true, require: []string{"security_services_file_name"}},
			{whenField: "authority_guidance_available", whenValue: true, require: []string{"authority_guidance_file_name"}},
		},
	},
	SectionDataInventory: {
		fields: map[string]fieldRule{
			"data_name": stringRule(true, 200), "proposed_data_level": enumRule(true, "GENERAL", "IMPORTANT_OR_ABOVE"),
			"data_category": stringRule(true, 200), "responsible_department": stringRule(true, 200), "responsible_person": stringRule(true, 100),
			"personal_information_types": arrayRule(true, 8, "SENSITIVE", "MINOR", "GENERAL", "NONE"),
			"total_volume":               stringRule(false, 100), "monthly_growth": stringRule(false, 100),
			"data_sources":            arrayRule(true, 8, "COLLECTED", "GENERATED", "MANUAL", "PURCHASED", "SHARED"),
			"inter_organization_flow": stringRule(false, 2000),
			"processor_interaction":   enumRule(true, "PROVIDED", "ENTRUSTED", "JOINT", "NONE"),
			"storage_locations":       arrayRule(true, 8, "PRIVATE_CLOUD", "PUBLIC_CLOUD", "HYBRID_CLOUD", "OWN_DATA_CENTER", "DOMESTIC", "OVERSEAS"),
		},
	},
	SectionClassificationReport: {
		fields: map[string]fieldRule{
			"responsible_entity_description": stringRule(true, 5000), "object_composition_description": stringRule(true, 5000),
			"business_description": stringRule(true, 5000), "subsystems_summary": stringRule(false, 5000),
			"data_description": stringRule(true, 5000), "security_responsibility_description": stringRule(true, 5000),
			"business_information_description": stringRule(true, 5000), "business_impact_object": enumRule(true, "LEGAL_RIGHTS", "PUBLIC_INTEREST", "NATIONAL_SECURITY"),
			"business_damage_degree": enumRule(true, "GENERAL", "SERIOUS", "EXTREME"), "business_information_level": intRule(true, 1, 5),
			"system_service_description": stringRule(true, 5000), "service_impact_object": enumRule(true, "LEGAL_RIGHTS", "PUBLIC_INTEREST", "NATIONAL_SECURITY"),
			"service_damage_degree": enumRule(true, "GENERAL", "SERIOUS", "EXTREME"), "system_service_level": intRule(true, 1, 5),
			"final_level": intRule(true, 1, 5),
		},
	},
}

func withFormat(rule fieldRule, format string) fieldRule { rule.format = format; return rule }

func parseAndValidateSection(code string, raw []byte) ([]byte, []ValidationIssue, error) {
	schema, ok := filingSchemas[code]
	if !ok {
		return nil, nil, fmt.Errorf("unknown section code")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var data map[string]any
	if err := decoder.Decode(&data); err != nil || data == nil {
		return nil, nil, fmt.Errorf("section data must be a JSON object")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, nil, fmt.Errorf("section data must contain one JSON object")
	}
	issues := make([]ValidationIssue, 0)
	for key, value := range data {
		rule, exists := schema.fields[key]
		if !exists {
			return nil, nil, fmt.Errorf("unknown field %q", key)
		}
		if issue := validateField(key, value, rule); issue != nil {
			issues = append(issues, *issue)
		}
	}
	for key, rule := range schema.fields {
		if rule.required && missingValue(data[key], rule.kind) {
			issues = append(issues, requiredIssue(key))
		}
	}
	for _, condition := range schema.conditions {
		if equalJSONScalar(data[condition.whenField], condition.whenValue) {
			for _, key := range condition.require {
				if missingValue(data[key], schema.fields[key].kind) {
					issues = append(issues, requiredIssue(key))
				}
			}
		}
	}
	issues = append(issues, crossFieldIssues(code, data)...)
	canonical, err := json.Marshal(data)
	return canonical, deduplicateIssues(issues), err
}

func validateField(key string, value any, rule fieldRule) *ValidationIssue {
	switch rule.kind {
	case kindString:
		text, ok := value.(string)
		if !ok {
			return typeIssue(key, "string")
		}
		length := utf8.RuneCountInString(strings.TrimSpace(text))
		if length < rule.minLength || rule.maxLength > 0 && length > rule.maxLength {
			return rangeIssue(key)
		}
		if length > 0 && len(rule.enum) > 0 {
			if _, ok := rule.enum[text]; !ok {
				return enumIssue(key)
			}
		}
		if length > 0 && !validFormat(text, rule.format) {
			return formatIssue(key, rule.format)
		}
	case kindBoolean:
		if _, ok := value.(bool); !ok {
			return typeIssue(key, "boolean")
		}
	case kindInteger:
		number, ok := value.(json.Number)
		if !ok {
			return typeIssue(key, "integer")
		}
		integer, err := strconv.ParseInt(number.String(), 10, 64)
		if err != nil {
			return typeIssue(key, "integer")
		}
		if integer < rule.minInt || integer > rule.maxInt {
			return rangeIssue(key)
		}
	case kindStringArray:
		items, ok := value.([]any)
		if !ok {
			return typeIssue(key, "string_array")
		}
		if rule.required && len(items) == 0 || rule.maxInt > 0 && int64(len(items)) > rule.maxInt {
			return rangeIssue(key)
		}
		seen := map[string]struct{}{}
		for _, item := range items {
			text, ok := item.(string)
			if !ok || strings.TrimSpace(text) == "" {
				return typeIssue(key, "string_array")
			}
			if _, ok = rule.enum[text]; len(rule.enum) > 0 && !ok {
				return enumIssue(key)
			}
			if _, duplicate := seen[text]; duplicate {
				return &ValidationIssue{Path: key, Code: "DUPLICATE_ITEM", Message: "array values must be unique"}
			}
			seen[text] = struct{}{}
		}
	}
	return nil
}

func crossFieldIssues(code string, data map[string]any) []ValidationIssue {
	var issues []ValidationIssue
	if code == SectionClassification || code == SectionClassificationReport {
		business, bOK := integerValue(data["business_information_level"])
		service, sOK := integerValue(data["system_service_level"])
		final, fOK := integerValue(data["final_level"])
		if bOK && sOK && fOK && final != maxInt64(business, service) {
			issues = append(issues, ValidationIssue{Path: "final_level", Code: "LEVEL_MISMATCH", Message: "final level must equal the higher component level"})
		}
	}
	if code == SectionDataInventory {
		values, _ := data["personal_information_types"].([]any)
		if containsString(values, "NONE") && len(values) > 1 {
			issues = append(issues, ValidationIssue{Path: "personal_information_types", Code: "MUTUALLY_EXCLUSIVE", Message: "NONE cannot be combined with another personal-information type"})
		}
	}
	return issues
}

func validFormat(value, format string) bool {
	switch format {
	case "":
		return true
	case "credit_code":
		return creditCodePattern.MatchString(value)
	case "postal_code":
		return postalCodePattern.MatchString(value)
	case "division_code":
		return divisionPattern.MatchString(value)
	case "email":
		address, err := mail.ParseAddress(value)
		return err == nil && address.Name == "" && address.Address == value && !strings.ContainsAny(value, "\r\n")
	case "date":
		parsed, err := time.Parse("2006-01-02", value)
		return err == nil && parsed.Format("2006-01-02") == value
	case "domain":
		return domainPattern.MatchString(value)
	default:
		return false
	}
}

func isSectionCode(code string) bool { _, ok := filingSchemas[code]; return ok }
func isMatrixCode(code string) bool {
	return code == MatrixBusinessInformation || code == MatrixSystemService
}
func validMatrixCell(row, column string) bool {
	validRows := enumSet("LEGAL_RIGHTS", "PUBLIC_INTEREST", "NATIONAL_SECURITY")
	validColumns := enumSet("GENERAL_DAMAGE", "SERIOUS_DAMAGE", "EXTREME_DAMAGE")
	_, rowOK := validRows[row]
	_, columnOK := validColumns[column]
	return rowOK && columnOK
}
func requiredIssue(key string) ValidationIssue {
	return ValidationIssue{Path: key, Code: "REQUIRED", Message: "field is required"}
}
func typeIssue(key, expected string) *ValidationIssue {
	return &ValidationIssue{Path: key, Code: "TYPE", Message: "field must be " + expected}
}
func rangeIssue(key string) *ValidationIssue {
	return &ValidationIssue{Path: key, Code: "RANGE", Message: "field is outside its allowed range"}
}
func enumIssue(key string) *ValidationIssue {
	return &ValidationIssue{Path: key, Code: "ENUM", Message: "field contains an unsupported code"}
}
func formatIssue(key, format string) *ValidationIssue {
	return &ValidationIssue{Path: key, Code: "FORMAT", Message: "field has invalid " + format + " format"}
}
func missingValue(value any, kind fieldKind) bool {
	if value == nil {
		return true
	}
	if kind == kindString {
		text, ok := value.(string)
		return !ok || strings.TrimSpace(text) == ""
	}
	if kind == kindStringArray {
		list, ok := value.([]any)
		return !ok || len(list) == 0
	}
	return false
}
func equalJSONScalar(left, right any) bool { return fmt.Sprint(left) == fmt.Sprint(right) }
func integerValue(value any) (int64, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	result, err := number.Int64()
	return result, err == nil
}
func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
func containsString(values []any, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
func deduplicateIssues(values []ValidationIssue) []ValidationIssue {
	seen := map[string]struct{}{}
	result := make([]ValidationIssue, 0, len(values))
	for _, value := range values {
		key := value.Path + "\x00" + value.Code
		if _, ok := seen[key]; !ok {
			seen[key] = struct{}{}
			result = append(result, value)
		}
	}
	return result
}
