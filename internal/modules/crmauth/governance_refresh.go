package crmauth

// errRefreshRejected 兼容中央会话轮换的错误分类，统一使用既有OIDC明确拒绝判定。
var errRefreshRejected = errOIDCRefreshRejected
