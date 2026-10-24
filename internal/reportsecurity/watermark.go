package reportsecurity

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/font"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/report"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portalreportreference"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"gorm.io/gorm"
)

type IdentitySource interface {
	Resolve(context.Context, string, uint64, string) (portalreportreference.Identity, error)
}
type IdentityHTTPClient struct {
	Endpoint string
	Client   *http.Client
	Token    func(context.Context) (string, error)
}

func (c *IdentityHTTPClient) Resolve(ctx context.Context, tenant string, customerID uint64, accountID string) (portalreportreference.Identity, error) {
	empty := portalreportreference.Identity{}
	if c == nil || c.Client == nil || c.Token == nil {
		return empty, ErrUnavailable
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return empty, ErrUnavailable
	}
	q := u.Query()
	q.Set("customer_id", strconv.FormatUint(customerID, 10))
	q.Set("account_id", accountID)
	u.RawQuery = q.Encode()
	token, err := c.Token(ctx)
	if err != nil || token == "" {
		return empty, ErrUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return empty, ErrUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+token)
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return empty, ErrUnavailable
	}
	req.Header.Set("X-Integration-Nonce", base64.RawURLEncoding.EncodeToString(nonce))
	req.Header.Set("X-Integration-Timestamp", time.Now().UTC().Format(time.RFC3339Nano))
	req.Header.Set("Accept", "application/json")
	res, err := c.Client.Do(req)
	if err != nil {
		return empty, ErrUnavailable
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return empty, ErrUnavailable
	}
	var envelope struct {
		Code string                         `json:"code"`
		Data portalreportreference.Identity `json:"data"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&envelope) != nil || envelope.Code != "OK" {
		return empty, ErrInvalid
	}
	identity := envelope.Data
	if identity.TenantID != tenant || identity.CustomerID != customerID || identity.AccountID != accountID || !safeText(identity.CustomerName, 200) || !safeText(identity.AccountMasked, 64) || !strings.Contains(identity.AccountMasked, "*") {
		return empty, ErrInvalid
	}
	return identity, nil
}

type RequestNumberSource interface {
	RequestNumber(context.Context, report.WatermarkContext) (string, error)
}
type GORMRequestNumbers struct{ DB *gorm.DB }

func (g GORMRequestNumbers) RequestNumber(ctx context.Context, c report.WatermarkContext) (string, error) {
	if g.DB == nil {
		return "", ErrUnavailable
	}
	var r report.Request
	err := database.FromContext(ctx, g.DB).Select("request_no").Where("tenant_id=? AND customer_id=? AND id=? AND status=? AND report_validity_status='ACTIVE' AND deleted_at IS NULL", c.TenantID, c.CustomerID, c.RequestID, report.StatusIssued).Take(&r).Error
	if err != nil || !safeText(r.RequestNo, 32) {
		return "", ErrInvalid
	}
	return r.RequestNo, nil
}

type PDFWatermarker struct {
	identity IdentitySource
	numbers  RequestNumberSource
	fontName string
	conf     *model.Configuration
}

func NewPDFWatermarker(configRoot, fontPath string, identity IdentitySource, numbers RequestNumberSource) (*PDFWatermarker, error) {
	if !filepath.IsAbs(configRoot) || !filepath.IsAbs(fontPath) || identity == nil || numbers == nil {
		return nil, ErrInvalid
	}
	if err := api.EnsureDefaultConfigAt(configRoot); err != nil {
		return nil, err
	}
	if err := api.InstallFonts([]string{fontPath}); err != nil {
		return nil, err
	}
	fontName := strings.TrimSuffix(filepath.Base(fontPath), filepath.Ext(fontPath))
	metrics, ok := font.UserFontMetrics[fontName]
	if !ok {
		return nil, ErrInvalid
	}
	for _, r := range "字体客户账号报告申请下载时间追踪" {
		if _, ok := metrics.Chars[uint32(r)]; !ok {
			return nil, ErrInvalid
		}
	}
	if _, err := api.TextWatermark("字体", "fontname:"+fontName+",points:9", true, false, types.POINTS); err != nil {
		return nil, err
	}
	return &PDFWatermarker{identity: identity, numbers: numbers, fontName: fontName, conf: api.LoadConfiguration()}, nil
}
func (w *PDFWatermarker) Apply(ctx context.Context, raw []byte, c report.WatermarkContext) ([]byte, error) {
	if w == nil || w.conf == nil || w.identity == nil || w.numbers == nil || c.TenantID == "" || c.CustomerID == 0 || c.RequestID == 0 || c.AccountID == "" || c.DownloadedAt.IsZero() || !safeText(c.TrackingCode, 64) {
		return nil, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	identity, err := w.identity.Resolve(ctx, c.TenantID, c.CustomerID, c.AccountID)
	if err != nil {
		return nil, err
	}
	if identity.TenantID != c.TenantID || identity.CustomerID != c.CustomerID || identity.AccountID != c.AccountID || !safeText(identity.CustomerName, 200) || !safeText(identity.AccountMasked, 64) || !strings.Contains(identity.AccountMasked, "*") {
		return nil, ErrInvalid
	}
	number, err := w.numbers.RequestNumber(ctx, c)
	if err != nil || !safeText(number, 32) {
		return nil, ErrInvalid
	}
	text := fmt.Sprintf("客户：%s\n账号：%s\n报告申请：%s\n下载时间：%s\n追踪：%s", wrapWatermarkText(identity.CustomerName, 28), wrapWatermarkText(identity.AccountMasked, 28), number, c.DownloadedAt.In(time.FixedZone("Asia/Shanghai", 8*60*60)).Format(time.RFC3339), c.TrackingCode)
	metrics, ok := font.UserFontMetrics[w.fontName]
	if !ok {
		return nil, ErrInvalid
	}
	for _, r := range text {
		if r == '\n' || r == '\r' {
			continue
		}
		if _, ok := metrics.Chars[uint32(r)]; !ok {
			return nil, ErrInvalid
		}
	}
	wm, err := api.TextWatermark(text, "fontname:"+w.fontName+",points:9,scale:1 abs,position:bc,offset:0 12,rotation:0,opacity:0.75", true, false, types.POINTS)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err = api.AddWatermarks(bytes.NewReader(raw), &out, nil, wm, w.conf); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if out.Len() == 0 || out.Len() > maxPDFBytes || api.Validate(bytes.NewReader(out.Bytes()), w.conf) != nil {
		return nil, ErrInvalid
	}
	return out.Bytes(), nil
}
func safeText(value string, max int) bool {
	if strings.TrimSpace(value) == "" || len([]rune(value)) > max {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func wrapWatermarkText(value string, width int) string {
	runes := []rune(value)
	var lines []string
	for len(runes) > width {
		lines = append(lines, string(runes[:width]))
		runes = runes[width:]
	}
	lines = append(lines, string(runes))
	return strings.Join(lines, "\n")
}
