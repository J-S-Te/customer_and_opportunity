package presale

import (
	"bytes"
	"context"
	"encoding/csv"
	"math/big"
	"strconv"
	"strings"
	"time"
)

const maxReportPeriod = 366 * 24 * time.Hour

type ReportQuery struct {
	From              time.Time
	To                time.Time
	OrganizationID    string
	PersonID          string
	OpportunityID     uint64
	distributionLimit int
}

type ReportScope struct {
	TenantID        string
	All             bool
	OrganizationIDs []string
	UserID          string
	PersonID        string
}

type ReportSummary struct {
	From                    time.Time `json:"from"`
	To                      time.Time `json:"to"`
	WorkHours               string    `json:"work_hours"`
	ParticipantCount        int64     `json:"participant_count"`
	ValidWorklogCount       int64     `json:"valid_worklog_count"`
	AutoCompletedTaskCount  int64     `json:"auto_completed_task_count"`
	CoveredOpportunityCount int64     `json:"covered_opportunity_count"`
	ActiveOpportunityCount  int64     `json:"active_opportunity_count"`
	OpportunityCoverageRate string    `json:"opportunity_coverage_rate_percent"`
	PMSSuccessCount         int64     `json:"pms_success_count"`
	PMSOutboxWorklogCount   int64     `json:"pms_outbox_worklog_count"`
	PMSSuccessRate          string    `json:"pms_success_rate_percent"`
}

type ReportTrendPoint struct {
	Date             string `json:"date"`
	WorkHours        string `json:"work_hours"`
	ParticipantCount int64  `json:"participant_count"`
	WorklogCount     int64  `json:"worklog_count"`
}

type ReportDistributionRow struct {
	DimensionID      string `json:"dimension_id"`
	DimensionName    string `json:"dimension_name"`
	Department       string `json:"department,omitempty"`
	WorkHours        string `json:"work_hours"`
	ParticipantCount int64  `json:"participant_count"`
	RequestCount     int64  `json:"request_count"`
	WorklogCount     int64  `json:"worklog_count"`
}

type ReportRepository interface {
	ReportSummary(context.Context, ReportScope, ReportQuery) (ReportSummary, error)
	ReportTrend(context.Context, ReportScope, ReportQuery) ([]ReportTrendPoint, error)
	ReportDistribution(context.Context, ReportScope, ReportQuery, string) ([]ReportDistributionRow, error)
	ReportFilterOptions(context.Context, ReportScope, ReportQuery) (RequestFilterOptions, error)
}

type ReportService struct {
	repo        ReportRepository
	exportSlots chan struct{}
}

func NewReportService(repo ReportRepository) *ReportService {
	return &ReportService{repo: repo, exportSlots: make(chan struct{}, 2)}
}

func (s *ReportService) Summary(ctx context.Context, actor Actor, query ReportQuery) (ReportSummary, error) {
	scope, query, err := validateReportAccess(actor, query)
	if err != nil {
		return ReportSummary{}, err
	}
	value, err := s.repo.ReportSummary(ctx, scope, query)
	if err != nil {
		return ReportSummary{}, err
	}
	value.From, value.To = query.From, query.To
	value.OpportunityCoverageRate = ratio(value.CoveredOpportunityCount, value.ActiveOpportunityCount)
	value.PMSSuccessRate = ratio(value.PMSSuccessCount, value.PMSOutboxWorklogCount)
	return value, nil
}

func (s *ReportService) Trend(ctx context.Context, actor Actor, query ReportQuery) ([]ReportTrendPoint, error) {
	scope, query, err := validateReportAccess(actor, query)
	if err != nil {
		return nil, err
	}
	values, err := s.repo.ReportTrend(ctx, scope, query)
	if err != nil {
		return nil, err
	}
	byDate := make(map[string]ReportTrendPoint, len(values))
	for _, value := range values {
		byDate[value.Date] = value
	}
	result := make([]ReportTrendPoint, 0, int(query.To.Sub(query.From)/(24*time.Hour))+1)
	// 数据库只返回有事实的日期；服务层补齐半开区间内的空日期，
	// 保证图表横轴连续且不会把“无数据”误解为接口漏行。
	for day := utcDay(query.From); day.Before(query.To); day = day.AddDate(0, 0, 1) {
		key := day.Format(time.DateOnly)
		value, ok := byDate[key]
		if !ok {
			value = ReportTrendPoint{Date: key, WorkHours: "0.00"}
		}
		result = append(result, value)
	}
	return result, nil
}

func (s *ReportService) Distribution(ctx context.Context, actor Actor, query ReportQuery, dimension string) ([]ReportDistributionRow, error) {
	scope, query, err := validateReportAccess(actor, query)
	if err != nil {
		return nil, err
	}
	dimension = strings.ToUpper(strings.TrimSpace(dimension))
	if dimension != "PERSON" && dimension != "DEPARTMENT" && dimension != "OPPORTUNITY" {
		return nil, ErrInvalidInput
	}
	return s.repo.ReportDistribution(ctx, scope, query, dimension)
}

func (s *ReportService) FilterOptions(ctx context.Context, actor Actor, query ReportQuery) (RequestFilterOptions, error) {
	scope, query, err := validateReportAccess(actor, query)
	if err != nil {
		return RequestFilterOptions{}, err
	}
	return s.repo.ReportFilterOptions(ctx, scope, query)
}

// 导出与列表使用相同的服务端授权范围。文件仅在当前请求中生成，不制造异步任务或
// 长期下载链接；超出上限时拒绝而不是悄悄截断报表。
func (s *ReportService) RequestExport(ctx context.Context, actor Actor, query ReportQuery, dimension string) ([]byte, error) {
	if s == nil || s.repo == nil {
		return nil, ErrReportExportUnavailable
	}
	if _, _, err := validateReportAccess(actor, query); err != nil {
		return nil, err
	}
	select {
	case s.exportSlots <- struct{}{}:
		defer func() { <-s.exportSlots }()
	default:
		return nil, ErrReportExportBusy
	}
	if transaction, ok := s.repo.(interface {
		WithTransaction(context.Context, func(context.Context) error) error
	}); ok {
		var data []byte
		err := transaction.WithTransaction(ctx, func(tx context.Context) error {
			var err error
			data, err = s.exportCSV(tx, actor, query, dimension)
			return err
		})
		return data, err
	}
	return s.exportCSV(ctx, actor, query, dimension)
}

func (s *ReportService) exportCSV(ctx context.Context, actor Actor, query ReportQuery, dimension string) ([]byte, error) {
	scope, query, err := validateReportAccess(actor, query)
	if err != nil {
		return nil, err
	}
	dimension = strings.ToUpper(strings.TrimSpace(dimension))
	if dimension == "" {
		dimension = "PERSON"
	}
	if dimension != "PERSON" && dimension != "DEPARTMENT" && dimension != "OPPORTUNITY" {
		return nil, ErrInvalidInput
	}
	query.distributionLimit = 10001
	summary, err := s.Summary(ctx, actor, query)
	if err != nil {
		return nil, err
	}
	trend, err := s.Trend(ctx, actor, query)
	if err != nil {
		return nil, err
	}
	rows, err := s.repo.ReportDistribution(ctx, scope, query, dimension)
	if err != nil {
		return nil, err
	}
	if len(rows) > 10000 {
		return nil, ErrReportExportLimit
	}
	var output bytes.Buffer
	output.WriteString("\xef\xbb\xbf")
	writer := csv.NewWriter(&output)
	records := [][]string{
		{"售前投入报表", "起始时间（UTC）", "结束时间（UTC）", "分布维度"},
		{"统计区间", query.From.Format(time.RFC3339), query.To.Format(time.RFC3339), dimension},
		{"汇总", "工时", "参与人数", "有效工时记录", "已结束任务", "覆盖商机", "有效商机", "商机覆盖率（%）", "工时成功记录", "工时总记录", "工时成功率（%）"},
		{"合计", summary.WorkHours, strconv.FormatInt(summary.ParticipantCount, 10), strconv.FormatInt(summary.ValidWorklogCount, 10), strconv.FormatInt(summary.AutoCompletedTaskCount, 10), strconv.FormatInt(summary.CoveredOpportunityCount, 10), strconv.FormatInt(summary.ActiveOpportunityCount, 10), summary.OpportunityCoverageRate, strconv.FormatInt(summary.PMSSuccessCount, 10), strconv.FormatInt(summary.PMSOutboxWorklogCount, 10), summary.PMSSuccessRate},
		{}, {"趋势日期（UTC）", "工时", "参与人数", "记录数"},
	}
	for _, point := range trend {
		records = append(records, []string{point.Date, point.WorkHours, strconv.FormatInt(point.ParticipantCount, 10), strconv.FormatInt(point.WorklogCount, 10)})
	}
	records = append(records, []string{}, []string{"分布", "名称", "部门", "工时", "参与人数", "申请数", "记录数"})
	for _, row := range rows {
		records = append(records, []string{dimension, row.DimensionName, row.Department, row.WorkHours, strconv.FormatInt(row.ParticipantCount, 10), strconv.FormatInt(row.RequestCount, 10), strconv.FormatInt(row.WorklogCount, 10)})
	}
	for _, record := range records {
		for i, cell := range record {
			// Excel 可将引号内的文本当作公式执行，检查前导空白后的首字符。
			if value := strings.TrimLeft(cell, " \t\r\n"); value != "" && strings.ContainsRune("=+-@", rune(value[0])) {
				record[i] = "'" + cell
			}
		}
		if err := writer.Write(record); err != nil {
			return nil, err
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, err
	}
	if output.Len() > 16<<20 {
		return nil, ErrReportExportLimit
	}
	return output.Bytes(), nil
}

func validateReportAccess(actor Actor, query ReportQuery) (ReportScope, ReportQuery, error) {
	if !actor.Can("presale.report") {
		return ReportScope{}, ReportQuery{}, ErrForbidden
	}
	query.From, query.To = query.From.UTC(), query.To.UTC()
	query.OrganizationID = strings.TrimSpace(query.OrganizationID)
	query.PersonID = strings.TrimSpace(query.PersonID)
	if query.From.IsZero() || query.To.IsZero() || !query.To.After(query.From) || query.To.Sub(query.From) > maxReportPeriod {
		return ReportScope{}, ReportQuery{}, ErrInvalidInput
	}
	scope := ReportScope{TenantID: actor.TenantID, UserID: actor.UserID, PersonID: actor.PersonID}
	switch strings.ToUpper(actor.ScopeMode) {
	case "ALL":
		scope.All = true
	case "ORG":
		scope.OrganizationIDs = uniqueNonEmpty(actor.OrganizationIDs)
		if len(scope.OrganizationIDs) == 0 {
			return ReportScope{}, ReportQuery{}, ErrForbidden
		}
		if query.OrganizationID != "" && !containsString(scope.OrganizationIDs, query.OrganizationID) {
			return ReportScope{}, ReportQuery{}, ErrForbidden
		}
	default:
		if strings.TrimSpace(scope.PersonID) == "" || strings.TrimSpace(scope.UserID) == "" {
			return ReportScope{}, ReportQuery{}, ErrForbidden
		}
		if query.PersonID != "" && query.PersonID != scope.PersonID {
			return ReportScope{}, ReportQuery{}, ErrForbidden
		}
		// 仅旧会话完全缺少 scope_mode 时才使用角色兼容逻辑；显式 SELF 范围不会被角色扩大。
		if actor.ScopeMode == "" && (actor.HasRole("technical_director") || actor.HasRole("sales_director") || actor.HasRole("team_lead")) {
			scope.All = true
		}
	}
	return scope, query, nil
}

func uniqueNonEmpty(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; !ok {
			seen[value] = struct{}{}
			result = append(result, value)
		}
	}
	return result
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func ratio(numerator, denominator int64) string {
	if denominator <= 0 || numerator <= 0 {
		return "0.00"
	}
	// 正常口径下分子应是分母子集；上限保护避免异常聚合数据产生超过 100% 的展示值。
	if numerator > denominator {
		numerator = denominator
	}
	percentage := new(big.Rat).SetFrac(big.NewInt(numerator), big.NewInt(denominator))
	percentage.Mul(percentage, big.NewRat(100, 1))
	return percentage.FloatString(2)
}

func utcDay(value time.Time) time.Time {
	value = value.UTC()
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.UTC)
}
