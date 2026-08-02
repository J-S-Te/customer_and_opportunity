package presale

import (
	"context"
	"math/big"
	"strings"
	"time"
)

const maxReportPeriod = 366 * 24 * time.Hour

type ReportQuery struct {
	From           time.Time
	To             time.Time
	OrganizationID string
	PersonID       string
	OpportunityID  uint64
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
}

type ReportService struct {
	repo ReportRepository
}

func NewReportService(repo ReportRepository) *ReportService { return &ReportService{repo: repo} }

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

// RequestExport deliberately fails closed until object storage, a worker and a
// downloadable file lifecycle are configured. It never returns a fabricated
// job identifier or a URL that does not exist.
func (s *ReportService) RequestExport(actor Actor, query ReportQuery) error {
	_, _, err := validateReportAccess(actor, query)
	if err != nil {
		return err
	}
	return ErrReportExportUnavailable
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
		// Backward-compatible role fallback is only used when an older session did
		// not populate scope_mode. An explicit SELF scope never receives it.
		if actor.ScopeMode == "" && (actor.HasRole("technical_lead") || actor.HasRole("sales_director") || actor.HasRole("team_lead")) {
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
	// The selected rows can only make the numerator a subset of the denominator.
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
