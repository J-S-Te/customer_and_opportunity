package idempotency

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
)

// DecodeSnapshot 严格解析响应快照并兼容 MySQL JSON 字段顺序和空白规范化。
// 参数：raw 为已存响应，digest 为创建时摘要，target 为原响应 DTO 的非空指针。
// 返回值：原始字节或原 DTO 重新编码的摘要匹配时成功，其余返回快照无效错误。
func DecodeSnapshot(raw []byte, digest string, target any) error {
	invalid := errors.New("idempotency response snapshot is invalid")
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return invalid
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return invalid
	}
	original := sha256.Sum256(raw)
	if hex.EncodeToString(original[:]) == digest {
		return nil
	}
	// MySQL JSON 会重排键和增加空白，不能把这种存储变化误判为载荷变更。
	// 按创建时同一 DTO 编码，保留字段值和数值类型，不能跳过摘要验证。
	canonical, err := json.Marshal(target)
	if err != nil {
		return invalid
	}
	computed := sha256.Sum256(canonical)
	if hex.EncodeToString(computed[:]) != digest {
		return invalid
	}
	return nil
}
