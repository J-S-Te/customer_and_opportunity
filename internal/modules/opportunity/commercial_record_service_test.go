package opportunity

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/audit"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
)

type commercialTestTransactionKey struct{}
type commercialTestStore struct {
	*GORMRepository
	parent      Opportunity
	visible     bool
	records     map[string]CommercialRecord
	links       map[uint64][]Attachment
	attachments map[string]Attachment
	idem        *ChangeIdempotency
	events      []audit.Event
	auditErr    error
	findIdem    int
}

// newCommercialTest 用途：创建不连接数据库的业务边界夹具。
// 参数：无。
// 返回值：真实台账服务、内存仓储及具有登记和附件读取权限的身份上下文。
func newCommercialTest() (*CommercialRecordService, *commercialTestStore, context.Context) {
	store := &commercialTestStore{GORMRepository: &GORMRepository{}, visible: true,
		parent: Opportunity{Model: database.Model{ID: 17, TenantID: "tenant-a", Version: 7},
			Status: StatusFollowing, CurrentStage: StageInitial, ExpectedAmount: decimal.NewFromInt(88)},
		records: map[string]CommercialRecord{}, links: map[uint64][]Attachment{}, attachments: map[string]Attachment{}}
	principal := auth.Principal{TenantID: "tenant-a", UserID: "actor-a", DisplayName: "登记人", Permissions: map[string]struct{}{
		"opportunity.read": {}, "opportunity.update": {}, "opportunity.attachment.read": {}}}
	return NewCommercialRecordService(store, store, store), store, auth.WithPrincipal(context.Background(), principal)
}

// commercialTestInput 用途：提供独立人工报价录入样本。
// 参数：无。
// 返回值：具有幂等键、业务日期和金额的有效登记请求。
func commercialTestInput() CommercialRecordRequest {
	amount := "123.4"
	return CommercialRecordRequest{Type: "QUOTATION", Title: "报价一", RecordDate: "2026-11-16", Amount: &amount, IdempotencyKey: "record-create-1"}
}

// FindByID 用途：模拟现有商机数据范围边界。
// 参数：ctx 为业务上下文，p 为可信主体，id 为父商机。
// 返回值：可见商机副本；跨租户、跨商机或不可见时返回不存在。
func (s *commercialTestStore) FindByID(ctx context.Context, p auth.Principal, id uint64) (*Opportunity, error) {
	if !s.visible || p.TenantID != s.parent.TenantID || id != s.parent.ID {
		return nil, ErrNotFound
	}
	v := s.parent
	return &v, nil
}

// FindByIDForUpdate 用途：要求写操作已进入事务后再读取父商机。
// 参数：ctx 为事务上下文，p 为可信主体，id 为商机编号。
// 返回值：可见商机；缺少事务标记或不可见时返回错误。
func (s *commercialTestStore) FindByIDForUpdate(ctx context.Context, p auth.Principal, id uint64) (*Opportunity, error) {
	if ctx.Value(commercialTestTransactionKey{}) != true {
		return nil, errors.New("parent lock outside transaction")
	}
	return s.FindByID(ctx, p, id)
}

// FindChangeIdempotencyForUpdate 用途：模拟操作者及父商机绑定的当前读。
// 参数：ctx 为事务，tenant、opp、operation、actor、key 为幂等坐标。
// 返回值：对应持久化响应；不匹配时为空。
func (s *commercialTestStore) FindChangeIdempotencyForUpdate(ctx context.Context, tenant string, opp uint64, operation, actor, key string) (*ChangeIdempotency, error) {
	s.findIdem++
	v := s.idem
	if v == nil || v.TenantID != tenant || v.OpportunityID != opp || v.Operation != operation || v.ActorID != actor || v.Key != key {
		return nil, nil
	}
	if ctx.Value(commercialTestTransactionKey{}) != true {
		return nil, errors.New("idempotency outside transaction")
	}
	return v, nil
}

// CreateChangeIdempotency 用途：模拟事务内保存创建快照。
// 参数：ctx 为事务上下文，value 为幂等响应及摘要。
// 返回值：成功为空；未使用事务则返回错误。
func (s *commercialTestStore) CreateChangeIdempotency(ctx context.Context, value *ChangeIdempotency) error {
	if ctx.Value(commercialTestTransactionKey{}) != true {
		return errors.New("idempotency outside transaction")
	}
	s.idem = value
	return nil
}

// WithTransaction 用途：模拟业务失败时台账、关联、幂等及审计共同回滚。
// 参数：ctx 为业务上下文，fn 为使用同一事务上下文的回调。
// 返回值：原始业务错误或成功。
func (s *commercialTestStore) WithTransaction(ctx context.Context, fn func(context.Context) error) error {
	records := map[string]CommercialRecord{}
	for k, v := range s.records {
		records[k] = v
	}
	links := map[uint64][]Attachment{}
	for k, v := range s.links {
		links[k] = append([]Attachment(nil), v...)
	}
	idem, count := s.idem, len(s.events)
	err := fn(context.WithValue(ctx, commercialTestTransactionKey{}, true))
	if err != nil {
		s.records, s.links, s.idem, s.events = records, links, idem, s.events[:count]
	}
	return err
}

// CreateRecord 用途：保存人工台账实体副本。
// 参数：ctx 为事务上下文，value 为新台账。
// 返回值：成功为空。
func (s *commercialTestStore) CreateRecord(ctx context.Context, value *CommercialRecord) error {
	value.ID = uint64(len(s.records) + 1)
	s.records[value.PublicID] = *value
	return nil
}

// FindRecord 用途：模拟按租户、父商机及不透明编号隔离的资料读取。
// 参数：ctx 为上下文，tenant 和 opp 为父范围，id 为资料编号，lock 为锁定标记。
// 返回值：台账副本或资料不存在错误。
func (s *commercialTestStore) FindRecord(ctx context.Context, tenant string, opp uint64, id string, lock bool) (*CommercialRecord, error) {
	v, ok := s.records[id]
	if !ok || v.TenantID != tenant || v.OpportunityID != opp {
		return nil, ErrCommercialRecordNotFound
	}
	return &v, nil
}

// ListRecords 用途：模拟台账分页的业务边界。
// 参数：ctx 为上下文，tenant 和 opp 为范围，page 和 size 为分页参数。
// 返回值：该范围内台账及总数。
func (s *commercialTestStore) ListRecords(ctx context.Context, tenant string, opp uint64, page, size int) ([]CommercialRecord, int64, error) {
	values := []CommercialRecord{}
	for _, v := range s.records {
		if v.TenantID == tenant && v.OpportunityID == opp {
			values = append(values, v)
		}
	}
	return values, int64(len(values)), nil
}

// SaveRecord 用途：模拟人工台账乐观锁更新。
// 参数：ctx 为上下文，value 为新状态，expected 为旧版本。
// 返回值：保存成功为空；版本错误时返回冲突。
func (s *commercialTestStore) SaveRecord(ctx context.Context, value *CommercialRecord, expected uint64) error {
	if s.records[value.PublicID].Version != expected {
		return ErrVersionConflict
	}
	value.Version = expected + 1
	s.records[value.PublicID] = *value
	return nil
}

// ResolveAttachments 用途：模拟可信附件的租户和父商机筛选。
// 参数：ctx 为上下文，tenant 和 opp 为父范围，ids 为所选附件，lock 为锁定标记。
// 返回值：匹配的附件实体列表。
func (s *commercialTestStore) ResolveAttachments(ctx context.Context, tenant string, opp uint64, ids []string, lock bool) ([]Attachment, error) {
	values := []Attachment{}
	for _, id := range ids {
		v, ok := s.attachments[id]
		if ok && v.TenantID == tenant && v.OpportunityID == opp {
			values = append(values, v)
		}
	}
	return values, nil
}

// ReplaceRecordAttachments 用途：替换台账自己的附件引用。
// 参数：ctx 为事务上下文，value 为台账，attachments 为已验证可信附件。
// 返回值：成功为空。
func (s *commercialTestStore) ReplaceRecordAttachments(ctx context.Context, value *CommercialRecord, attachments []Attachment) error {
	s.links[value.ID] = append([]Attachment(nil), attachments...)
	return nil
}

// ListRecordAttachments 用途：按批次读取资料的附件引用。
// 参数：ctx 为上下文，tenant 和 opp 为范围，ids 为台账内部主键。
// 返回值：台账主键到附件列表的映射。
func (s *commercialTestStore) ListRecordAttachments(ctx context.Context, tenant string, opp uint64, ids []uint64) (map[uint64][]Attachment, error) {
	values := map[uint64][]Attachment{}
	for _, id := range ids {
		values[id] = s.links[id]
	}
	return values, nil
}

// Write 用途：要求真实业务服务把审计写入同一事务。
// 参数：ctx 为事务上下文，event 为操作者及前后状态快照。
// 返回值：预设审计错误或成功。
func (s *commercialTestStore) Write(ctx context.Context, event audit.Event) error {
	if ctx.Value(commercialTestTransactionKey{}) != true {
		return errors.New("audit outside transaction")
	}
	if s.auditErr != nil {
		return s.auditErr
	}
	s.events = append(s.events, event)
	return nil
}

// TestCommercialRecordLifecycleAndReplay 用途：防止资料变更误改父商机或创建重试重复写入。
// 参数：t 为测试上下文。
// 返回值：无；创建、更正、精确重放、作废及审计不满足契约时失败。
func TestCommercialRecordLifecycleAndReplay(t *testing.T) {
	service, store, ctx := newCommercialTest()
	input := commercialTestInput()
	first, err := service.Create(ctx, 17, input)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == "" || first.Version != 1 || first.SourceKind != "MANUAL" || first.CreatedBy != "actor-a" || first.Amount == nil || *first.Amount != "123.40" {
		t.Fatalf("created=%+v", first)
	}
	changed := input
	changed.Title = "更正报价"
	changed.Status = "CANCELLED"
	changed.Version = 1
	second, err := service.Update(ctx, 17, first.ID, changed)
	if err != nil {
		t.Fatal(err)
	}
	if second.Version != 2 || second.IsVoid || second.Status != "CANCELLED" {
		t.Fatalf("updated=%+v", second)
	}
	replay, err := service.Create(ctx, 17, input)
	if err != nil || replay.Version != 1 || replay.Title != "报价一" || len(store.events) != 2 {
		t.Fatalf("replay=%+v err=%v events=%d", replay, err, len(store.events))
	}
	input.Title = "变更载荷"
	if _, err = service.Create(ctx, 17, input); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("payload conflict=%v", err)
	}
	if _, err = service.Update(ctx, 17, first.ID, changed); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("version conflict=%v", err)
	}
	last, err := service.Void(ctx, 17, first.ID, CommercialRecordVoidRequest{Version: 2, Reason: "录入错误"})
	if err != nil || !last.IsVoid || last.Version != 3 || last.VoidReason != "录入错误" {
		t.Fatalf("void=%+v err=%v", last, err)
	}
	if _, err = service.Void(ctx, 17, first.ID, CommercialRecordVoidRequest{Version: 2, Reason: "录入错误"}); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("duplicate void=%v", err)
	}
	if _, err = service.Update(ctx, 17, first.ID, CommercialRecordRequest{Version: 3}); !errors.Is(err, ErrCommercialRecordVoid) {
		t.Fatalf("void update=%v", err)
	}
	page, err := service.List(ctx, 17, 1, 20)
	if err != nil || page.Total != 1 || len(page.Items) != 1 || !page.Items[0].IsVoid {
		t.Fatalf("list=%+v err=%v", page, err)
	}
	if store.parent.Version != 7 || store.parent.CurrentStage != StageInitial || !store.parent.ExpectedAmount.Equal(decimal.NewFromInt(88)) {
		t.Fatalf("parent changed=%+v", store.parent)
	}
	if len(store.events) != 3 || len(store.events[1].BeforeJSON) == 0 || store.events[2].Reason != "录入错误" || store.events[0].ActorNameSnapshot != "登记人" {
		t.Fatalf("audit=%+v", store.events)
	}
}

// TestCommercialRecordAttachmentGuards 用途：阻止未经读取授权或不可信、跨范围附件进入台账。
// 参数：t 为测试上下文。
// 返回值：无；非法关联成功或附件权限屏蔽失效时失败。
func TestCommercialRecordAttachmentGuards(t *testing.T) {
	for _, tt := range []struct {
		name, tenant, status string
		opp                  uint64
		permission           bool
		want                 error
	}{
		{"clean", "tenant-a", AttachmentClean, 17, true, nil},
		{"pending", "tenant-a", AttachmentScanning, 17, true, ErrAttachmentNotReady},
		{"tenant", "tenant-b", AttachmentClean, 17, true, ErrAttachmentNotFound},
		{"parent", "tenant-a", AttachmentClean, 18, true, ErrAttachmentNotFound},
		{"permission", "tenant-a", AttachmentClean, 17, false, apperror.ErrForbidden},
	} {
		t.Run(tt.name, func(t *testing.T) {
			service, store, ctx := newCommercialTest()
			input := commercialTestInput()
			input.AttachmentIDs = []string{"attachment-a"}
			store.attachments["attachment-a"] = Attachment{Model: database.Model{ID: 8, TenantID: tt.tenant, Version: 1}, PublicID: "attachment-a", OpportunityID: tt.opp, ScanStatus: tt.status, FileName: "资料.pdf"}
			if !tt.permission {
				p, _ := auth.FromContext(ctx)
				delete(p.Permissions, "opportunity.attachment.read")
				ctx = auth.WithPrincipal(ctx, p)
			}
			result, err := service.Create(ctx, 17, input)
			if !errors.Is(err, tt.want) {
				t.Fatalf("result=%+v err=%v want=%v", result, err, tt.want)
			}
			if tt.want != nil {
				if len(store.records) != 0 || len(store.events) != 0 || store.idem != nil {
					t.Fatal("rejected write left data")
				}
				return
			}
			if len(result.Attachments) != 1 || result.Attachments[0].ID != "attachment-a" {
				t.Fatalf("attachments=%+v", result.Attachments)
			}
			p, _ := auth.FromContext(ctx)
			delete(p.Permissions, "opportunity.attachment.read")
			ctx = auth.WithPrincipal(ctx, p)
			list, err := service.List(ctx, 17, 1, 20)
			if err != nil || len(list.Items[0].Attachments) != 0 {
				t.Fatalf("attachment disclosure=%+v err=%v", list, err)
			}
		})
	}
}

// TestCommercialRecordParentAndPermissionGuards 用途：确保父商机可见性优先于幂等重放。
// 参数：t 为测试上下文。
// 返回值：无；跨范围、作废商机或缺权限写入成功时失败。
func TestCommercialRecordParentAndPermissionGuards(t *testing.T) {
	service, store, ctx := newCommercialTest()
	input := commercialTestInput()
	first, err := service.Create(ctx, 17, input)
	if err != nil {
		t.Fatal(err)
	}
	checks := store.findIdem
	store.visible = false
	if _, err = service.Create(ctx, 17, input); !errors.Is(err, ErrNotFound) {
		t.Fatalf("invisible replay=%v", err)
	}
	if store.findIdem != checks {
		t.Fatal("invisible parent probed idempotency")
	}
	if _, err = service.List(ctx, 17, 1, 20); !errors.Is(err, ErrNotFound) {
		t.Fatalf("invisible list=%v", err)
	}
	store.visible = true
	store.parent.Status = StatusVoid
	if _, err = service.Create(ctx, 17, input); !errors.Is(err, ErrInactive) {
		t.Fatalf("void parent replay=%v", err)
	}
	if _, err = service.Update(ctx, 17, first.ID, input); !errors.Is(err, ErrInactive) {
		t.Fatalf("void parent update=%v", err)
	}
	store.parent.Status = StatusFollowing
	p, _ := auth.FromContext(ctx)
	delete(p.Permissions, "opportunity.update")
	ctx = auth.WithPrincipal(ctx, p)
	if _, err = service.Create(ctx, 17, input); !errors.Is(err, apperror.ErrForbidden) {
		t.Fatalf("missing write permission=%v", err)
	}
	if _, err = service.List(context.Background(), 17, 1, 20); !errors.Is(err, apperror.ErrUnauthenticated) {
		t.Fatalf("unauthenticated=%v", err)
	}
}

// TestCommercialRecordAuditFailureRollsBack 用途：验证业务服务遇审计错误时回滚资料和幂等结果。
// 参数：t 为测试上下文。
// 返回值：无；审计失败留下部分业务状态时失败。
func TestCommercialRecordAuditFailureRollsBack(t *testing.T) {
	service, store, ctx := newCommercialTest()
	failure := errors.New("audit unavailable")
	store.auditErr = failure
	if _, err := service.Create(ctx, 17, commercialTestInput()); !errors.Is(err, failure) {
		t.Fatalf("audit failure=%v", err)
	}
	if len(store.records) != 0 || len(store.links) != 0 || store.idem != nil || len(store.events) != 0 {
		t.Fatalf("partial create=%+v", store)
	}
	store.auditErr = nil
	first, err := service.Create(ctx, 17, commercialTestInput())
	if err != nil {
		t.Fatal(err)
	}
	store.auditErr = failure
	input := commercialTestInput()
	input.Title = "不能保存"
	input.Version = 1
	if _, err = service.Update(ctx, 17, first.ID, input); !errors.Is(err, failure) {
		t.Fatalf("update audit failure=%v", err)
	}
	if store.records[first.ID].Title != "报价一" || store.records[first.ID].Version != 1 {
		t.Fatal("audit failure persisted update")
	}
	if _, err = service.Void(ctx, 17, first.ID, CommercialRecordVoidRequest{Version: 1, Reason: "错误"}); !errors.Is(err, failure) {
		t.Fatalf("void audit failure=%v", err)
	}
	if store.records[first.ID].IsVoid || store.records[first.ID].Version != 1 {
		t.Fatal("audit failure persisted void")
	}
}

// TestCommercialRecordInvalidCommands 用途：阻止遗漏幂等键、无版本及无作废说明的写入。
// 参数：t 为测试上下文。
// 返回值：无；无效命令被接受时失败。
func TestCommercialRecordInvalidCommands(t *testing.T) {
	service, _, ctx := newCommercialTest()
	input := commercialTestInput()
	input.IdempotencyKey = ""
	if _, err := service.Create(ctx, 17, input); !errors.Is(err, ErrIdempotencyRequired) {
		t.Fatalf("missing key=%v", err)
	}
	input.IdempotencyKey = string(make([]byte, 129))
	if _, err := service.Create(ctx, 17, input); !errors.Is(err, ErrIdempotencyKeyTooLong) {
		t.Fatalf("long key=%v", err)
	}
	input = commercialTestInput()
	first, err := service.Create(ctx, 17, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Update(ctx, 17, first.ID, input); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("missing version=%v", err)
	}
	if _, err = service.Update(ctx, 17, "foreign-record", input); !errors.Is(err, ErrCommercialRecordNotFound) {
		t.Fatalf("unknown record=%v", err)
	}
	if _, err = service.Void(ctx, 17, first.ID, CommercialRecordVoidRequest{Version: 1}); !errors.Is(err, ErrCommercialRecordInvalid) {
		t.Fatalf("missing reason=%v", err)
	}
	if _, err = service.List(ctx, 17, 0, 20); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("invalid pagination=%v", err)
	}
}

// TestCommercialRecordSnapshotIsExact 用途：验证幂等响应实际保存可解码的创建时快照。
// 参数：t 为测试上下文。
// 返回值：无；快照缺少金额、人工来源或主体绑定时失败。
func TestCommercialRecordSnapshotIsExact(t *testing.T) {
	service, store, ctx := newCommercialTest()
	first, err := service.Create(ctx, 17, commercialTestInput())
	if err != nil {
		t.Fatal(err)
	}
	var snapshot CommercialRecordResponse
	if err = json.Unmarshal(store.idem.ResponseJSON, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.ID != first.ID || snapshot.SourceKind != "MANUAL" || store.idem.ActorID != "actor-a" || store.idem.OpportunityID != 17 || store.idem.Operation != "COMMERCIAL_RECORD_CREATE" {
		t.Fatalf("snapshot=%+v idem=%+v", snapshot, store.idem)
	}
}
