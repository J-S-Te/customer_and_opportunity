package portalprojectworker

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/integrationhttp"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

const maxProjectResponseBytes = 4 << 20

type httpSource struct {
	endpoint    string
	client      *http.Client
	now         func() time.Time
	nonceReader io.Reader
	pageSize    int
}

// newHTTPSource follows the base platform's deployed client_credentials
// contract: client_secret_basic, with only grant_type and scope in the form.
// It deliberately does not send a non-standard audience parameter.
func newHTTPSource(ctx context.Context, cfg Config) (*httpSource, error) {
	transport, err := integrationhttp.NewTransport(cfg.TLS, 3*time.Second)
	if err != nil {
		return nil, err
	}
	return newHTTPSourceWithTransport(ctx, cfg, transport), nil
}

func newHTTPSourceWithTransport(ctx context.Context, cfg Config, transport http.RoundTripper) *httpSource {
	tokenClient := &http.Client{Transport: transport, Timeout: cfg.TokenTimeout}
	tokenContext := context.WithValue(ctx, oauth2.HTTPClient, tokenClient)
	credentials := clientcredentials.Config{
		ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret,
		TokenURL: cfg.TokenURL, Scopes: []string{cfg.Scope}, AuthStyle: oauth2.AuthStyleInHeader,
	}
	client := &http.Client{Transport: &oauth2.Transport{Source: credentials.TokenSource(tokenContext), Base: transport}, Timeout: cfg.RequestTimeout}
	return &httpSource{endpoint: cfg.SnapshotsURL, client: client, now: time.Now, nonceReader: rand.Reader, pageSize: cfg.PageSize}
}

func (s *httpSource) changed(ctx context.Context, tenantID string, customerID uint64, cursor string) (sourcePage, error) {
	endpoint, err := url.Parse(s.endpoint)
	if err != nil {
		return sourcePage{}, errors.New("project snapshot endpoint is invalid")
	}
	query := endpoint.Query()
	query.Set("customerId", strconv.FormatUint(customerID, 10))
	query.Set("cursor", cursor)
	query.Set("limit", strconv.Itoa(s.pageSize))
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return sourcePage{}, errors.New("create project snapshot request failed")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-Tenant-ID", tenantID)
	request.Header.Set("X-Integration-Timestamp", s.now().UTC().Format(time.RFC3339Nano))
	nonce, err := randomNonce(s.nonceReader)
	if err != nil {
		return sourcePage{}, err
	}
	request.Header.Set("X-Integration-Nonce", nonce)
	response, err := s.client.Do(request)
	if err != nil {
		return sourcePage{}, safeIntegrationTransportError(err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		requestID := safeHeader(response.Header.Get("X-Request-ID"))
		if requestID != "" {
			return sourcePage{}, fmt.Errorf("project snapshot dependency returned HTTP %d request_id=%s", response.StatusCode, requestID)
		}
		return sourcePage{}, fmt.Errorf("project snapshot dependency returned HTTP %d", response.StatusCode)
	}
	limited := io.LimitReader(response.Body, maxProjectResponseBytes+1)
	raw, err := io.ReadAll(limited)
	if err != nil || len(raw) > maxProjectResponseBytes {
		return sourcePage{}, errors.New("project snapshot dependency returned an invalid response")
	}
	var envelope sourceEnvelope
	if err = json.Unmarshal(raw, &envelope); err != nil || envelope.Code != "OK" {
		return sourcePage{}, errors.New("project snapshot dependency returned an invalid response")
	}
	page, err := envelope.Data.toPage()
	if err != nil {
		return sourcePage{}, errors.New("project snapshot dependency returned an invalid response")
	}
	if page.HasMore && (page.NextCursor == "" || page.NextCursor == cursor) {
		return sourcePage{}, errors.New("project snapshot dependency returned a non-advancing cursor")
	}
	if cursor != "" && page.NextCursor == "" {
		return sourcePage{}, errors.New("project snapshot dependency discarded the stable cursor")
	}
	return page, nil
}

type sourceEnvelope struct {
	Code string             `json:"code"`
	Data projectPagePayload `json:"data"`
}

type projectPagePayload struct {
	Items      []projectPayload `json:"items"`
	NextCursor string           `json:"next_cursor"`
	HasMore    bool             `json:"has_more"`
}

type projectPayload struct {
	ProjectID              string             `json:"project_id"`
	ProjectName            string             `json:"project_name"`
	ContractNo             string             `json:"contract_no"`
	Status                 string             `json:"status"`
	ProgressPct            uint8              `json:"progress_pct"`
	CurrentStage           string             `json:"current_stage"`
	ExpectedEndDate        string             `json:"expected_end_date"`
	Delayed                bool               `json:"delayed"`
	ManagerName            string             `json:"manager_name_snapshot"`
	ManagerContactMasked   string             `json:"manager_contact_masked"`
	ManagerPortalAccountID string             `json:"manager_portal_account_id"`
	SourceUpdatedAt        string             `json:"source_updated_at"`
	RawVersion             string             `json:"raw_version"`
	Milestones             []milestonePayload `json:"milestones"`
	Activities             []activityPayload  `json:"activities"`
	Team                   []teamPayload      `json:"team"`
}
type milestonePayload struct {
	StageCode   string `json:"stage_code"`
	StageName   string `json:"stage_name"`
	Status      string `json:"status"`
	PlannedAt   string `json:"planned_at"`
	CompletedAt string `json:"completed_at"`
	SortNo      int    `json:"sort_no"`
}
type activityPayload struct {
	SourceActivityID string `json:"source_activity_id"`
	Type             string `json:"type"`
	Content          string `json:"content"`
	OccurredAt       string `json:"occurred_at"`
}
type teamPayload struct {
	PersonRef     string `json:"person_ref"`
	Name          string `json:"name_snapshot"`
	Role          string `json:"role"`
	ContactMasked string `json:"contact_masked"`
}

func (p projectPagePayload) toPage() (sourcePage, error) {
	if len(p.NextCursor) > 1024 {
		return sourcePage{}, errors.New("cursor is too long")
	}
	result := sourcePage{Bundles: make([]sourceBundle, 0, len(p.Items)), NextCursor: p.NextCursor, HasMore: p.HasMore}
	seen := make(map[string]struct{}, len(p.Items))
	for i := range p.Items {
		bundle, err := p.Items[i].toBundle()
		if err != nil {
			return sourcePage{}, err
		}
		if _, exists := seen[bundle.ProjectID]; exists {
			return sourcePage{}, errors.New("duplicate project in page")
		}
		seen[bundle.ProjectID] = struct{}{}
		result.Bundles = append(result.Bundles, bundle)
	}
	return result, nil
}

func (p projectPayload) toBundle() (sourceBundle, error) {
	if !validRequired(p.ProjectID, 64) || !validRequired(p.ProjectName, 200) || !validRequired(p.Status, 32) || !validRequired(p.CurrentStage, 64) || !validRequired(p.RawVersion, 64) || p.ProgressPct > 100 || len([]rune(p.ContractNo)) > 64 || len([]rune(p.ManagerName)) > 128 || len([]rune(p.ManagerContactMasked)) > 128 || !validOptionalOpaque(p.ManagerPortalAccountID, 128) || len(p.Milestones) != 5 {
		return sourceBundle{}, errors.New("invalid project snapshot")
	}
	updatedAt, err := parseRequiredTime(p.SourceUpdatedAt)
	if err != nil {
		return sourceBundle{}, err
	}
	expectedEndDate, err := parseDate(p.ExpectedEndDate)
	if err != nil {
		return sourceBundle{}, err
	}
	result := sourceBundle{ProjectID: p.ProjectID, ProjectName: p.ProjectName, ContractNo: p.ContractNo, Status: p.Status, ProgressPct: p.ProgressPct, CurrentStage: p.CurrentStage, ExpectedEndDate: expectedEndDate, Delayed: p.Delayed, ManagerName: p.ManagerName, ManagerContactMasked: p.ManagerContactMasked, ManagerPortalAccountID: p.ManagerPortalAccountID, SourceUpdatedAt: updatedAt, RawVersion: p.RawVersion}
	for _, item := range p.Milestones {
		if !validRequired(item.StageCode, 64) || !validRequired(item.StageName, 128) || !validRequired(item.Status, 32) || item.SortNo < 0 {
			return sourceBundle{}, errors.New("invalid project milestone")
		}
		plannedAt, err := parseOptionalTime(item.PlannedAt)
		if err != nil {
			return sourceBundle{}, err
		}
		completedAt, err := parseOptionalTime(item.CompletedAt)
		if err != nil {
			return sourceBundle{}, err
		}
		result.Milestones = append(result.Milestones, sourceMilestone{StageCode: item.StageCode, StageName: item.StageName, Status: item.Status, PlannedAt: plannedAt, CompletedAt: completedAt, SortNo: item.SortNo})
	}
	activityIDs := make(map[string]struct{}, len(p.Activities))
	for _, item := range p.Activities {
		if !validRequired(item.SourceActivityID, 64) || !validRequired(item.Type, 32) || strings.TrimSpace(item.Content) == "" {
			return sourceBundle{}, errors.New("invalid project activity")
		}
		if _, exists := activityIDs[item.SourceActivityID]; exists {
			return sourceBundle{}, errors.New("duplicate project activity")
		}
		activityIDs[item.SourceActivityID] = struct{}{}
		occurredAt, err := parseRequiredTime(item.OccurredAt)
		if err != nil {
			return sourceBundle{}, err
		}
		result.Activities = append(result.Activities, sourceActivity{SourceActivityID: item.SourceActivityID, Type: item.Type, Content: item.Content, OccurredAt: occurredAt})
	}
	for _, item := range p.Team {
		if !validRequired(item.PersonRef, 64) || !validRequired(item.Name, 128) || !validRequired(item.Role, 64) || len(item.ContactMasked) > 128 {
			return sourceBundle{}, errors.New("invalid project team member")
		}
		result.Team = append(result.Team, sourceTeamMember{PersonRef: item.PersonRef, Name: item.Name, Role: item.Role, ContactMasked: item.ContactMasked})
	}
	return result, nil
}

func parseRequiredTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.IsZero() {
		return time.Time{}, errors.New("invalid project source timestamp")
	}
	return parsed.UTC(), nil
}
func parseOptionalTime(value string) (*time.Time, error) {
	if value == "" {
		return nil, nil
	}
	parsed, err := parseRequiredTime(value)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}
func parseDate(value string) (*time.Time, error) {
	if value == "" {
		return nil, nil
	}
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		return nil, errors.New("invalid project expected end date")
	}
	return &parsed, nil
}
func validRequired(value string, max int) bool {
	return strings.TrimSpace(value) != "" && value == strings.TrimSpace(value) && len([]rune(value)) <= max
}
func validOptionalOpaque(value string, max int) bool {
	return value == "" || value == strings.TrimSpace(value) && len([]rune(value)) <= max
}
func randomNonce(reader io.Reader) (string, error) {
	raw := make([]byte, 32)
	if _, err := io.ReadFull(reader, raw); err != nil {
		return "", errors.New("generate project snapshot nonce failed")
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
func safeIntegrationTransportError(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("project snapshot request timed out")
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return errors.New("project snapshot request timed out")
	}
	return errors.New("project snapshot transport failed")
}
func safeHeader(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 128 {
		return ""
	}
	for _, r := range value {
		if !(r == '-' || r == '_' || r == '.' || r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z') {
			return ""
		}
	}
	return value
}
