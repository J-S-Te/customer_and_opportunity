package presale

import (
	"context"
	"time"
)

// 售前模块只能通过该端口读取商机，具体实现必须校验传入 Actor 的商机数据范围。
type OpportunityReader interface {
	GetAccessible(ctx context.Context, actor Actor, opportunityID uint64) (OpportunitySnapshot, error)
}

// 电话加密、解密和脱敏策略由基础设施实现，领域模型不持有密钥细节。
type PhoneProtector interface {
	Encrypt(ctx context.Context, plaintext string) ([]byte, error)
	Decrypt(ctx context.Context, ciphertext []byte) (string, error)
	Mask(plaintext string) string
}

type Clock interface{ Now() time.Time }

type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now().UTC() }

// 事件与任务标识必须是不透明且抗碰撞的，不能暴露数据库自增规律。
type IDGenerator interface{ NewID() string }

// ActorResolver 将宿主应用已认证上下文转换为售前模块可信主体，租户和权限不从请求体读取。
type ActorResolver interface {
	Resolve(ctx context.Context) (Actor, error)
}

// 审批命令先写入 Outbox，事务提交后再由 worker 调用真实审批引擎，
// 避免数据库回滚后外部审批已经发生的双写不一致。
type ApprovalCommandPort interface {
	Start(ctx context.Context, event OutboxEvent) (ApprovalStartResult, error)
	Act(ctx context.Context, event OutboxEvent) error
}

type ApprovalStartResult struct {
	EngineInstanceID string
	EventSequence    uint64
	// NextApproverID/NextApproverName 是审批引擎返回的兼容展示信息；业务服务仍会
	// 通过基础平台角色目录解析当前有效审批人，不能把引擎快照直接作为通知授权依据。
	NextApproverID   string
	NextApproverName string
}

// PMS 推送只定义领域端口；机器认证、超时和具体 MQ/HTTP 协议由生产基础设施负责。
type PMSPublisher interface {
	PublishWorklog(ctx context.Context, event OutboxEvent) (responseCode string, err error)
}
