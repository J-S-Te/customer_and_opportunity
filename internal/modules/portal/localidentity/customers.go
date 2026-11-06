package localidentity

import (
	"context"
	"errors"
	"fmt"
	driver "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"strings"
)

type CustomerInput struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// CreateCustomer 登记真实本地客户并原子锁定本地权威。
// 参数：ctx 为真实管理会话，input 为本地客户业务编号及名称。
// 返回值：真实持久化客户或校验、唯一性与审计错误。
func (s *Service) CreateCustomer(ctx context.Context, input CustomerInput) (Customer, error) {
	p, err := s.principal(ctx, PermissionCustomerManage, true)
	if err != nil {
		return Customer{}, err
	}
	input.Code, input.Name = strings.TrimSpace(input.Code), strings.TrimSpace(input.Name)
	if !validText(input.Code, 64, true) || !validText(input.Name, 200, true) {
		return Customer{}, ErrInvalid
	}
	value := Customer{Record: s.record(p), Code: input.Code, Name: input.Name, Status: "ACTIVE", Namespace: LocalNamespace}
	err = s.Repo.WithTransaction(ctx, func(tx context.Context) error {
		if err := s.Repo.ClaimMode(tx, p.TenantID, ModeLocal, p.UserID, s.now()); err != nil {
			return err
		}
		var count int64
		if err := s.Repo.tx(tx).Model(&Customer{}).Where("tenant_id=? AND code=?", p.TenantID, input.Code).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return ErrConflict
		}
		if err := s.Repo.tx(tx).Create(&value).Error; err != nil {
			return err
		}
		return s.audit(tx, p, "PORTAL_LOCAL_CUSTOMER_CREATED", "portal_local_customer", fmt.Sprint(value.ID))
	})
	return value, localPersistenceError(err)
}

// ListCustomers 返回有界本地目录，不包含客户业务数据或联系方式明文。
// 参数：ctx 为真实管理员会话，after 为上一页真实ID，limit 为1至100。
// 返回值：本租户客户或权限、存储错误。
func (s *Service) ListCustomers(ctx context.Context, after uint64, limit int) ([]Customer, error) {
	p, err := s.principal(ctx, PermissionCustomerManage, false)
	if err != nil {
		return nil, err
	}
	if limit < 1 || limit > 100 {
		limit = 50
	}
	rows := []Customer{}
	err = s.Repo.tx(ctx).Where("tenant_id=? AND id>?", p.TenantID, after).Order("id").Limit(limit).Find(&rows).Error
	return rows, err
}

// activeCustomer 在本地权威内读取精确客户，不接收CRM编号替代。
// 参数：ctx 为请求，tenant/id 为可信目标。
// 返回值：有效本地客户或不存在错误。
func (r *Repository) activeCustomer(ctx context.Context, tenant string, id uint64) (Customer, error) {
	var row Customer
	err := r.tx(ctx).Where("tenant_id=? AND id=? AND status='ACTIVE' AND namespace=?", tenant, id, LocalNamespace).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return row, ErrNotFound
	}
	return row, err
}

// CreateContact 登记加密保存的真实开户联系人，不创建平台凭据。
// 参数：ctx 为管理会话，input 为本地客户及登记手机。
// 返回值：脱敏联系人或权限、范围、加密与事务错误。
func (s *Service) CreateContact(ctx context.Context, input ContactInput) (Contact, error) {
	p, err := s.principal(ctx, PermissionCustomerManage, true)
	if err != nil {
		return Contact{}, err
	}
	input.DisplayName, input.Phone = strings.TrimSpace(input.DisplayName), strings.TrimSpace(input.Phone)
	if !validText(input.DisplayName, 128, true) || !validText(input.Phone, 32, true) || len(input.Phone) < 7 {
		return Contact{}, ErrInvalid
	}
	if _, err := s.Repo.activeCustomer(ctx, p.TenantID, input.CustomerID); err != nil {
		return Contact{}, err
	}
	cipher, err := s.Protector.Encrypt(ctx, []byte(input.Phone))
	if err != nil {
		return Contact{}, err
	}
	value := Contact{Record: s.record(p), CustomerID: input.CustomerID, DisplayName: input.DisplayName, PhoneCipher: cipher, PhoneDigest: s.digest("contact-phone", input.Phone), PhoneMasked: mask(input.Phone), Status: "ACTIVE"}
	err = s.Repo.WithTransaction(ctx, func(tx context.Context) error {
		if err := s.Repo.tx(tx).Create(&value).Error; err != nil {
			return err
		}
		return s.audit(tx, p, "PORTAL_LOCAL_CONTACT_CREATED", "portal_local_contact", fmt.Sprint(value.ID))
	})
	return value, localPersistenceError(err)
}

// ListContacts 查询精确客户下的有界联系人及已有有效平台绑定。
// 参数：ctx 为管理会话，customer 为真实本地客户。
// 返回值：联系人列表或权限、客户不存在错误。
func (s *Service) ListContacts(ctx context.Context, customer uint64) ([]Contact, error) {
	p, err := s.principal(ctx, PermissionCustomerManage, false)
	if err != nil {
		return nil, err
	}
	if _, err = s.Repo.activeCustomer(ctx, p.TenantID, customer); err != nil {
		return nil, err
	}
	rows := []Contact{}
	err = s.Repo.tx(ctx).Where("tenant_id=? AND customer_id=?", p.TenantID, customer).Order("id").Limit(100).Find(&rows).Error
	return rows, err
}

// mask 生成账号或联系电话的有界脱敏展示。
// 参数：value 为只在服务器内处理的原文。
// 返回值：包含星号的脱敏内容。
func mask(value string) string {
	r := []rune(value)
	if len(r) <= 4 {
		return strings.Repeat("*", len(r))
	}
	return string(r[:2]) + strings.Repeat("*", len(r)-4) + string(r[len(r)-2:])
}

// localPersistenceError 将真实唯一性冲突保留为可理解的业务冲突。
// 参数：err为数据库结果，不吞掉其他存储错误。
// 返回值：明确冲突或原始错误。
func localPersistenceError(err error) error {
	var mysqlError *driver.MySQLError
	if errors.As(err, &mysqlError) && mysqlError.Number == 1062 {
		return ErrConflict
	}
	return err
}
