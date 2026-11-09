package idempotency

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// TestDecodeSnapshotStorageNormalization 验证存储格式变化可恢复而业务值篡改仍被拒绝。
// 参数：t 为测试上下文；摘要来源为创建端原始 DTO 字节。
// 返回值：原样与 MySQL 规范化响应通过；值变更、未知字段或多对象响应拒绝。
func TestDecodeSnapshotStorageNormalization(t *testing.T) {
	original := []byte(`{"id":7,"name":"历史合同","amount":"50000.00"}`)
	digest := sha256.Sum256(original)
	for _, tc := range []struct {
		name, stored string
		valid        bool
	}{
		{"original", string(original), true},
		{"mysql-order", `{"id": 7, "name": "历史合同", "amount": "50000.00"}`, true},
		{"reordered", `{"amount":"50000.00","name":"历史合同","id":7}`, true},
		{"changed-id", `{"id":8,"name":"历史合同","amount":"50000.00"}`, false},
		{"changed-money", `{"id":7,"name":"历史合同","amount":"50000.01"}`, false},
		{"unknown-field", `{"id":7,"name":"历史合同","amount":"50000.00","extra":1}`, false},
		{"trailing-object", string(original) + `{}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var result struct {
				ID     uint64 `json:"id"`
				Name   string `json:"name"`
				Amount string `json:"amount"`
			}
			err := DecodeSnapshot([]byte(tc.stored), hex.EncodeToString(digest[:]), &result)
			if (err == nil) != tc.valid {
				t.Fatalf("snapshot valid=%t err=%v", tc.valid, err)
			}
		})
	}
}
