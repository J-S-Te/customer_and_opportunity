package presaleengineersyncworker

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
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/integrationhttp"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

type SourceEngineer struct {
	PersonID   string
	PersonName string
	Department string
	Role       string
	SkillTags  []string
	Contact    string
	ValidFlag  bool
	SyncedAt   time.Time
}

type SourceSnapshot struct {
	TenantID  string
	Full      bool
	Revision  time.Time
	Engineers []SourceEngineer
}

type EngineerSource interface {
	Fetch(context.Context, string) (SourceSnapshot, error)
}

type HTTPSource struct {
	client      *http.Client
	endpoint    string
	now         func() time.Time
	nonceReader io.Reader
}

func NewHTTPSource(cfg Config) (*HTTPSource, error) {
	transport, err := integrationhttp.NewTransport(cfg.TLS, 2*time.Second)
	if err != nil {
		return nil, err
	}
	tokenClient := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: rejectPMSRedirect}
	cc := clientcredentials.Config{ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, TokenURL: cfg.TokenURL, Scopes: []string{cfg.Scope}, AuthStyle: oauth2.AuthStyleInHeader}
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, tokenClient)
	client := cc.Client(ctx)
	client.Timeout = 10 * time.Second
	client.CheckRedirect = rejectPMSRedirect
	return &HTTPSource{client: client, endpoint: cfg.Endpoint, now: time.Now, nonceReader: rand.Reader}, nil
}

func (s *HTTPSource) Fetch(ctx context.Context, tenant string) (SourceSnapshot, error) {
	// PMS 是共享技术人员池，租户边界只来自本地持久化任务；请求不发送 tenant，响应也不能声明或改变 tenant。
	endpoint, err := url.Parse(s.endpoint)
	if err != nil {
		return SourceSnapshot{}, errors.New("invalid PMS endpoint")
	}
	query := endpoint.Query()
	query.Set("scope", "tech")
	endpoint.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return SourceSnapshot{}, err
	}
	req.Header.Set("Accept", "application/json")
	now := s.now
	if now == nil {
		now = time.Now
	}
	nonceReader := s.nonceReader
	if nonceReader == nil {
		nonceReader = rand.Reader
	}
	nonce := make([]byte, 32)
	if _, err = io.ReadFull(nonceReader, nonce); err != nil {
		return SourceSnapshot{}, errors.New("generate PMS technician request nonce failed")
	}
	req.Header.Set("X-Integration-Timestamp", now().UTC().Format(time.RFC3339Nano))
	req.Header.Set("X-Integration-Nonce", base64.RawURLEncoding.EncodeToString(nonce))
	resp, err := s.client.Do(req)
	if err != nil {
		return SourceSnapshot{}, safeTransportError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return SourceSnapshot{}, fmt.Errorf("PMS technician response status=%d", resp.StatusCode)
	}
	if !pmsJSONContentType(resp.Header.Get("Content-Type")) {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return SourceSnapshot{}, errors.New("invalid PMS technician response content type")
	}
	var payload struct {
		Technicians []struct {
			PersonID   string   `json:"personId"`
			PersonName string   `json:"personName"`
			Department string   `json:"department"`
			Role       string   `json:"role"`
			SkillTags  []string `json:"skillTags"`
			Contact    string   `json:"contact"`
			ValidFlag  *bool    `json:"validFlag"`
			SyncedAt   string   `json:"syncedAt"`
		} `json:"technicians"`
	}
	const maxPayloadBytes = 4 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPayloadBytes+1))
	if err != nil || len(body) > maxPayloadBytes {
		return SourceSnapshot{}, errors.New("PMS technician response exceeds size limit")
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&payload); err != nil {
		return SourceSnapshot{}, errors.New("invalid PMS technician response")
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return SourceSnapshot{}, errors.New("invalid trailing PMS technician response data")
	}
	if strings.TrimSpace(tenant) == "" {
		return SourceSnapshot{}, errors.New("local sync job tenant is empty")
	}
	result := SourceSnapshot{TenantID: tenant, Full: true, Engineers: make([]SourceEngineer, 0, len(payload.Technicians))}
	for _, value := range payload.Technicians {
		if value.ValidFlag == nil {
			return SourceSnapshot{}, errors.New("PMS technician response is missing validFlag")
		}
		syncedAt, parseErr := parseSourceTime(value.SyncedAt)
		if parseErr != nil {
			return SourceSnapshot{}, errors.New("PMS technician response contains invalid syncedAt")
		}
		if result.Revision.IsZero() || syncedAt.After(result.Revision) {
			result.Revision = syncedAt
		}
		skills := make([]string, len(value.SkillTags))
		for index := range value.SkillTags {
			skills[index] = strings.TrimSpace(value.SkillTags[index])
		}
		result.Engineers = append(result.Engineers, SourceEngineer{PersonID: strings.TrimSpace(value.PersonID), PersonName: strings.TrimSpace(value.PersonName), Department: strings.TrimSpace(value.Department), Role: strings.TrimSpace(value.Role), SkillTags: skills, Contact: strings.TrimSpace(value.Contact), ValidFlag: *value.ValidFlag, SyncedAt: syncedAt})
	}
	// 固定 OAuth 客户端和固定端点共同确定权威来源，返回快照只绑定到调用方传入的本地租户。
	return result, nil
}

func rejectPMSRedirect(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }

func pmsJSONContentType(value string) bool {
	mediaType := strings.TrimSpace(strings.SplitN(value, ";", 2)[0])
	return strings.EqualFold(mediaType, "application/json")
}

func parseSourceTime(value string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05"} {
		parsed, err := time.Parse(layout, strings.TrimSpace(value))
		if err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, errors.New("invalid source time")
}

func safeTransportError(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("PMS technician request timed out")
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) && networkErr.Timeout() {
		return errors.New("PMS technician request timed out")
	}
	return errors.New("PMS technician transport failed")
}

func normalizedRole(value string) (string, bool) {
	switch strings.TrimSpace(value) {
	case "技术总监":
		return "technical_director", true
	case "团队负责人":
		return "team_lead", true
	case "项目经理":
		return "project_manager", true
	case "实施工程师":
		return "implementation_engineer", true
	default:
		return "", false
	}
}
