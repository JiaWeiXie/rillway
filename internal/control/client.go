package control

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"rillway/internal/config"
	"rillway/internal/outbound"
	"strings"
	"time"
)

type Client struct {
	base  string
	token string
	http  *http.Client
}
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("管理 API (%d)：%s", e.Status, e.Message) }

// NewClient verifies TLS normally; caFile adds a local CA without disabling verification.
func NewClient(baseURL, token, caFile string) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, fmt.Errorf("管理網址格式不正確")
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		local := u.Hostname() == "localhost" || ip != nil && ip.IsLoopback()
		if u.Scheme != "http" || !local {
			return nil, fmt.Errorf("遠端管理網址必須使用 HTTPS")
		}
	}
	tc := &tls.Config{MinVersion: tls.VersionTLS12}
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("讀取 CA 憑證：%w", err)
		}
		pool, _ := x509.SystemCertPool()
		if pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("CA 憑證不含有效的 PEM certificate")
		}
		tc.RootCAs = pool
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tc
	transport.Proxy = nil // Management must not recursively use Rillway's own proxy.
	return &Client{base: strings.TrimRight(baseURL, "/"), token: token, http: &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (c *Client) request(ctx context.Context, method, path string, body, dst any) error {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return err
		}
	}
	r, err := http.NewRequestWithContext(ctx, method, c.base+path, &buf)
	if err != nil {
		return err
	}
	r.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(r)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	reader := io.LimitReader(res.Body, 8<<20)
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(reader).Decode(&e)
		if e.Error == "" {
			e.Error = http.StatusText(res.StatusCode)
		}
		return &APIError{Status: res.StatusCode, Message: e.Error}
	}
	if dst != nil {
		return json.NewDecoder(reader).Decode(dst)
	}
	return nil
}

func (c *Client) Config(ctx context.Context) (config.Config, error) {
	var v config.Config
	err := c.request(ctx, "GET", "/api/v1/config", nil, &v)
	return v, err
}

func (c *Client) Apply(ctx context.Context, cfg config.Config) (config.Config, error) {
	var v config.Config
	err := c.request(ctx, "PUT", "/api/v1/config", cfg, &v)
	return v, err
}

func (c *Client) Snapshot(ctx context.Context) (json.RawMessage, error) {
	var v json.RawMessage
	err := c.request(ctx, "GET", "/api/v1/stats", nil, &v)
	return v, err
}

func (c *Client) Statuses(ctx context.Context) ([]outbound.Status, error) {
	var v []outbound.Status
	err := c.request(ctx, "GET", "/api/v1/outbounds", nil, &v)
	return v, err
}

func (c *Client) Action(ctx context.Context, id, action, value string) error {
	return c.request(ctx, "POST", "/api/v1/outbounds/"+url.PathEscape(id)+"/"+url.PathEscape(action), map[string]string{"value": value}, nil)
}
