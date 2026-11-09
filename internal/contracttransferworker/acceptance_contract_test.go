package contracttransferworker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// TestContractIntakeAcceptanceCrossRepository 消费合同真实Handler生成的受理回执。
// 参数：t为测试上下文，CRM_INTAKE_ACK_CONTRACT_FILE指定跨仓交换文件。
// 返回值：无；合法已审核回执被投递为永久失败或身份不匹配被接受时失败。
func TestContractIntakeAcceptanceCrossRepository(t *testing.T) {
	path := os.Getenv("CRM_INTAKE_ACK_CONTRACT_FILE")
	if path == "" {
		t.Skip("仅由跨仓契约驱动脚本执行")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var wires []json.RawMessage
	if err = json.Unmarshal(raw, &wires); err != nil {
		t.Fatal(err)
	}
	if len(wires) != 3 {
		t.Fatalf("接收端回执数量=%d", len(wires))
	}
	for i, wire := range wires {
		t.Run(string(rune('0'+i)), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusAccepted); _, _ = w.Write(wire) }))
			defer server.Close()
			client := &contractClient{client: server.Client(), url: server.URL, now: time.Now, nonce: func() (string, error) { return "nonce-7", nil }}
			result, err := client.deliver(context.Background(), signedCommand{EventID: "event-7", TenantID: "tenant-a"})
			if err != nil || result.IntakeID != "intake-7" || result.Status != "ACCEPTED" {
				t.Fatalf("接收端真实回执不能消费: result=%+v err=%v body=%s", result, err, wire)
			}
		})
	}
}

// TestContractIntakeACKRejectsWrongIdentity 验证受理状态不能替代事件和接入身份。
// 参数：t为测试上下文，分别破坏事件ID、接入ID和受理状态。
// 返回值：无；不可信回执被接受时失败。
func TestContractIntakeACKRejectsWrongIdentity(t *testing.T) {
	for _, data := range []string{`{"intake_id":"intake-7","event_id":"other","status":"ACCEPTED"}`, `{"intake_id":"","event_id":"event-7","status":"ACCEPTED"}`, `{"intake_id":"intake-7","event_id":"event-7","status":"UNKNOWN"}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"code":"OK","data":` + data + `}`))
		}))
		client := &contractClient{client: server.Client(), url: server.URL, now: time.Now, nonce: func() (string, error) { return "nonce-7", nil }}
		_, err := client.deliver(context.Background(), signedCommand{EventID: "event-7", TenantID: "tenant-a"})
		server.Close()
		var permanent permanentDeliveryError
		if !errors.As(err, &permanent) {
			t.Fatalf("不可信回执未拒绝: %s err=%v", data, err)
		}
	}
}
