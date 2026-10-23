package reportsecurity

import (
	"context"
	"crypto/subtle"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/report"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"gorm.io/gorm"
)

type RiskEvidence interface {
	Issuance(context.Context, report.Actor, *report.Grant) (report.DownloadMetadata, error)
	RecentSuccesses(context.Context, report.Actor, uint64, time.Time) (int64, error)
}
type GORMRiskEvidence struct{ DB *gorm.DB }

func (e GORMRiskEvidence) Issuance(ctx context.Context, a report.Actor, g *report.Grant) (report.DownloadMetadata, error) {
	var event report.DownloadEvent
	if e.DB == nil || g == nil {
		return report.DownloadMetadata{}, ErrUnavailable
	}
	err := database.FromContext(ctx, e.DB).Where("tenant_id=? AND customer_id=? AND request_id=? AND account_id=? AND grant_id=? AND event_type='GRANT_ISSUED' AND result='SUCCESS'", a.TenantID, a.CustomerID, g.RequestID, a.AccountID, g.ID).Order("id DESC").Take(&event).Error
	if err != nil {
		return report.DownloadMetadata{}, ErrUnavailable
	}
	return report.DownloadMetadata{IPHash: event.IPHash, DeviceHash: event.DeviceHash}, nil
}
func (e GORMRiskEvidence) RecentSuccesses(ctx context.Context, a report.Actor, requestID uint64, since time.Time) (int64, error) {
	var count int64
	if e.DB == nil {
		return 0, ErrUnavailable
	}
	err := database.FromContext(ctx, e.DB).Model(&report.DownloadEvent{}).Where("tenant_id=? AND customer_id=? AND request_id=? AND account_id=? AND event_type='DOWNLOAD_SUCCEEDED' AND result='SUCCESS' AND occurred_at>=?", a.TenantID, a.CustomerID, requestID, a.AccountID, since).Count(&count).Error
	return count, err
}

type AuditRiskPolicy struct {
	evidence RiskEvidence
	window   time.Duration
	limit    int64
	now      func() time.Time
}

func NewAuditRiskPolicy(evidence RiskEvidence, window time.Duration, limit int64) (*AuditRiskPolicy, error) {
	if evidence == nil || window < time.Minute || window > time.Hour || limit < 1 || limit > 100 {
		return nil, ErrInvalid
	}
	return &AuditRiskPolicy{evidence: evidence, window: window, limit: limit, now: func() time.Time { return time.Now().UTC() }}, nil
}
func (p *AuditRiskPolicy) Evaluate(ctx context.Context, a report.Actor, r *report.Request, g *report.Grant, m report.DownloadMetadata) (bool, string, error) {
	if p == nil || p.evidence == nil || r == nil || g == nil || r.TenantID != a.TenantID || r.CustomerID != a.CustomerID || g.TenantID != a.TenantID || g.CustomerID != a.CustomerID || g.AccountID != a.AccountID || g.RequestID != r.ID {
		return false, "", ErrInvalid
	}
	if !storeIDPattern.MatchString(m.IPHash) || !storeIDPattern.MatchString(m.DeviceHash) {
		return false, "", ErrInvalid
	}
	baseline, err := p.evidence.Issuance(ctx, a, g)
	if err != nil {
		return false, "", err
	}
	if !storeIDPattern.MatchString(baseline.IPHash) || !storeIDPattern.MatchString(baseline.DeviceHash) {
		return false, "", ErrInvalid
	}
	// 这些哈希来自服务端观察到的直接对端及 User-Agent，
	// 仅是有界的会话及授权指纹，不代表 GeoIP 判定。
	if subtle.ConstantTimeCompare([]byte(baseline.DeviceHash), []byte(m.DeviceHash)) != 1 {
		return true, "DOWNLOAD_DEVICE_CHANGED", nil
	}
	if subtle.ConstantTimeCompare([]byte(baseline.IPHash), []byte(m.IPHash)) != 1 {
		return true, "DOWNLOAD_NETWORK_CHANGED", nil
	}
	count, err := p.evidence.RecentSuccesses(ctx, a, r.ID, p.now().Add(-p.window))
	if err != nil {
		return false, "", err
	}
	if count >= p.limit {
		return true, "DOWNLOAD_RATE_EXCEEDED", nil
	}
	return false, "", nil
}
