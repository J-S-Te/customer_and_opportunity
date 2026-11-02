// Package handoverguard 提供人员离职交接的写入门闩。
//
// 背景：冻结检查如果只发生在写事务之外，就存在"check-then-act"竞态窗口——
// 冻结行落库前已通过检查的写入会在冻结之后提交，新责任既不在交接清单里，
// 也留在离职人名下。这里用 (tenant_id, user_id) 粒度的门闩行把
// "CreateFreeze 插入冻结行"与"责任分配写事务"对同一用户串行化，
// 使写入只可能在"未冻结"或"已冻结"两种确定状态下完成。
package handoverguard

import (
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	// ErrUserFrozen 表示在门闩行锁内仍查到 FROZEN 冻结行：本次写入必须中止。
	ErrUserFrozen = errors.New("user is frozen by an in-flight handover")
	// ErrTransactionRequired 表示调用方没有把门闩放进写事务。
	// 事务外执行时行锁立即释放，不构成串行化，因此这里直接拒绝而不是静默降级。
	ErrTransactionRequired = errors.New("handover freeze gate requires an active write transaction")
	// ErrSubjectRequired 表示缺少租户或用户标识，无法定位门闩行（fail-closed）。
	ErrSubjectRequired = errors.New("handover freeze gate requires tenant and user identifiers")
)

const (
	freezeTable  = "crm_handover_freeze"
	gateTable    = "crm_handover_gate"
	frozenStatus = "FROZEN"
)

type gateRow struct {
	TenantID  string    `gorm:"column:tenant_id;primaryKey;size:64"`
	UserID    string    `gorm:"column:user_id;primaryKey;size:64"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (gateRow) TableName() string { return gateTable }

// freezeProbe 只取 id，用于在加锁读里判断是否存在 FROZEN 行。
type freezeProbe struct {
	ID string `gorm:"column:id"`
}

// Lock 保证 (tenant, user) 门闩行存在并在当前事务内加行锁。
// 冻结侧（CreateFreeze）与责任写入侧都必须先走这里，锁顺序一致才不会互相死锁。
func Lock(tx *gorm.DB, tenantID, userID string) error {
	tenantID, userID = strings.TrimSpace(tenantID), strings.TrimSpace(userID)
	if tx == nil {
		return ErrTransactionRequired
	}
	if tenantID == "" || userID == "" {
		return ErrSubjectRequired
	}
	// 历史数据没有门闩行：幂等插入自愈；已存在的行不改写 updated_at，避免无意义写放大。
	if err := tx.Exec(
		"INSERT INTO "+gateTable+" (tenant_id, user_id, updated_at) VALUES (?, ?, ?) ON DUPLICATE KEY UPDATE updated_at = updated_at",
		tenantID, userID, time.Now().UTC(),
	).Error; err != nil {
		return err
	}
	var gate gateRow
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("tenant_id = ? AND user_id = ?", tenantID, userID).
		Take(&gate).Error; err != nil {
		return err
	}
	return nil
}

// Guard 在写事务内锁定门闩行后复核冻结：存在 FROZEN 行返回 ErrUserFrozen。
// 这是责任写入的最终防线；调用方原有的预检只用于尽早给出友好文案。
//
// 复核必须用加锁读而不是普通 SELECT：写事务在等待门闩行锁之前可能已经建立过
// REPEATABLE READ 快照，普通读会看到"冻结落库前"的旧快照而漏判；
// 加锁读读取最新已提交版本，配合门闩行锁才能真正关闭竞态窗口。
func Guard(tx *gorm.DB, tenantID, userID string) error {
	if err := Lock(tx, tenantID, userID); err != nil {
		return err
	}
	var frozen freezeProbe
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Table(freezeTable).
		Select("id").
		Where("tenant_id = ? AND user_id = ? AND status = ?", strings.TrimSpace(tenantID), strings.TrimSpace(userID), frozenStatus).
		Limit(1).
		Take(&frozen).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return ErrUserFrozen
}
