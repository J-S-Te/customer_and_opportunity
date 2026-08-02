package presalealertworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/presale"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const leaseName = "presale-alert-scan"

const (
	alertPending   = "PENDING"
	alertUnread    = "UNREAD"
	alertCancelled = "CANCELLED"
)

const recipientRoleScope = "ROLE_SCOPE"

type recipientTarget struct {
	Kind string
	ID   string
}

type App struct {
	db            *gorm.DB
	workerID      string
	pollInterval  time.Duration
	leaseDuration time.Duration
	batchSize     int
	now           func() time.Time
}

func New(config Config) (*App, error) {
	db, err := gorm.Open(mysql.Open(config.MySQLDSN), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		return nil, err
	}
	return &App{db: db, workerID: config.WorkerID, pollInterval: config.PollInterval, leaseDuration: config.LeaseDuration, batchSize: config.BatchSize, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (a *App) Close() error {
	db, err := a.db.DB()
	if err != nil {
		return err
	}
	return db.Close()
}

func (a *App) Run(ctx context.Context) error {
	if err := a.Scan(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	ticker := time.NewTicker(a.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := a.Scan(ctx); err != nil && !errors.Is(err, context.Canceled) {
				return err
			}
		}
	}
}

type scanRequest struct {
	ID                  uint64
	TenantID            string
	RequestNo           string
	ApplicantID         string
	Status              presale.RequestStatus
	CurrentApprovalNode uint8
	ExpectedEnd         time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
	StatusEnteredAt     time.Time
}

func (a *App) Scan(ctx context.Context) error {
	now := a.now().UTC()
	acquired, err := a.acquireLease(ctx, now)
	if err != nil || !acquired {
		return err
	}
	if err = a.cancelTerminalPending(ctx, now); err != nil {
		return err
	}
	var afterID uint64
	for {
		var requests []scanRequest
		err = a.db.WithContext(ctx).Table("crm_presale_requests r").Select(`r.id,r.tenant_id,r.request_no,r.applicant_id,r.status,r.current_approval_node,r.expected_end,r.created_at,r.updated_at,
			CASE WHEN r.status='PENDING_APPROVAL' AND r.current_approval_node=2 THEN COALESCE(
			 (SELECT MAX(l.approved_at) FROM crm_presale_approval_logs l WHERE l.tenant_id=r.tenant_id AND l.request_id=r.id AND l.node=1 AND l.result='PASS'),r.updated_at)
			 ELSE COALESCE((SELECT MAX(s.occurred_at) FROM crm_presale_status_logs s
			 WHERE s.tenant_id=r.tenant_id AND s.request_id=r.id AND s.to_status=r.status),r.updated_at) END AS status_entered_at`).
			Where("r.id>? AND r.deleted_at IS NULL AND r.status NOT IN ('COMPLETED','REJECTED','CANCELLED')", afterID).Order("r.id").Limit(a.batchSize).Scan(&requests).Error
		if err != nil {
			return err
		}
		for _, request := range requests {
			if err = a.scanRequest(ctx, request, now); err != nil {
				return err
			}
		}
		var done bool
		afterID, done = nextRequestPage(afterID, requests, a.batchSize)
		if done {
			break
		}
	}
	return a.deliverSiteMessages(ctx, now)
}

func (a *App) acquireLease(ctx context.Context, now time.Time) (bool, error) {
	acquired := false
	err := a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Exec(`INSERT IGNORE INTO crm_presale_job_leases(job_name,owner_id,lease_until,updated_at) VALUES(?,?,?,?)`, leaseName, a.workerID, now.Add(a.leaseDuration), now)
		if result.Error != nil {
			return result.Error
		}
		var lease struct {
			OwnerID    string
			LeaseUntil time.Time
		}
		if err := tx.Table("crm_presale_job_leases").Clauses(clause.Locking{Strength: "UPDATE"}).Select("owner_id,lease_until").Where("job_name=?", leaseName).Take(&lease).Error; err != nil {
			return err
		}
		if lease.OwnerID == a.workerID || !lease.LeaseUntil.After(now) {
			if err := tx.Table("crm_presale_job_leases").Where("job_name=?", leaseName).Updates(map[string]any{"owner_id": a.workerID, "lease_until": now.Add(a.leaseDuration), "updated_at": now}).Error; err != nil {
				return err
			}
			acquired = true
		}
		return nil
	})
	return acquired, err
}

func (a *App) scanRequest(ctx context.Context, request scanRequest, now time.Time) error {
	return a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var locked scanRequest
		if err := tx.Table("crm_presale_requests").Clauses(clause.Locking{Strength: "UPDATE"}).Select("id,tenant_id,request_no,applicant_id,status,current_approval_node,expected_end,created_at,updated_at").Where("tenant_id=? AND id=? AND deleted_at IS NULL", request.TenantID, request.ID).Take(&locked).Error; err != nil {
			return err
		}
		if terminal(locked.Status) {
			return cancelPending(tx, locked, now)
		}
		// Recompute the transition basis while holding the request lock. The
		// outer scan projection is only a candidate list and is never trusted for
		// timing decisions after concurrent state changes.
		if err := loadStatusEnteredAt(tx, &locked); err != nil {
			return err
		}
		var rules []presale.AlertRule
		if err := tx.Where("tenant_id=? AND enabled=1 AND deleted_at IS NULL", locked.TenantID).Find(&rules).Error; err != nil {
			return err
		}
		assignments, err := currentAssignees(tx, locked)
		if err != nil {
			return err
		}
		desired := make(map[alertIdentity]bool)
		for _, rule := range rules {
			basis, due, recipients, active := evaluate(rule, locked, assignments, now)
			if !active {
				continue
			}
			recipients, err = resolveRecipientTargets(tx, locked.TenantID, recipients, now)
			if err != nil {
				return err
			}
			for _, recipient := range recipients {
				desired[alertIdentity{Type: rule.Type, RuleVersion: rule.ConfigVersion, RecipientKind: recipient.Kind, RecipientID: recipient.ID}] = true
				if err = createAlert(tx, locked, rule, basis, due, recipient, now); err != nil {
					return err
				}
			}
		}
		return cancelObsolete(tx, locked, desired, now)
	})
}

func loadStatusEnteredAt(tx *gorm.DB, request *scanRequest) error {
	var entered *time.Time
	if request.Status == presale.StatusPendingApproval && request.CurrentApprovalNode == 2 {
		if err := tx.Table("crm_presale_approval_logs").Select("MAX(approved_at)").Where("tenant_id=? AND request_id=? AND node=1 AND result='PASS'", request.TenantID, request.ID).Scan(&entered).Error; err != nil {
			return err
		}
	} else {
		if err := tx.Table("crm_presale_status_logs").Select("MAX(occurred_at)").Where("tenant_id=? AND request_id=? AND to_status=?", request.TenantID, request.ID, request.Status).Scan(&entered).Error; err != nil {
			return err
		}
	}
	if entered == nil {
		request.StatusEnteredAt = request.UpdatedAt
	} else {
		request.StatusEnteredAt = entered.UTC()
	}
	return nil
}

func terminal(status presale.RequestStatus) bool {
	return status == presale.StatusCompleted || status == presale.StatusRejected || status == presale.StatusCancelled
}

func cancellableAlertStatus(status string) bool {
	return status == alertPending || status == alertUnread
}

func currentAssignees(tx *gorm.DB, request scanRequest) ([]string, error) {
	var ids []string
	err := tx.Table("crm_presale_assignments").Distinct("assignee_id").Where("tenant_id=? AND request_id=? AND is_current=1 AND deleted_at IS NULL", request.TenantID, request.ID).Pluck("assignee_id", &ids).Error
	return ids, err
}

func evaluate(rule presale.AlertRule, request scanRequest, assignees []string, now time.Time) (time.Time, time.Time, []recipientTarget, bool) {
	hours := time.Duration(rule.ThresholdHours) * time.Hour
	switch rule.Type {
	case presale.AlertApprovalNode1Overdue:
		due := request.StatusEnteredAt.Add(hours)
		return request.StatusEnteredAt, due, roleRecipientsPlaceholder("sales_director"), request.Status == presale.StatusPendingApproval && request.CurrentApprovalNode == 1 && !now.Before(due)
	case presale.AlertApprovalNode2Overdue:
		due := request.StatusEnteredAt.Add(hours)
		return request.StatusEnteredAt, due, roleRecipientsPlaceholder("team_lead"), request.Status == presale.StatusPendingApproval && request.CurrentApprovalNode == 2 && !now.Before(due)
	case presale.AlertAssignmentOverdue:
		due := request.StatusEnteredAt.Add(hours)
		return request.StatusEnteredAt, due, roleRecipientsPlaceholder("team_lead"), request.Status == presale.StatusApprovedPendingAssignment && !now.Before(due)
	case presale.AlertExecutionDueSoon:
		due := request.ExpectedEnd.Add(-hours)
		return request.ExpectedEnd, due, executionRecipients(assignees, request.ApplicantID), request.Status == presale.StatusExecuting && !now.Before(due) && now.Before(request.ExpectedEnd)
	case presale.AlertExecutionOverdue:
		due := request.ExpectedEnd.Add(hours)
		return request.ExpectedEnd, due, executionRecipients(assignees, request.ApplicantID), request.Status == presale.StatusExecuting && !now.Before(due)
	default:
		return time.Time{}, time.Time{}, nil, false
	}
}

// Management recipients are resolved from active, locally persisted CRM OIDC
// sessions whose roles were signed by the base platform. These are CRM user
// roles, not PMS personnel-pool roles. The approval engine does not currently
// expose the actual task assignee, so this deliberately does not claim to
// notify the current approver.
func roleRecipientsPlaceholder(role string) []recipientTarget {
	return []recipientTarget{{Kind: recipientRoleScope, ID: role}}
}

func executionRecipients(assignees []string, applicant string) []recipientTarget {
	result := make([]recipientTarget, 0, len(assignees)+2)
	for _, value := range assignees {
		result = append(result, recipientTarget{Kind: presale.AlertRecipientPerson, ID: value})
	}
	result = append(result, recipientTarget{Kind: presale.AlertRecipientUser, ID: applicant})
	result = append(result, roleRecipientsPlaceholder("team_lead")...)
	return uniqueRecipients(result)
}

func resolveRecipientTargets(tx *gorm.DB, tenant string, candidates []recipientTarget, now time.Time) ([]recipientTarget, error) {
	resolved := make([]recipientTarget, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Kind != recipientRoleScope {
			resolved = append(resolved, candidate)
			continue
		}
		if !supportedManagementRecipientRole(candidate.ID) {
			return nil, fmt.Errorf("unsupported CRM management recipient role %q", candidate.ID)
		}
		var values []string
		// CRM management roles are signed OIDC user roles, not PMS personnel-pool
		// roles. The local active-session projection is the only currently
		// available authoritative mapping from those roles to CRM user IDs.
		if err := tx.Table("crm_oidc_sessions").Distinct("platform_user_id").
			Where("tenant_id=? AND platform_user_id<>'' AND revoked_at IS NULL AND expires_at>? AND JSON_CONTAINS(roles_json,JSON_QUOTE(?),'$')", tenant, now, candidate.ID).
			Pluck("platform_user_id", &values).Error; err != nil {
			return nil, err
		}
		for _, value := range values {
			resolved = append(resolved, recipientTarget{Kind: presale.AlertRecipientUser, ID: value})
		}
	}
	return uniqueRecipients(resolved), nil
}

func supportedManagementRecipientRole(role string) bool {
	return role == "sales_director" || role == "team_lead"
}

func uniqueRecipients(values []recipientTarget) []recipientTarget {
	seen := make(map[string]bool, len(values))
	result := make([]recipientTarget, 0, len(values))
	for _, value := range values {
		key := value.Kind + "\x00" + value.ID
		if (value.Kind == presale.AlertRecipientUser || value.Kind == presale.AlertRecipientPerson || value.Kind == recipientRoleScope) && value.ID != "" && !seen[key] {
			seen[key] = true
			result = append(result, value)
		}
	}
	return result
}

func createAlert(tx *gorm.DB, request scanRequest, rule presale.AlertRule, basis, due time.Time, recipient recipientTarget, now time.Time) error {
	alert := presale.Alert{BaseModel: presale.BaseModel{TenantID: request.TenantID, CreatedBy: "presale-alert-worker", UpdatedBy: "presale-alert-worker", Version: 1}, RequestID: request.ID, AlertType: rule.Type, RuleVersion: rule.ConfigVersion, BasisAt: basis, DueAt: due, Status: alertPending, RecipientKind: recipient.Kind, RecipientID: recipient.ID}
	result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&alert)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return nil
	}
	payload, err := json.Marshal(map[string]any{"alert_id": alert.ID, "request_id": request.ID, "request_no": request.RequestNo, "alert_type": rule.Type, "recipient_kind": recipient.Kind, "recipient_id": recipient.ID, "due_at": due, "basis_at": basis, "path": "/customer-opportunity/presale?request_id=" + strconv.FormatUint(request.ID, 10)})
	if err != nil {
		return err
	}
	event := presale.OutboxEvent{EventID: fmt.Sprintf("presale-alert-%d", alert.ID), TenantID: request.TenantID, EventType: "PRESALE_ALERT_SITE_MESSAGE", AggregateType: "presale_alert", AggregateID: strconv.FormatUint(alert.ID, 10), Payload: payload, Status: "PENDING", CreatedAt: now}
	return tx.Create(&event).Error
}

func cancelPending(tx *gorm.DB, request scanRequest, now time.Time) error {
	var ids []uint64
	if err := tx.Model(&presale.Alert{}).Where("tenant_id=? AND request_id=? AND status IN ('PENDING','UNREAD')", request.TenantID, request.ID).Pluck("id", &ids).Error; err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	if err := tx.Model(&presale.Alert{}).Where("id IN ? AND status IN ('PENDING','UNREAD')", ids).Updates(map[string]any{"status": alertCancelled, "updated_at": now, "updated_by": "presale-alert-worker", "version": gorm.Expr("version+1")}).Error; err != nil {
		return err
	}
	return tx.Model(&presale.OutboxEvent{}).Where("event_type='PRESALE_ALERT_SITE_MESSAGE' AND aggregate_type='presale_alert' AND aggregate_id IN ? AND status='PENDING'", uint64Strings(ids)).Update("status", "CANCELLED").Error
}

type alertIdentity struct {
	Type          presale.AlertType
	RuleVersion   uint64
	RecipientKind string
	RecipientID   string
}

func cancelObsolete(tx *gorm.DB, request scanRequest, desired map[alertIdentity]bool, now time.Time) error {
	var pending []presale.Alert
	if err := tx.Where("tenant_id=? AND request_id=? AND status IN ('PENDING','UNREAD')", request.TenantID, request.ID).Find(&pending).Error; err != nil {
		return err
	}
	for _, alert := range pending {
		if desired[alertIdentity{Type: alert.AlertType, RuleVersion: alert.RuleVersion, RecipientKind: alert.RecipientKind, RecipientID: alert.RecipientID}] {
			continue
		}
		if err := cancelActiveByID(tx, alert.ID, now); err != nil {
			return err
		}
	}
	return nil
}

func cancelActiveByID(tx *gorm.DB, id uint64, now time.Time) error {
	if err := tx.Model(&presale.Alert{}).Where("id=? AND status IN ('PENDING','UNREAD')", id).Updates(map[string]any{"status": alertCancelled, "updated_at": now, "updated_by": "presale-alert-worker", "version": gorm.Expr("version+1")}).Error; err != nil {
		return err
	}
	return tx.Model(&presale.OutboxEvent{}).Where("event_type='PRESALE_ALERT_SITE_MESSAGE' AND aggregate_type='presale_alert' AND aggregate_id=? AND status='PENDING'", strconv.FormatUint(id, 10)).Update("status", "CANCELLED").Error
}

func uint64Strings(values []uint64) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, strconv.FormatUint(value, 10))
	}
	return result
}

func nextRequestPage(previous uint64, values []scanRequest, batchSize int) (uint64, bool) {
	if len(values) == 0 {
		return previous, true
	}
	next := values[len(values)-1].ID
	return next, len(values) < batchSize
}

func (a *App) cancelTerminalPending(ctx context.Context, now time.Time) error {
	var requests []scanRequest
	if err := a.db.WithContext(ctx).Table("crm_presale_requests r").Distinct("r.id,r.tenant_id").Joins("JOIN crm_presale_alerts a ON a.tenant_id=r.tenant_id AND a.request_id=r.id AND a.status IN ('PENDING','UNREAD')").Where("r.status IN ('COMPLETED','REJECTED','CANCELLED')").Scan(&requests).Error; err != nil {
		return err
	}
	for _, request := range requests {
		if err := a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return cancelPending(tx, request, now) }); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) deliverSiteMessages(ctx context.Context, now time.Time) error {
	for {
		var events []presale.OutboxEvent
		err := a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Raw(`SELECT * FROM crm_outbox_events WHERE event_type='PRESALE_ALERT_SITE_MESSAGE' AND status='PENDING' ORDER BY created_at,id LIMIT ? FOR UPDATE SKIP LOCKED`, a.batchSize).Scan(&events).Error; err != nil {
				return err
			}
			for _, event := range events {
				alertID, err := strconv.ParseUint(event.AggregateID, 10, 64)
				if err != nil {
					return err
				}
				var alert presale.Alert
				if err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND id=?", event.TenantID, alertID).Take(&alert).Error; errors.Is(err, gorm.ErrRecordNotFound) {
					if err = tx.Model(&presale.OutboxEvent{}).Where("id=? AND status='PENDING'", event.ID).Update("status", "CANCELLED").Error; err != nil {
						return err
					}
					continue
				} else if err != nil {
					return err
				}
				if alert.Status != alertPending || (alert.RecipientKind != presale.AlertRecipientUser && alert.RecipientKind != presale.AlertRecipientPerson) {
					if alert.Status == alertPending {
						if err = tx.Model(&presale.Alert{}).Where("tenant_id=? AND id=? AND status='PENDING'", event.TenantID, alert.ID).Updates(map[string]any{"status": alertCancelled, "updated_at": now, "updated_by": "presale-alert-worker", "version": gorm.Expr("version+1")}).Error; err != nil {
							return err
						}
					}
					if err = tx.Model(&presale.OutboxEvent{}).Where("id=? AND status='PENDING'", event.ID).Update("status", "CANCELLED").Error; err != nil {
						return err
					}
					continue
				}
				if err = tx.Model(&presale.Alert{}).Where("id=? AND status='PENDING'", alert.ID).Updates(map[string]any{"status": alertUnread, "sent_at": now, "updated_at": now, "updated_by": "presale-alert-worker", "version": gorm.Expr("version+1")}).Error; err != nil {
					return err
				}
				if err = tx.Model(&presale.OutboxEvent{}).Where("id=? AND status='PENDING'", event.ID).Updates(map[string]any{"status": "SENT", "sent_at": now}).Error; err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		if len(events) < a.batchSize {
			return nil
		}
	}
}
