package presale

import (
	"context"
	"fmt"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
)

func (r *GORMRepository) ReportSummary(ctx context.Context, scope ReportScope, query ReportQuery) (ReportSummary, error) {
	// 工时口径统一排除删除和作废记录，时间窗口采用 [From, To)；后续汇总、趋势和分布
	// 都复用同一范围构造器，避免同一筛选条件在不同图表中产生不同统计结果。
	worklogWhere, worklogArgs := reportWorklogWhere(scope, query, "w", "r", "o")
	result := ReportSummary{WorkHours: "0.00"}
	if err := database.FromContext(ctx, r.db).Raw(`SELECT
		COALESCE(CAST(SUM(w.work_hours) AS CHAR), '0.00') AS work_hours,
		COUNT(DISTINCT w.person_id) AS participant_count,
		COUNT(DISTINCT w.id) AS valid_worklog_count
		FROM crm_presale_worklogs w
		JOIN crm_presale_requests r ON r.tenant_id=w.tenant_id AND r.id=w.request_id
		JOIN crm_opportunities o ON o.tenant_id=r.tenant_id AND o.id=r.opportunity_id
		WHERE `+worklogWhere, worklogArgs...).Scan(&result).Error; err != nil {
		return ReportSummary{}, err
	}

	requestWhere, requestArgs := reportRequestWhere(scope, query, "r", "o")
	if err := database.FromContext(ctx, r.db).Raw(`SELECT COUNT(DISTINCT r.id)
		FROM crm_presale_status_logs l
		JOIN crm_presale_requests r ON r.tenant_id=l.tenant_id AND r.id=l.request_id
		JOIN crm_opportunities o ON o.tenant_id=r.tenant_id AND o.id=r.opportunity_id
		WHERE l.tenant_id=? AND l.trigger=? AND l.occurred_at>=? AND l.occurred_at<? AND `+requestWhere,
		append([]any{scope.TenantID, completionPolicy, query.From, query.To}, requestArgs...)...).Scan(&result.AutoCompletedTaskCount).Error; err != nil {
		return ReportSummary{}, err
	}

	// “覆盖商机”要求窗口结束前已存在售前申请，且在窗口开始前没有进入终态；因此它表示
	// 统计窗口内仍有效的售前覆盖，而不是历史上只要创建过申请就永久计入覆盖。
	opportunityWhere, opportunityArgs := reportOpportunityWhere(scope, query, "o")
	coverageWhere, coverageArgs := reportCoverageRequestWhere(scope, query, "coverage_request")
	if err := database.FromContext(ctx, r.db).Raw(`SELECT
		COUNT(DISTINCT o.id) AS active_opportunity_count,
		COUNT(DISTINCT CASE WHEN EXISTS (
			SELECT 1 FROM crm_presale_requests coverage_request
			WHERE coverage_request.tenant_id=o.tenant_id AND coverage_request.opportunity_id=o.id
			  AND coverage_request.deleted_at IS NULL AND coverage_request.created_at<?
			  AND NOT EXISTS (SELECT 1 FROM crm_presale_status_logs terminal_log
				WHERE terminal_log.tenant_id=coverage_request.tenant_id AND terminal_log.request_id=coverage_request.id
				AND terminal_log.to_status IN (?,?,?) AND terminal_log.occurred_at<?)
			  AND `+coverageWhere+`
		) THEN o.id END) AS covered_opportunity_count
		FROM crm_opportunities o
		WHERE o.tenant_id=? AND o.deleted_at IS NULL AND o.created_at<?
		  AND (o.opp_status=? OR (o.opp_status=? AND o.stage_changed_at>=?)
		       OR (o.opp_status=? AND o.end_date>=DATE(?))) AND `+opportunityWhere,
		append(append([]any{query.To, StatusCompleted, StatusRejected, StatusCancelled, query.From}, coverageArgs...), append([]any{scope.TenantID, query.To, "FOLLOWING", "CLOSED", query.From, "VOID", query.From}, opportunityArgs...)...)...).Scan(&result).Error; err != nil {
		return ReportSummary{}, err
	}

	if err := database.FromContext(ctx, r.db).Raw(`SELECT
		COUNT(DISTINCT w.id) AS pms_outbox_worklog_count,
		COUNT(DISTINCT CASE WHEN w.push_status=? THEN w.id END) AS pms_success_count
		FROM crm_outbox_events e
		JOIN crm_presale_worklogs w ON w.tenant_id=e.tenant_id AND e.aggregate_type=? AND e.aggregate_id=CAST(w.id AS CHAR)
		JOIN crm_presale_requests r ON r.tenant_id=w.tenant_id AND r.id=w.request_id
		JOIN crm_opportunities o ON o.tenant_id=r.tenant_id AND o.id=r.opportunity_id
		WHERE e.tenant_id=? AND e.event_type=? AND `+worklogWhere,
		append([]any{PushSuccess, "presale_worklog", scope.TenantID, "PRESALE_WORKLOG_CREATED"}, worklogArgs...)...).Scan(&result).Error; err != nil {
		return ReportSummary{}, err
	}
	return result, nil
}

func reportCoverageRequestWhere(scope ReportScope, query ReportQuery, request string) (string, []any) {
	// 全租户、组织和个人三类范围由上层可信主体解析后传入。个人范围只允许本人申请或
	// 以 PMS 人员身份参与的申请；人员筛选还必须落在同一 [From, To) 工时窗口内。
	where, args := "1=1", make([]any, 0, 4)
	if !scope.All && len(scope.OrganizationIDs) == 0 {
		where += ` AND (` + request + `.applicant_id=? OR EXISTS (SELECT 1 FROM crm_presale_assignments coverage_assignment
			WHERE coverage_assignment.tenant_id=` + request + `.tenant_id AND coverage_assignment.request_id=` + request + `.id
			AND coverage_assignment.assignee_id=? AND coverage_assignment.deleted_at IS NULL))`
		args = append(args, scope.UserID, scope.PersonID)
	}
	if query.PersonID != "" {
		where += ` AND EXISTS (SELECT 1 FROM crm_presale_worklogs coverage_person
			WHERE coverage_person.tenant_id=` + request + `.tenant_id AND coverage_person.request_id=` + request + `.id
			AND coverage_person.person_id=? AND coverage_person.deleted_at IS NULL AND coverage_person.voided_at IS NULL
			AND coverage_person.work_start>=? AND coverage_person.work_start<?)`
		args = append(args, query.PersonID, query.From, query.To)
	}
	return where, args
}

func (r *GORMRepository) ReportTrend(ctx context.Context, scope ReportScope, query ReportQuery) ([]ReportTrendPoint, error) {
	where, args := reportWorklogWhere(scope, query, "w", "r", "o")
	var values []ReportTrendPoint
	err := database.FromContext(ctx, r.db).Raw(`SELECT DATE_FORMAT(w.work_start,'%Y-%m-%d') AS date,
		COALESCE(CAST(SUM(w.work_hours) AS CHAR), '0.00') AS work_hours,
		COUNT(DISTINCT w.person_id) AS participant_count, COUNT(DISTINCT w.id) AS worklog_count
		FROM crm_presale_worklogs w
		JOIN crm_presale_requests r ON r.tenant_id=w.tenant_id AND r.id=w.request_id
		JOIN crm_opportunities o ON o.tenant_id=r.tenant_id AND o.id=r.opportunity_id
		WHERE `+where+` GROUP BY DATE(w.work_start) ORDER BY DATE(w.work_start)`, args...).Scan(&values).Error
	return values, err
}

func (r *GORMRepository) ReportDistribution(ctx context.Context, scope ReportScope, query ReportQuery, dimension string) ([]ReportDistributionRow, error) {
	where, args := reportWorklogWhere(scope, query, "w", "r", "o")
	// SELECT 和 GROUP BY 无法使用占位符，因此动态 SQL 只能从这组内部固定映射取值；
	// 外部 dimension 未命中白名单时直接拒绝，不能拼接到 SQL 中。
	selects := map[string]string{
		"PERSON": `w.person_id AS dimension_id, w.person_name_snapshot AS dimension_name,
			w.department_snapshot AS department`,
		"DEPARTMENT": `COALESCE(NULLIF(w.department_snapshot,''),'UNSPECIFIED') AS dimension_id,
			COALESCE(NULLIF(w.department_snapshot,''),'未记录部门') AS dimension_name, '' AS department`,
		"OPPORTUNITY": `CAST(o.id AS CHAR) AS dimension_id, o.name AS dimension_name, '' AS department`,
	}
	groups := map[string]string{
		"PERSON":      "w.person_id,w.person_name_snapshot,w.department_snapshot",
		"DEPARTMENT":  "w.department_snapshot",
		"OPPORTUNITY": "o.id,o.name",
	}
	selection, ok := selects[dimension]
	if !ok {
		return nil, ErrInvalidInput
	}
	var values []ReportDistributionRow
	err := database.FromContext(ctx, r.db).Raw(fmt.Sprintf(`SELECT %s,
		COALESCE(CAST(SUM(w.work_hours) AS CHAR), '0.00') AS work_hours,
		COUNT(DISTINCT w.person_id) AS participant_count,
		COUNT(DISTINCT r.id) AS request_count, COUNT(DISTINCT w.id) AS worklog_count
		FROM crm_presale_worklogs w
		JOIN crm_presale_requests r ON r.tenant_id=w.tenant_id AND r.id=w.request_id
		JOIN crm_opportunities o ON o.tenant_id=r.tenant_id AND o.id=r.opportunity_id
		WHERE %s GROUP BY %s ORDER BY SUM(w.work_hours) DESC,dimension_id ASC LIMIT 500`, selection, where, groups[dimension]), args...).Scan(&values).Error
	return values, err
}

func reportWorklogWhere(scope ReportScope, query ReportQuery, worklog, request, opportunity string) (string, []any) {
	// 表别名仅由本文件中的固定调用点传入；所有业务筛选值仍通过参数绑定，避免把组织、
	// 人员或商机标识直接拼入 SQL。
	where := worklog + ".tenant_id=? AND " + worklog + ".deleted_at IS NULL AND " + worklog + ".voided_at IS NULL AND " + worklog + ".work_start>=? AND " + worklog + ".work_start<?"
	args := []any{scope.TenantID, query.From, query.To}
	where, args = appendReportScope(where, args, scope, worklog, opportunity)
	where, args = appendReportFilters(where, args, query, worklog, request, opportunity)
	return where, args
}

func reportRequestWhere(scope ReportScope, query ReportQuery, request, opportunity string) (string, []any) {
	where := request + ".tenant_id=? AND " + request + ".deleted_at IS NULL"
	args := []any{scope.TenantID}
	// 组织范围以商机权威 owner_org_id 为准；个人范围同时覆盖 CRM 申请人和 PMS 当前执行人，
	// 不把前端提交的组织或人员标识当成授权依据。
	if scope.All {
	} else if len(scope.OrganizationIDs) > 0 {
		where += " AND " + opportunity + ".owner_org_id IN ?"
		args = append(args, scope.OrganizationIDs)
	} else {
		where += ` AND (` + request + `.applicant_id=? OR EXISTS (SELECT 1 FROM crm_presale_assignments scoped_assignment
			WHERE scoped_assignment.tenant_id=` + request + `.tenant_id AND scoped_assignment.request_id=` + request + `.id
			AND scoped_assignment.assignee_id=? AND scoped_assignment.deleted_at IS NULL))`
		args = append(args, scope.UserID, scope.PersonID)
	}
	where, args = appendReportFilters(where, args, query, "filtered_worklog", request, opportunity)
	return where, args
}

func reportOpportunityWhere(scope ReportScope, query ReportQuery, opportunity string) (string, []any) {
	// 商机分母也必须服从同一数据范围：个人只能看到其在窗口内有效参与的售前申请所覆盖
	// 的商机，不能因为报表查询绕过普通申请列表的资源边界。
	where, args := "1=1", make([]any, 0, 8)
	if scope.All {
	} else if len(scope.OrganizationIDs) > 0 {
		where += " AND " + opportunity + ".owner_org_id IN ?"
		args = append(args, scope.OrganizationIDs)
	} else {
		where += ` AND EXISTS (SELECT 1 FROM crm_presale_requests scoped_request
			WHERE scoped_request.tenant_id=` + opportunity + `.tenant_id AND scoped_request.opportunity_id=` + opportunity + `.id
			AND scoped_request.deleted_at IS NULL AND scoped_request.created_at<?
			AND NOT EXISTS (SELECT 1 FROM crm_presale_status_logs scoped_terminal
				WHERE scoped_terminal.tenant_id=scoped_request.tenant_id AND scoped_terminal.request_id=scoped_request.id
				AND scoped_terminal.to_status IN (?,?,?) AND scoped_terminal.occurred_at<?)
			AND (scoped_request.applicant_id=? OR EXISTS (SELECT 1 FROM crm_presale_assignments scoped_assignment
				WHERE scoped_assignment.tenant_id=scoped_request.tenant_id AND scoped_assignment.request_id=scoped_request.id
				AND scoped_assignment.assignee_id=? AND scoped_assignment.deleted_at IS NULL)))`
		args = append(args, query.To, StatusCompleted, StatusRejected, StatusCancelled, query.From, scope.UserID, scope.PersonID)
	}
	if query.OrganizationID != "" {
		where += " AND " + opportunity + ".owner_org_id=?"
		args = append(args, query.OrganizationID)
	}
	if query.OpportunityID != 0 {
		where += " AND " + opportunity + ".id=?"
		args = append(args, query.OpportunityID)
	}
	if query.PersonID != "" {
		where += ` AND EXISTS (SELECT 1 FROM crm_presale_requests person_request
			JOIN crm_presale_worklogs person_worklog ON person_worklog.tenant_id=person_request.tenant_id AND person_worklog.request_id=person_request.id
			WHERE person_request.tenant_id=` + opportunity + `.tenant_id AND person_request.opportunity_id=` + opportunity + `.id
			AND person_worklog.person_id=? AND person_worklog.deleted_at IS NULL AND person_worklog.voided_at IS NULL
			AND person_worklog.work_start>=? AND person_worklog.work_start<?)`
		args = append(args, query.PersonID, query.From, query.To)
	}
	return where, args
}

func appendReportScope(where string, args []any, scope ReportScope, worklog, opportunity string) (string, []any) {
	if scope.All {
		return where, args
	}
	if len(scope.OrganizationIDs) > 0 {
		return where + " AND " + opportunity + ".owner_org_id IN ?", append(args, scope.OrganizationIDs)
	}
	return where + " AND " + worklog + ".person_id=?", append(args, scope.PersonID)
}

func appendReportFilters(where string, args []any, query ReportQuery, worklog, request, opportunity string) (string, []any) {
	if query.OrganizationID != "" {
		where += " AND " + opportunity + ".owner_org_id=?"
		args = append(args, query.OrganizationID)
	}
	if query.PersonID != "" {
		if worklog == "filtered_worklog" {
			where += ` AND EXISTS (SELECT 1 FROM crm_presale_worklogs filtered_worklog
				WHERE filtered_worklog.tenant_id=` + request + `.tenant_id AND filtered_worklog.request_id=` + request + `.id
				AND filtered_worklog.person_id=? AND filtered_worklog.deleted_at IS NULL AND filtered_worklog.voided_at IS NULL
				AND filtered_worklog.work_start>=? AND filtered_worklog.work_start<?)`
			args = append(args, query.PersonID, query.From, query.To)
		} else {
			where += " AND " + worklog + ".person_id=?"
			args = append(args, query.PersonID)
		}
	}
	if query.OpportunityID != 0 {
		where += " AND " + opportunity + ".id=?"
		args = append(args, query.OpportunityID)
	}
	return where, args
}

var _ ReportRepository = (*GORMRepository)(nil)
