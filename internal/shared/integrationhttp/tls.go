// Package integrationhttp 构造出站服务集成共用的传输边界。它不允许跳过证书验证，并把客户端
// 私钥留在拥有该集成的进程内。
package integrationhttp

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// TLSOptions 描述可选私有信任根和客户端身份；客户端证书与私钥必须成对配置。
type TLSOptions struct {
	RootCAFile     string
	ClientCertFile string
	ClientKeyFile  string
	ServerName     string
	RequireMTLS    bool
}

// 只校验配置形状，不读取密钥文件；实际加载在启动构造 Transport 时完成并失败关闭。
func (o TLSOptions) Validate() error {
	for name, value := range map[string]string{
		"root CA file": o.RootCAFile, "client certificate file": o.ClientCertFile,
		"client key file": o.ClientKeyFile, "server name": o.ServerName,
	} {
		if value != strings.TrimSpace(value) {
			return fmt.Errorf("integration TLS %s must not contain surrounding whitespace", name)
		}
	}
	if (o.ClientCertFile == "") != (o.ClientKeyFile == "") {
		return errors.New("integration TLS client certificate and key must be configured together")
	}
	if o.RequireMTLS && o.ClientCertFile == "" {
		return errors.New("integration mTLS requires a client certificate and key")
	}
	if strings.ContainsAny(o.ServerName, "\x00\r\n/\\") {
		return errors.New("integration TLS server name is invalid")
	}
	return nil
}

// 防止把 TLS 策略误配到明文或 URL 内嵌凭据的端点。是否允许查询由调用方决定，但 userinfo 和
// fragment 永远不是合法服务身份的一部分。
func (o TLSOptions) ValidateEndpoints(rawURLs ...string) error {
	if err := o.Validate(); err != nil {
		return err
	}
	for _, raw := range rawURLs {
		parsed, err := url.ParseRequestURI(raw)
		if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
			return errors.New("integration endpoint is invalid")
		}
		if o.configured() && parsed.Scheme != "https" {
			return errors.New("integration TLS configuration requires HTTPS endpoints")
		}
	}
	return nil
}

// 加载当前信任根和客户端身份材料。调用方应在启动时一次性构造，使无效或不可读密钥在领取任何
// 业务事件前阻止部署。
func NewTransport(o TLSOptions, dialTimeout time.Duration) (*http.Transport, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	if dialTimeout <= 0 {
		return nil, errors.New("integration dial timeout must be positive")
	}
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("default HTTP transport has an unexpected type")
	}
	transport := base.Clone()
	transport.DialContext = (&net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}).DialContext
	transport.TLSHandshakeTimeout = 5 * time.Second
	transport.ResponseHeaderTimeout = 15 * time.Second
	transport.MaxIdleConnsPerHost = 16

	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: o.ServerName}
	if o.RootCAFile != "" {
		roots, err := x509.SystemCertPool()
		if err != nil || roots == nil {
			roots = x509.NewCertPool()
		}
		pemBytes, err := os.ReadFile(o.RootCAFile)
		if err != nil {
			return nil, fmt.Errorf("read integration TLS root CA: %w", err)
		}
		if !roots.AppendCertsFromPEM(pemBytes) {
			return nil, errors.New("integration TLS root CA contains no valid certificates")
		}
		tlsConfig.RootCAs = roots
	}
	if o.ClientCertFile != "" {
		certificate, err := tls.LoadX509KeyPair(o.ClientCertFile, o.ClientKeyFile)
		if err != nil {
			return nil, fmt.Errorf("load integration TLS client identity: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{certificate}
	}
	transport.TLSClientConfig = tlsConfig
	return transport, nil
}

func (o TLSOptions) configured() bool {
	return o.RootCAFile != "" || o.ClientCertFile != "" || o.ClientKeyFile != "" || o.ServerName != "" || o.RequireMTLS
}
