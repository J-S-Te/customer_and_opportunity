// Package notificationdeliveryworker 把 CRM 本地站内信双写投递到基础平台中央站内信。
// 投递是幂等的最佳努力：本地 crm_notifications 仍是权威收件箱，平台投递失败只记录日志并下次重试。
package notificationdeliveryworker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/notification"
)

type App struct {
	db     *gorm.DB
	config Config
	client *http.Client
	logger *slog.Logger
	now    func() time.Time
}

func New(config Config) (*App, error) {
	db, err := gorm.Open(mysql.Open(config.MySQLDSN), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		return nil, err
	}
	return &App{
		db: db, config: config,
		client: &http.Client{Timeout: 15 * time.Second},
		logger: slog.Default(),
		now:    func() time.Time { return time.Now().UTC() },
	}, nil
}

func (a *App) Close() error {
	sqlDB, err := a.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

func (a *App) Run(ctx context.Context) error {
	if !a.config.Enabled() {
		a.logger.Warn("notification delivery worker disabled: platform notification credentials not configured")
		return nil
	}
	if _, err := a.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	ticker := time.NewTicker(a.config.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if _, err := a.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
				return err
			}
		}
	}
}

func (a *App) RunOnce(ctx context.Context) (int, error) {
	var items []notification.Notification
	if err := a.db.WithContext(ctx).
		Where("platform_delivered_at IS NULL AND platform_delivery_failed_at IS NULL AND status IN ?", []string{notification.StatusUnread, notification.StatusRead}).
		Order("id ASC").Limit(a.config.BatchSize).Find(&items).Error; err != nil {
		return 0, err
	}
	delivered := 0
	for _, item := range items {
		if err := a.deliver(ctx, item); err != nil {
			if isPermanentDeliveryError(err) {
				if updateErr := a.db.WithContext(ctx).Model(&notification.Notification{}).Where("id = ?", item.ID).Updates(map[string]any{
					"platform_delivery_failed_at":  a.now(),
					"platform_delivery_error_code": "PLATFORM_VALIDATION_ERROR",
				}).Error; updateErr != nil {
					return delivered, updateErr
				}
				a.logger.Warn("CRM notification permanently rejected by platform", "notification_id", item.ID, "error", err)
				continue
			}
			a.logger.Error("deliver CRM notification to platform", "notification_id", item.ID, "error", err)
			continue
		}
		if err := a.db.WithContext(ctx).Model(&notification.Notification{}).Where("id = ?", item.ID).
			Update("platform_delivered_at", a.now()).Error; err != nil {
			return delivered, err
		}
		delivered++
	}
	return delivered, nil
}

func (a *App) deliver(ctx context.Context, item notification.Notification) error {
	token, err := a.accessToken(ctx)
	if err != nil {
		return err
	}
	referenceType, referenceID, err := referenceFor(item)
	if err != nil {
		return err
	}
	platformEventID := platformEventCode(a.config.ApplicationCode, a.config.EnvironmentCode, item.SourceEventID)
	payload := ingestionEventPayload{
		EventID:           platformEventID,
		EventType:         item.Type,
		NotificationScope: "CROSS_SYSTEM",
		Priority:          priorityFor(item.Type),
		Title:             item.Title,
		Content:           item.Body,
		TargetURL:         item.TargetPath,
		ReferenceType:     referenceType,
		ReferenceID:       referenceID,
		IdempotencyKey:    platformEventID,
		Recipients:        []string{item.RecipientID},
		OccurredAt:        item.CreatedAt,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode notification payload: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, a.config.PlatformBaseURL+"/api/v1/notifications/events", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := a.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	responseBody, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		var receipt struct {
			Data struct {
				Status string `json:"status"`
			} `json:"data"`
		}
		if err := json.Unmarshal(responseBody, &receipt); err != nil {
			return fmt.Errorf("decode platform notification receipt: %w", err)
		}
		status := strings.ToUpper(strings.TrimSpace(receipt.Data.Status))
		if status != "ACCEPTED" && status != "DUPLICATE" {
			return fmt.Errorf("platform notification receipt is not accepted: %q", status)
		}
		return nil
	}
	return &platformAPIError{statusCode: response.StatusCode, body: strings.TrimSpace(string(responseBody))}
}

var platformEventCodePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_:-]{0,127}$`)

func platformEventCode(application, environment, source string) string {
	source = strings.TrimSpace(source)
	if platformEventCodePattern.MatchString(source) && len(application)+len(environment)+len(source)+2 <= 128 {
		return source
	}
	return fmt.Sprintf("CRM_%X", sha256.Sum256([]byte(source)))
}

type platformAPIError struct {
	statusCode int
	body       string
}

func (err *platformAPIError) Error() string {
	return fmt.Sprintf("platform notification API returned %d: %s", err.statusCode, err.body)
}

func isPermanentDeliveryError(err error) bool {
	var apiErr *platformAPIError
	return errors.As(err, &apiErr) && apiErr.statusCode == http.StatusUnprocessableEntity
}

func referenceFor(item notification.Notification) (string, string, error) {
	if item.RequestID > 0 {
		return "PRESALE_REQUEST", strconv.FormatUint(item.RequestID, 10), nil
	}
	if item.OpportunityID > 0 {
		return "OPPORTUNITY", strconv.FormatUint(item.OpportunityID, 10), nil
	}
	return "", "", fmt.Errorf("notification %d has no presale request or opportunity reference", item.ID)
}

func (a *App) accessToken(ctx context.Context) (string, error) {
	form := url.Values{"grant_type": {"client_credentials"}, "scope": {"notification.ingest"}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, a.config.PlatformTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	request.SetBasicAuth(a.config.ClientID, a.config.ClientSecret)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := a.client.Do(request)
	if err != nil {
		return "", fmt.Errorf("request notification token: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return "", fmt.Errorf("platform notification token returned HTTP %d", response.StatusCode)
	}
	var token struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		Scope       string `json:"scope"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&token); err != nil {
		return "", fmt.Errorf("decode notification token: %w", err)
	}
	if strings.TrimSpace(token.AccessToken) == "" || !strings.EqualFold(token.TokenType, "bearer") {
		return "", fmt.Errorf("platform notification token is missing bearer access token")
	}
	return token.AccessToken, nil
}

type ingestionEventPayload struct {
	EventID           string     `json:"event_id"`
	EventType         string     `json:"event_type"`
	NotificationScope string     `json:"notification_scope"`
	Priority          string     `json:"priority"`
	Title             string     `json:"title"`
	Content           string     `json:"content"`
	TargetURL         string     `json:"target_url"`
	ReferenceType     string     `json:"reference_type"`
	ReferenceID       string     `json:"reference_id"`
	IdempotencyKey    string     `json:"idempotency_key"`
	Recipients        []string   `json:"recipient_user_ids"`
	OccurredAt        time.Time  `json:"occurred_at"`
	ExpiresAt         *time.Time `json:"expires_at"`
}

func priorityFor(notificationType string) string {
	switch notificationType {
	case notification.TypePresaleApprovalPending:
		return "HIGH"
	default:
		return "NORMAL"
	}
}
