// Package migrationplan 固化 CRM 与 Portal 迁移文件的应用级顺序，但不执行 DDL；生产执行、
// 语句检查点和在线变更策略仍由发布平台负责。
package migrationplan

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type Schema string

const (
	CRM    Schema = "crm"
	Portal Schema = "portal"
)

type Entry struct {
	Position int    `json:"position"`
	File     string `json:"file"`
	SHA256   string `json:"sha256"`
	Bytes    int    `json:"bytes"`
}

type Plan struct {
	Schema  Schema  `json:"schema"`
	Entries []Entry `json:"entries"`
}

var baseFiles = map[Schema][]string{
	CRM: {
		"000001_create_shared.up.sql", "000002_create_customers.up.sql", "000003_create_opportunities.up.sql",
		"000004_create_portal_invites.up.sql", "000005_presale_requests_approval.up.sql", "000006_presale_assignment_progress.up.sql",
		"000007_presale_worklog_outbox.up.sql", "000009_crm_followups.up.sql", "000010_customer_change_logs.up.sql",
		"000012_presale_outbox_lease.up.sql", "000013_crm_oidc_sessions.up.sql", "000014_portal_invite_compensation.up.sql",
		"000015_crm_opportunity_lifecycle.up.sql", "000019_presale_alerts.up.sql", "000021_presale_reports.up.sql",
		"000023_opportunity_team.up.sql", "000025_portal_invite_compensation_worker.up.sql", "000026_opportunity_stage_alerts.up.sql",
		"000027_customer_merge.up.sql", "000029_opportunity_owner_notifications.up.sql", "000030_presale_engineer_sync.up.sql",
		"000032_customer_query_indexes.up.sql", "000033_customer_profile_children.up.sql", "000034_customer_imports.up.sql",
		"000035_presale_timeline_indexes.up.sql", "000036_presale_progress_idempotency.up.sql", "000037_presale_query_indexes.up.sql",
		"000040_presale_mutation_idempotency.up.sql", "000042_presale_assignment_notifications.up.sql", "000044_customer_create_idempotency.up.sql",
		"000045_opportunity_create_idempotency.up.sql", "000047_opportunity_external_edges.up.sql", "000048_contract_transfer_delivery.up.sql",
		"000049_opportunity_attachments.up.sql", "000051_presale_progress_notifications.up.sql", "000053_opportunity_member_terms.up.sql",
		"000055_crm_oidc_session_organizations.up.sql", "000058_crm_oidc_session_person_id.up.sql", "000060_presale_approval_pending_task_binding.up.sql", "000064_portal_invite_operation_idempotency.up.sql", "000065_presale_approval_callback_replay_binding.up.sql", "000067_presale_daily_metrics.up.sql", "000069_crm_portal_access_disable.up.sql", "000071_presale_alert_recipient_namespace.up.sql", "000072_presale_worker_heartbeats.up.sql", "000074_portal_invite_login_account.up.sql", "000075_crm_request_audit_outbox.up.sql", "000078_presale_approval_rules.up.sql",
		"000080_crm_session_data_scopes.up.sql", "000081_portal_identity_reconciliation.up.sql", "000082_opportunity_contract_link.up.sql", "000083_opportunity_type_source_multiselect.up.sql",
		"000084_presale_workflow_notifications.up.sql", "000094_add_user_login_ip_to_request_audit_outbox.up.sql", "000096_add_login_ip_to_crm_oidc_sessions.up.sql",
	},
	Portal: {
		"000008_create_portal_core.up.sql", "000011_portal_session_claims.up.sql", "000016_portal_project_sync.up.sql",
		"000017_portal_report_delivery.up.sql", "000018_portal_session_revalidation.up.sql", "000020_portal_machine_request_replays.up.sql",
		"000022_portal_account_security.up.sql", "000024_portal_feedback.up.sql", "000028_portal_service_evaluations.up.sql",
		"000031_portal_filing.up.sql", "000038_portal_report_status_events.up.sql", "000039_portal_report_download_grants.up.sql",
		"000041_portal_report_actor_columns.up.sql", "000043_portal_report_issued_notifications.up.sql", "000046_portal_project_exports.up.sql",
		"000050_portal_project_messages.up.sql", "000052_portal_project_message_read_receipts.up.sql", "000054_portal_project_message_keyset_reads.up.sql",
		"000056_portal_filing_materials_and_submission_outbox.up.sql", "000057_portal_report_file_security_evidence.up.sql", "000059_portal_report_scan_status_evidence.up.sql", "000061_portal_report_watermark_tracking.up.sql", "000062_portal_filing_waiting_contract_status.up.sql", "000063_portal_report_async_ingest.up.sql", "000066_portal_filing_submission_receipts.up.sql",
		"000068_portal_report_risk_operations.up.sql",
		"000070_portal_identity_disable_idempotency.up.sql",
		"000073_portal_worker_heartbeats.up.sql", "000076_portal_request_audit_outbox.up.sql", "000077_portal_customer_service_options.up.sql",
		"000079_portal_session_data_scopes.up.sql",
		"000095_add_user_login_ip_to_portal_request_audit_outbox.up.sql", "000097_add_login_ip_to_portal_sessions.up.sql",
	},
}

var migrationName = regexp.MustCompile(`^[0-9]{6}_[a-z0-9_]+\.up\.sql$`)

func Schemas() []Schema { return []Schema{CRM, Portal} }

func Files(schema Schema) ([]string, error) {
	files, ok := baseFiles[schema]
	if !ok {
		return nil, fmt.Errorf("unsupported migration schema %q", schema)
	}
	return append([]string(nil), files...), nil
}

// 读取已声明迁移，并在目录存在未归属的 up 文件时失败；每个新迁移必须先唯一分配给 CRM 或
// Portal，才能生成发布清单。
func Build(directory string, schema Schema) (Plan, error) {
	files, err := Files(schema)
	if err != nil {
		return Plan{}, err
	}
	if strings.TrimSpace(directory) == "" {
		return Plan{}, errors.New("migration directory is required")
	}
	declared, err := allDeclared()
	if err != nil {
		return Plan{}, err
	}
	directoryEntries, err := os.ReadDir(directory)
	if err != nil {
		return Plan{}, fmt.Errorf("read migration directory: %w", err)
	}
	for _, entry := range directoryEntries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".up.sql") {
			continue
		}
		if !migrationName.MatchString(entry.Name()) {
			return Plan{}, fmt.Errorf("migration file has an invalid name: %s", entry.Name())
		}
		if _, ok := declared[entry.Name()]; !ok {
			return Plan{}, fmt.Errorf("up migration is not assigned to CRM or Portal: %s", entry.Name())
		}
	}

	plan := Plan{Schema: schema, Entries: make([]Entry, 0, len(files))}
	for index, name := range files {
		contents, readErr := os.ReadFile(filepath.Join(directory, name))
		if readErr != nil {
			return Plan{}, fmt.Errorf("read migration %s: %w", name, readErr)
		}
		if len(contents) == 0 {
			return Plan{}, fmt.Errorf("migration is empty: %s", name)
		}
		digest := sha256.Sum256(contents)
		plan.Entries = append(plan.Entries, Entry{Position: index + 1, File: name, SHA256: "sha256:" + hex.EncodeToString(digest[:]), Bytes: len(contents)})
	}
	return plan, nil
}

func allDeclared() (map[string]Schema, error) {
	declared := make(map[string]Schema)
	for _, schema := range Schemas() {
		files := baseFiles[schema]
		last := ""
		for _, name := range files {
			if !migrationName.MatchString(name) {
				return nil, fmt.Errorf("invalid declared migration name: %s", name)
			}
			if last != "" && name <= last {
				return nil, fmt.Errorf("%s migrations are not strictly ordered: %s", schema, name)
			}
			if owner, duplicate := declared[name]; duplicate {
				return nil, fmt.Errorf("migration %s belongs to both %s and %s", name, owner, schema)
			}
			declared[name] = schema
			last = name
		}
	}
	return declared, nil
}

// 组合校验和同时绑定 schema、顺序、文件名、大小和单文件摘要，防止仅内容相同却调换执行顺序。
func CombinedChecksum(plan Plan) string {
	lines := make([]string, 0, len(plan.Entries)+1)
	lines = append(lines, string(plan.Schema))
	for _, entry := range plan.Entries {
		lines = append(lines, fmt.Sprintf("%d\x00%s\x00%d\x00%s", entry.Position, entry.File, entry.Bytes, entry.SHA256))
	}
	digest := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return "sha256:" + hex.EncodeToString(digest[:])
}

// 返回跨 schema 的稳定排序视图，供诊断和测试核对，不代表实际执行顺序。
func DeclaredFiles() []string {
	declared, err := allDeclared()
	if err != nil {
		return nil
	}
	files := make([]string, 0, len(declared))
	for name := range declared {
		files = append(files, name)
	}
	sort.Strings(files)
	return files
}
