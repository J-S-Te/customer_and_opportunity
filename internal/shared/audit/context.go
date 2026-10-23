package audit

import "context"

// Meta 是请求级审计来源信息（AUD-IP-2026）。
//
// 业务写操作通常在服务层完成，那里没有 *http.Request；请求入口把解析好的
// 三类地址放进上下文，业务审计写入时再取出，避免"业务事件没有任何地址"。
type Meta struct {
	LoginIP, ClientIP, IPSource string
}

type metaKey struct{}

// WithMeta 写入来源信息。
func WithMeta(ctx context.Context, meta Meta) context.Context {
	return context.WithValue(ctx, metaKey{}, meta)
}

// MetaFrom 读取来源信息；不存在时返回零值。
func MetaFrom(ctx context.Context) Meta {
	meta, _ := ctx.Value(metaKey{}).(Meta)
	return meta
}
