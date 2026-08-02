package presaleengineersyncworker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/presale"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/security"
	"gorm.io/gorm"
)

var errLeaseLost = errors.New("PMS engineer sync lease was lost")

type Store struct {
	db    *gorm.DB
	codec *security.SensitiveCodec
	ids   func() string
}

func NewStore(db *gorm.DB, codec *security.SensitiveCodec, ids func() string) *Store {
	return &Store{db: db, codec: codec, ids: ids}
}

func (s *Store) Schedule(ctx context.Context, now time.Time, interval time.Duration) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO crm_presale_engineer_sync_states
(tenant_id,created_by,updated_by,created_at,updated_at,version,next_sync_at,last_job_no,last_person_count)
SELECT tenants.tenant_id,'system','system',?,?,1,?,'',0 FROM (
 SELECT DISTINCT tenant_id FROM crm_customers WHERE deleted_at IS NULL
 UNION SELECT DISTINCT tenant_id FROM crm_presale_requests WHERE deleted_at IS NULL
 UNION SELECT DISTINCT tenant_id FROM crm_presale_engineers WHERE deleted_at IS NULL
) tenants LEFT JOIN crm_presale_engineer_sync_states s ON s.tenant_id=tenants.tenant_id
WHERE s.id IS NULL`, now, now, now).Error; err != nil {
			return err
		}
		var states []presale.EngineerSyncState
		if err := tx.Raw(`SELECT * FROM crm_presale_engineer_sync_states WHERE deleted_at IS NULL AND next_sync_at<=? ORDER BY next_sync_at,id LIMIT 100 FOR UPDATE SKIP LOCKED`, now).Scan(&states).Error; err != nil {
			return err
		}
		for _, state := range states {
			var count int64
			if err := tx.Model(&presale.EngineerSyncJob{}).Where("tenant_id=? AND status IN ? AND deleted_at IS NULL", state.TenantID, []string{"PENDING", "PROCESSING", "RETRY_WAIT"}).Count(&count).Error; err != nil {
				return err
			}
			if count == 0 {
				job := presale.EngineerSyncJob{BaseModel: presale.BaseModel{TenantID: state.TenantID, CreatedBy: "system", UpdatedBy: "system", Version: 1}, JobNo: s.ids(), TriggerType: "SCHEDULED", RequestedBy: "system", IdempotencyKey: "scheduled:" + state.NextSyncAt.UTC().Format(time.RFC3339Nano), RequestHash: "SCHEDULED", Status: "PENDING"}
				if err := tx.Create(&job).Error; err != nil {
					return err
				}
			}
			if err := tx.Model(&presale.EngineerSyncState{}).Where("id=? AND version=?", state.ID, state.Version).Updates(map[string]any{"next_sync_at": now.Add(interval), "updated_by": "system", "updated_at": now, "version": gorm.Expr("version+1")}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) Claim(ctx context.Context, worker string, now time.Time, lease time.Duration, limit int) ([]presale.EngineerSyncJob, error) {
	var output []presale.EngineerSyncJob
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT * FROM crm_presale_engineer_sync_jobs
WHERE deleted_at IS NULL AND ((status IN ('PENDING','RETRY_WAIT') AND (next_retry_at IS NULL OR next_retry_at<=?)) OR (status='PROCESSING' AND locked_until<?))
ORDER BY created_at,id LIMIT ? FOR UPDATE SKIP LOCKED`, now, now, limit).Scan(&output).Error; err != nil {
			return err
		}
		for i := range output {
			until := now.Add(lease)
			if err := tx.Model(&presale.EngineerSyncJob{}).Where("id=?", output[i].ID).Updates(map[string]any{"status": "PROCESSING", "locked_by": worker, "locked_until": until, "started_at": now, "updated_at": now}).Error; err != nil {
				return err
			}
			output[i].Status, output[i].LockedBy, output[i].LockedUntil = "PROCESSING", worker, &until
		}
		return nil
	})
	return output, err
}

func (s *Store) Renew(ctx context.Context, job presale.EngineerSyncJob, worker string, now time.Time, lease time.Duration) error {
	result := s.db.WithContext(ctx).Model(&presale.EngineerSyncJob{}).Where("id=? AND tenant_id=? AND status='PROCESSING' AND locked_by=? AND locked_until>=?", job.ID, job.TenantID, worker, now).Updates(map[string]any{"locked_until": now.Add(lease), "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errLeaseLost
	}
	return nil
}

func (s *Store) Apply(ctx context.Context, job presale.EngineerSyncJob, worker string, snapshot SourceSnapshot, now time.Time, interval time.Duration) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var locked presale.EngineerSyncJob
		if err := tx.Raw(`SELECT * FROM crm_presale_engineer_sync_jobs WHERE id=? AND tenant_id=? AND status='PROCESSING' AND locked_by=? AND locked_until>=? FOR UPDATE`, job.ID, job.TenantID, worker, now).Scan(&locked).Error; err != nil {
			return err
		}
		if locked.ID == 0 {
			return errLeaseLost
		}
		if err := validateSnapshot(job.TenantID, snapshot); err != nil {
			return err
		}
		seen := make([]string, 0, len(snapshot.Engineers))
		for _, source := range snapshot.Engineers {
			role, ok := normalizedRole(source.Role)
			if !ok {
				return errors.New("PMS technician response contains an unsupported role")
			}
			contact, err := s.codec.Encrypt(source.Contact)
			if err != nil {
				return err
			}
			engineer := presale.Engineer{BaseModel: presale.BaseModel{TenantID: job.TenantID, CreatedBy: "pms-sync", UpdatedBy: "pms-sync", Version: 1}, PersonID: source.PersonID, PersonName: source.PersonName, Department: source.Department, Role: role, SkillTagsJSON: mustJSON(source.SkillTags), ContactCipher: contact, ValidFlag: source.ValidFlag, SourceUpdatedAt: source.SyncedAt, SyncedAt: now}
			if err = tx.Exec(`INSERT INTO crm_presale_engineers
(tenant_id,created_by,updated_by,created_at,updated_at,version,person_id,person_name,department,role,skill_tags_json,contact_cipher,valid_flag,source_updated_at,synced_at)
VALUES (?,?,?,?,?,1,?,?,?,?,?,?,?,?,?) ON DUPLICATE KEY UPDATE person_name=VALUES(person_name),department=VALUES(department),role=VALUES(role),skill_tags_json=VALUES(skill_tags_json),contact_cipher=VALUES(contact_cipher),valid_flag=VALUES(valid_flag),source_updated_at=VALUES(source_updated_at),synced_at=VALUES(synced_at),updated_by='pms-sync',updated_at=VALUES(updated_at),version=version+1`, engineer.TenantID, engineer.CreatedBy, engineer.UpdatedBy, now, now, engineer.PersonID, engineer.PersonName, engineer.Department, engineer.Role, engineer.SkillTagsJSON, engineer.ContactCipher, engineer.ValidFlag, engineer.SourceUpdatedAt, engineer.SyncedAt).Error; err != nil {
				return err
			}
			seen = append(seen, source.PersonID)
		}
		inactive := tx.Model(&presale.Engineer{}).Where("tenant_id=? AND deleted_at IS NULL", job.TenantID)
		if len(seen) > 0 {
			inactive = inactive.Where("person_id NOT IN ?", seen)
		}
		if err := inactive.Updates(map[string]any{"valid_flag": false, "updated_by": "pms-sync", "updated_at": now, "version": gorm.Expr("version+1")}).Error; err != nil {
			return err
		}
		if err := tx.Model(&presale.EngineerSyncState{}).Where("tenant_id=?", job.TenantID).Updates(map[string]any{"last_attempt_at": now, "last_successful_at": now, "last_source_revision": snapshot.Revision, "next_sync_at": now.Add(interval), "last_job_no": job.JobNo, "last_person_count": len(snapshot.Engineers), "updated_by": "pms-sync", "updated_at": now, "version": gorm.Expr("version+1")}).Error; err != nil {
			return err
		}
		result := tx.Model(&presale.EngineerSyncJob{}).Where("id=? AND status='PROCESSING' AND locked_by=?", job.ID, worker).Updates(map[string]any{"status": "SUCCEEDED", "person_count": len(snapshot.Engineers), "source_revision": snapshot.Revision, "finished_at": now, "locked_by": "", "locked_until": nil, "next_retry_at": nil, "last_error": "", "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errLeaseLost
		}
		return nil
	})
}

func (s *Store) Fail(ctx context.Context, job presale.EngineerSyncJob, worker string, now time.Time, message string) error {
	attempt := job.RetryCount + 1
	status := "RETRY_WAIT"
	var next *time.Time
	if retry, ok := retryAt(now, attempt); ok {
		next = &retry
	} else {
		status = "DEAD_LETTER"
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// A failed fetch/apply may return after the lease has expired. In that
		// case the old worker must not overwrite a task that another instance is
		// now entitled to reclaim.
		result := tx.Model(&presale.EngineerSyncJob{}).
			Where("id=? AND tenant_id=? AND status='PROCESSING' AND locked_by=? AND locked_until>=?", job.ID, job.TenantID, worker, now).
			Updates(map[string]any{"status": status, "retry_count": attempt, "next_retry_at": next, "locked_by": "", "locked_until": nil, "last_error": sanitize(message), "finished_at": now, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errLeaseLost
		}
		return tx.Model(&presale.EngineerSyncState{}).Where("tenant_id=?", job.TenantID).Updates(map[string]any{"last_attempt_at": now, "last_job_no": job.JobNo, "updated_by": "pms-sync", "updated_at": now, "version": gorm.Expr("version+1")}).Error
	})
}

func mustJSON(value []string) []byte { encoded, _ := json.Marshal(value); return encoded }
func sanitize(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(value, "\n", " "), "\r", " "))
	runes := []rune(value)
	if len(runes) > 1000 {
		return string(runes[:1000])
	}
	return value
}
func retryAt(now time.Time, attempt uint8) (time.Time, bool) {
	delays := []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour, 3 * time.Hour, 6 * time.Hour}
	if attempt == 0 || int(attempt) > len(delays) {
		return time.Time{}, false
	}
	return now.Add(delays[attempt-1]), true
}
