package filegatewayclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// ReleaseBinding 仅停用原始业务元组。网关负责
// 授权、延迟绑定隔离及保留策略；此操作不删除文件字节。
func (client *Client) ReleaseBinding(ctx context.Context, requestID, applicationID, fileID, resourceType, resourceID, bindingType string) error {
	for _, value := range []string{requestID, applicationID, fileID, resourceType, resourceID, bindingType} {
		if strings.TrimSpace(value) == "" {
			return errors.New("file binding release identity is incomplete")
		}
	}
	payload, err := json.Marshal(map[string]string{"application_id": applicationID, "resource_type": resourceType, "resource_id": resourceID, "binding_type": bindingType})
	if err != nil {
		return err
	}
	return client.do(ctx, http.MethodPost, "/api/v1/files/"+url.PathEscape(fileID)+"/bindings/release", requestID, "application/json", bytes.NewReader(payload), nil)
}
