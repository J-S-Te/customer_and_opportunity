package localidentity

import (
	"bytes"
	"encoding/json"
	"io"
)

// decodeMetadata 仅接受单个严格JSON对象，不忽略未知来源声明。
// 参数：raw为有界multipart元数据，target为明确请求DTO。
// 返回值：是否合法完整对象。
func decodeMetadata(raw string, target any) bool {
	value := bytes.TrimSpace([]byte(raw))
	if len(value) < 2 || len(value) > 16<<10 || value[0] != '{' || value[len(value)-1] != '}' {
		return false
	}
	d := json.NewDecoder(bytes.NewReader(value))
	d.DisallowUnknownFields()
	return d.Decode(target) == nil && d.Decode(&struct{}{}) == io.EOF
}
