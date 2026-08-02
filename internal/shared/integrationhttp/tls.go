// Package integrationhttp builds the transport boundary shared by outbound
// service integrations. It never permits insecure certificate verification and
// keeps private client credentials in the process that owns the integration.
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

// TLSOptions describes optional private trust roots and a client identity.
// ClientCertFile and ClientKeyFile must always be configured as a pair.
type TLSOptions struct {
	RootCAFile     string
	ClientCertFile string
	ClientKeyFile  string
	ServerName     string
	RequireMTLS    bool
}

// Validate checks configuration shape without reading secret files.
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

// ValidateEndpoints prevents TLS policy from being accidentally attached to a
// clear-text or credential-bearing URL. Queries may be allowed by the caller;
// userinfo and fragments are never valid service identities.
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

// NewTransport loads the current trust and client identity material. Callers
// should build it once at process startup so invalid or unreadable secrets fail
// the deployment before any business event is claimed.
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
