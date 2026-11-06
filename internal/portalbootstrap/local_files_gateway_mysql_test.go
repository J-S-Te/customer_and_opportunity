package portalbootstrap

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/filegatewayclient"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/localidentity"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
)

const jointTenant = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
const jointPortalApp = "01ARZ3NDEKTSV4RRFFQ69G5FAW"

// jointGatewayDSN 仅允许本轮新建随机回环空库，防止联合测试连接现有业务库。
// 参数：t为测试上下文，DSN仅从PORTAL_GATEWAY_TEST_DSN显式读取。
// 返回值：已验证DSN及连接；无配置跳过，不符合隔离边界失败。
func jointGatewayDSN(t *testing.T) (string, *sql.DB) {
	t.Helper()
	raw := os.Getenv("PORTAL_GATEWAY_TEST_DSN")
	if raw == "" {
		t.Skip("requires isolated PORTAL_GATEWAY_TEST_DSN; no connection attempted")
	}
	cfg, err := driver.ParseDSN(raw)
	if err != nil {
		t.Fatal("invalid joint test DSN")
	}
	host, portRaw, err := net.SplitHostPort(cfg.Addr)
	port, portErr := strconv.Atoi(portRaw)
	if err != nil || portErr != nil || cfg.Net != "tcp" || host != "127.0.0.1" || port < 32768 || !regexp.MustCompile(`^portal_gateway_test_[a-f0-9]{8}$`).MatchString(cfg.DBName) {
		t.Fatal("requires uniquely named disposable joint test database")
	}
	db, err := sql.Open("mysql", raw)
	if err != nil {
		t.Fatal("open joint test database failed")
	}
	t.Cleanup(func() { _ = db.Close() })
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE()").Scan(&count); err != nil || count != 0 {
		t.Fatal("joint test requires new empty schema")
	}
	return raw, db
}

// jointGatewayToken 签发仅供本轮网关验证的合成Ed25519机器令牌。
// 参数：t为测试上下文，key为临时私钥，client及scopes为明确机器用途。
// 返回值：短效JWT，不持久化、不输出，不冒充真实平台签发。
func jointGatewayToken(t *testing.T, key ed25519.PrivateKey, client string, scopes []string) string {
	t.Helper()
	now := time.Now().Unix()
	header, _ := json.Marshal(map[string]string{"alg": "EdDSA", "typ": "JWT"})
	payload, _ := json.Marshal(map[string]any{"iss": "portal-joint-test", "aud": "portal-joint-gateway", "token_use": "application", "sub": client, "oauth_client_id": "01ARZ3NDEKTSV4RRFFQ69G5FAX", "tenant_id": jointTenant, "application_id": jointPortalApp, "application_code": "customer_portal", "environment_id": "01ARZ3NDEKTSV4RRFFQ69G5FAY", "environment_code": "test", "scope": scopes, "iat": now, "nbf": now, "exp": now + 300})
	raw := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	return raw + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, []byte(raw)))
}

// jointGatewayProcess 构建并启动真实网关，只挂载本轮临时公钥和存储。
// 参数：t为测试上下文，dsn为独占空库，public为合成验证公钥。
// 返回值：随机回环地址；结束时精确终止本轮子进程，不触碰其他服务。
func jointGatewayProcess(t *testing.T, dsn string, public ed25519.PublicKey) string {
	t.Helper()
	temporary := t.TempDir()
	binary := filepath.Join(temporary, "file-gateway")
	module, err := filepath.Abs("../../../platform")
	if err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", binary, "./cmd/file-gateway")
	build.Dir = module
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build actual gateway: %v: %s", err, out)
	}
	der, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(temporary, "public.pem")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	command := exec.Command(binary)
	command.Dir = temporary
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(name, "FILE_GATEWAY_") && !strings.Contains(name, "DSN") {
			command.Env = append(command.Env, entry)
		}
	}
	command.Env = append(command.Env, "FILE_GATEWAY_DATABASE_DSN="+dsn, "FILE_GATEWAY_HTTP_ADDRESS="+address, "FILE_GATEWAY_STORAGE_BACKEND=local", "FILE_GATEWAY_STORAGE_ROOT="+filepath.Join(temporary, "store"), "FILE_GATEWAY_TOKEN_ISSUER=portal-joint-test", "FILE_GATEWAY_TOKEN_AUDIENCE=portal-joint-gateway", "FILE_GATEWAY_TOKEN_PUBLIC_KEY_PATH="+keyPath)
	var privateLogs bytes.Buffer
	command.Stdout = &privateLogs
	command.Stderr = &privateLogs
	if err := command.Start(); err != nil {
		t.Fatal("start actual joint gateway failed")
	}
	t.Cleanup(func() {
		_ = command.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { _ = command.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = command.Process.Kill()
			<-done
		}
	})
	base := "http://" + address
	client := &http.Client{Timeout: time.Second}
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		response, err := client.Get(base + "/readyz")
		if err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return base
			}
		}
	}
	t.Fatal("actual joint gateway not ready; private logs withheld")
	return ""
}

// jointPDF 生成最小有效合成PDF，内容与客户业务无关。
// 参数：无。
// 返回值：包含规范交叉引用及结束标记的单页PDF。
func jointPDF() []byte {
	objects := []string{"<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [3 0 R] /Count 1 >>", "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << >> /Contents 4 0 R >>", "<< /Length 0 >>\nstream\n\nendstream"}
	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n")
	offsets := []int{0}
	for i, object := range objects {
		offsets = append(offsets, out.Len())
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&out, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xref)
	return out.Bytes()
}

// TestLocalPortalAdapterActualGatewayMySQL 将正式门户适配器与真实网关进程串联。
// 参数：t为测试上下文，仅显式新空库，平台OAuth签发端为合成端口。
// 返回值：无；机器actor、协议、绑定、内容、重放或隔离不一致时失败。
func TestLocalPortalAdapterActualGatewayMySQL(t *testing.T) {
	dsn, db := jointGatewayDSN(t)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	base := jointGatewayProcess(t, dsn, public)
	oauth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, secret, _ := r.BasicAuth()
		_ = r.ParseForm()
		if r.URL.Path != "/oauth2/token" || user != "customer_portal-test-file-gateway-writer" || secret != "synthetic-secret" || r.Form.Get("scope") != "platform:file:upload platform:file:bind platform:file:download" {
			t.Error("unexpected formal Portal OAuth request")
			http.Error(w, "invalid test OAuth request", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": jointGatewayToken(t, private, user, strings.Fields(r.Form.Get("scope"))), "token_type": "Bearer", "expires_in": 300})
	}))
	defer oauth.Close()
	writer, err := newPortalFileGatewayClient(Config{FileGatewayBaseURL: base, PlatformBaseURL: oauth.URL, FileGatewayClientID: "customer_portal-test-file-gateway-writer", FileGatewayClientSecret: "synthetic-secret", FileGatewayScope: "platform:file:upload platform:file:bind platform:file:download"})
	if err != nil {
		t.Fatal(err)
	}
	files, err := NewLocalIdentityFiles(writer, jointPortalApp, jointPortalApp)
	if err != nil {
		t.Fatal(err)
	}
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{TenantID: jointTenant, UserID: "01ARZ3NDEKTSV4RRFFQ69G5FAZ", Roles: []string{"portal_super_admin"}, Permissions: map[string]struct{}{localidentity.PermissionReportManage: {}}})
	raw := jointPDF()
	descriptor, err := files.Upload(ctx, 123, "joint-operation", "joint-local.pdf", bytes.NewReader(raw))
	if err != nil {
		t.Fatal("actual Portal adapter upload failed:", err)
	}
	replay, err := files.Upload(ctx, 123, "joint-operation", "joint-local.pdf", bytes.NewReader(raw))
	if err != nil || replay.ObjectRef != descriptor.ObjectRef {
		t.Fatalf("replay lost original source: %v", err)
	}
	reader, err := filegatewayclient.New(base, &http.Client{Timeout: 5 * time.Second}, func(context.Context) (string, error) {
		return jointGatewayToken(t, private, "customer_portal-test-portal-report-source", []string{"platform:file:portal_report"}), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	input := filegatewayclient.PortalReportSourceInput{SourceType: "PORTAL_LOCAL", RequestID: 123, ExpectedSHA256: descriptor.FileHash, ExpectedSize: descriptor.Size, ExpectedMIME: descriptor.MIME, ExpectedName: descriptor.FileName}
	source, err := reader.OpenPortalReportFile(ctx, "joint-read", descriptor.ObjectRef, input)
	if err != nil {
		t.Fatal(err)
	}
	content, err := io.ReadAll(source.Content)
	_ = source.Content.Close()
	sum := sha256.Sum256(content)
	if err != nil || !bytes.Equal(content, raw) || descriptor.FileHash != hex.EncodeToString(sum[:]) {
		t.Fatal("actual immutable source bytes differ")
	}
	var actor string
	var sessions int
	if err := db.QueryRow("SELECT COUNT(*),MAX(COALESCE(actor_user_id,'')) FROM file_upload_v2_session WHERE file_id=?", descriptor.ObjectRef).Scan(&sessions, &actor); err != nil || sessions != 1 || actor != "" {
		t.Fatalf("machine source asserted a human or duplicated session: sessions=%d actor_empty=%v err=%v", sessions, actor == "", err)
	}
	input.RequestID = 124
	if source, err := reader.OpenPortalReportFile(ctx, "wrong-request", descriptor.ObjectRef, input); err == nil {
		if source.Content != nil {
			_ = source.Content.Close()
		}
		t.Fatal("different request released source")
	}
}
