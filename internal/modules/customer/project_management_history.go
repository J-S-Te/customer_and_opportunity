package customer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	requestctx "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

const ProjectManagementHistoryScope = "crm.project_history.read"
const maxProjectManagementHistoryBytes = 512 << 10

type projectManagementHistoryReader struct {
	endpoint string
	client   *http.Client
}

type projectManagementHistoryItem struct {
	ProjectID       string    `json:"project_id"`
	ProjectName     string    `json:"project_name"`
	ContractNo      string    `json:"contract_no"`
	Status          string    `json:"status"`
	ProgressPct     uint8     `json:"progress_pct"`
	Due             string    `json:"due"`
	SourceUpdatedAt time.Time `json:"source_updated_at"`
}

type projectManagementHistoryPage struct {
	Items    []projectManagementHistoryItem `json:"items"`
	TenantID string                         `json:"tenant_id"`
	Customer string                         `json:"customer_id"`
	Page     int                            `json:"page"`
	PageSize int                            `json:"page_size"`
	Total    int64                          `json:"total"`
}

// projectHistoryHTTPData 输出当前来源的列表契约，直连摘要没有同步记录字段。
// 参数：page 是经过 CRM 访问控制和来源校验的分页。
// 返回值：供 HTTP 序列化的项目历史列表。
func projectHistoryHTTPData(page ProjectHistoryPage) any {
	if page.Source != "project_management" {
		return page
	}
	items := make([]projectManagementHistoryItem, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, projectManagementHistoryItem{
			ProjectID: item.ProjectID, ProjectName: item.ProjectName, ContractNo: item.ContractNo,
			Status: item.Status, ProgressPct: item.ProgressPct, Due: item.Due,
			SourceUpdatedAt: item.SourceUpdatedAt,
		})
	}
	return struct {
		Items    []projectManagementHistoryItem `json:"items"`
		Source   string                         `json:"source"`
		Page     int                            `json:"page"`
		PageSize int                            `json:"page_size"`
		Total    int64                          `json:"total"`
	}{Items: items, Source: page.Source, Page: page.Page, PageSize: page.PageSize, Total: page.Total}
}

// NewProjectManagementHistoryReader 创建仅用于 CRM 项目摘要读取的机器客户端。
// 参数：ctx 为客户端生命周期上下文，options 为项目端点和独立 OAuth 凭据。
// 返回值：项目历史读取器；配置无效时返回错误。
func NewProjectManagementHistoryReader(ctx context.Context, options ProjectHistoryReaderOptions) (ProjectHistoryReader, error) {
	if err := ValidateProjectManagementHistoryOptions(options); err != nil {
		return nil, err
	}
	transport := http.DefaultTransport
	if options.HTTPClient != nil && options.HTTPClient.Transport != nil {
		transport = options.HTTPClient.Transport
	}
	noRedirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	tokenClient := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: noRedirect}
	credentials := clientcredentials.Config{
		ClientID: options.ClientID, ClientSecret: options.ClientSecret, TokenURL: options.TokenURL,
		Scopes: []string{ProjectManagementHistoryScope}, AuthStyle: oauth2.AuthStyleInHeader,
	}
	tokenContext := context.WithValue(ctx, oauth2.HTTPClient, tokenClient)
	client := &http.Client{
		Transport: &oauth2.Transport{Source: credentials.TokenSource(tokenContext), Base: transport},
		Timeout:   10 * time.Second, CheckRedirect: noRedirect,
	}
	return &projectManagementHistoryReader{endpoint: strings.TrimRight(options.Endpoint, "/"), client: client}, nil
}

// ValidateProjectManagementHistoryOptions 校验启用直连需要的凭据和目标地址。
// 参数：options 为项目摘要端点、发令端点与专用客户端配置。
// 返回值：配置完整时为空；占位或非法配置返回错误。
func ValidateProjectManagementHistoryOptions(options ProjectHistoryReaderOptions) error {
	for key, value := range map[string]string{
		"CRM_PROJECT_HISTORY_CLIENT_ID": options.ClientID, "CRM_PROJECT_HISTORY_CLIENT_SECRET": options.ClientSecret,
	} {
		if cleaned := strings.TrimSpace(value); cleaned == "" || cleaned == "PENDING_ONBOARDING" {
			return fmt.Errorf("%s must be provisioned", key)
		}
	}
	if options.Scope != ProjectManagementHistoryScope {
		return fmt.Errorf("CRM_PROJECT_HISTORY_SCOPE must be %s", ProjectManagementHistoryScope)
	}
	for key, raw := range map[string]string{
		"CRM_PROJECT_HISTORY_URL": options.Endpoint, "CRM_PROJECT_HISTORY_TOKEN_URL": options.TokenURL,
	} {
		parsed, err := url.ParseRequestURI(raw)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
			parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return fmt.Errorf("%s must be a valid HTTP(S) URL without credentials, query or fragment", key)
		}
	}
	return nil
}

// ListCustomerProjects 读取当前客户的权威项目摘要，并核对源端确认的租户和客户。
// 参数：ctx 为 CRM 请求，tenantID/customerID 为已校验的业务范围，page/pageSize 为分页参数。
// 返回值：经校验的摘要分页；依赖失败或范围不符时返回错误。
func (r *projectManagementHistoryReader) ListCustomerProjects(
	ctx context.Context, tenantID string, customerID uint64, page, pageSize int,
) (ProjectHistoryPage, error) {
	if tenantID == "" || strings.TrimSpace(tenantID) != tenantID || customerID == 0 ||
		page < 1 || page > maxProjectHistoryPage || pageSize < 1 || pageSize > 100 {
		return ProjectHistoryPage{}, errors.New("invalid project management history request")
	}
	customer := strconv.FormatUint(customerID, 10)
	query := url.Values{"page": {strconv.Itoa(page)}, "page_size": {strconv.Itoa(pageSize)}}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		r.endpoint+"/"+customer+"/projects?"+query.Encode(), nil)
	if err != nil {
		return ProjectHistoryPage{}, errors.New("cannot create project history request")
	}
	request.Header.Set("Accept", "application/json")
	if requestID := requestctx.ID(ctx); requestID != "" {
		request.Header.Set("X-Request-ID", requestID)
	}
	value, err := r.readPage(request)
	if err != nil {
		return ProjectHistoryPage{}, err
	}
	if value.TenantID != tenantID || value.Customer != customer ||
		!validProjectHistoryPagination(len(value.Items), value.Items != nil,
			value.Page, value.PageSize, value.Total, page, pageSize) {
		return ProjectHistoryPage{}, errors.New("invalid project history scope or pagination")
	}
	offset := int64(page-1) * int64(pageSize)
	if int64(len(value.Items)) != max(0, min(int64(pageSize), value.Total-offset)) {
		return ProjectHistoryPage{}, errors.New("incomplete project history page")
	}
	return value.toCRMPage()
}

// readPage 从项目管理服务读取有界结果，保留 HTTP 失败语义。
// 参数：request 为当前客户摘要请求。
// 返回值：项目管理分页，响应无效、超限或请求失败时返回错误。
func (r *projectManagementHistoryReader) readPage(request *http.Request) (projectManagementHistoryPage, error) {
	response, err := r.client.Do(request)
	if err != nil {
		if request.Context().Err() != nil {
			return projectManagementHistoryPage{}, request.Context().Err()
		}
		return projectManagementHistoryPage{}, errors.New("project management history request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return projectManagementHistoryPage{}, fmt.Errorf("project management history returned HTTP %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxProjectManagementHistoryBytes+1))
	var body struct {
		Data projectManagementHistoryPage `json:"data"`
	}
	if err != nil || len(raw) > maxProjectManagementHistoryBytes || decodeProjectHistoryJSON(raw, &body) != nil {
		return projectManagementHistoryPage{}, errors.New("invalid project management history response")
	}
	return body.Data, nil
}

// toCRMPage 只传递 CRM 页签所需字段，不暴露机器确认的租户或客户标识。
// 参数：无，使用当前源端分页。
// 返回值：CRM 分页；重复项目或摘要字段非法时返回错误。
func (p projectManagementHistoryPage) toCRMPage() (ProjectHistoryPage, error) {
	items := make([]ProjectHistoryItem, 0, len(p.Items))
	seen := make(map[string]struct{}, len(p.Items))
	for _, item := range p.Items {
		if _, duplicate := seen[item.ProjectID]; duplicate ||
			!boundedRequired(item.ProjectID, 64) || !boundedRequired(item.ProjectName, 255) ||
			!boundedRequired(item.Status, 32) || !boundedOptional(item.ContractNo, 64) ||
			!boundedOptional(item.Due, 64) || item.ProgressPct > 100 || item.SourceUpdatedAt.IsZero() {
			return ProjectHistoryPage{}, errors.New("invalid project management summary")
		}
		seen[item.ProjectID] = struct{}{}
		items = append(items, ProjectHistoryItem{
			ProjectID: item.ProjectID, ProjectName: item.ProjectName, ContractNo: item.ContractNo,
			Status: item.Status, ProgressPct: item.ProgressPct, Due: item.Due,
			SourceUpdatedAt: item.SourceUpdatedAt,
		})
	}
	return ProjectHistoryPage{
		Source: "project_management", Items: items, Total: p.Total, Page: p.Page, PageSize: p.PageSize,
	}, nil
}
