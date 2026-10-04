// Package diagnostic performs explicitly requested, bounded network checks.
package diagnostic

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"rillway/internal/config"
	"rillway/internal/outbound"
	"strings"
	"time"
)

type Result struct {
	Host           string            `json:"host"`
	Outbound       string            `json:"outbound"`
	Family         string            `json:"requested_family"`
	DNSIPs         []string          `json:"dns_ips,omitempty"`
	DestinationIP  string            `json:"destination_ip,omitempty"`
	ConnectMillis  float64           `json:"connect_ms"`
	TLSMillis      float64           `json:"tls_ms"`
	Status         int               `json:"http_status,omitempty"`
	CDN            map[string]string `json:"response_metadata,omitempty"`
	Bytes          int64             `json:"download_bytes,omitempty"`
	BytesPerSecond float64           `json:"download_bytes_per_second,omitempty"`
	Error          string            `json:"error,omitempty"`
}

func Run(ctx context.Context, c config.Config, id, family, downloadURL string) ([]Result, error) {
	if family != "auto" && family != "ipv4" && family != "ipv6" {
		return nil, errors.New("family must be auto, ipv4 or ipv6")
	}
	var selected *config.Outbound
	for _, o := range c.Outbounds {
		if o.ID == id {
			selected = &o
			break
		}
	}
	if selected == nil {
		return nil, errors.New("unknown outbound")
	}
	p, err := outbound.New(ctx, *selected)
	if err != nil {
		return nil, err
	}
	defer func() { _ = p.Close() }()
	urls := []string{"https://github.com/", "https://api.github.com/", "https://codeload.github.com/", "https://raw.githubusercontent.com/", "https://avatars.githubusercontent.com/", "https://github.githubassets.com/"}
	download := downloadURL != ""
	if download {
		u, e := url.Parse(downloadURL)
		if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
			return nil, errors.New("download requires an explicit HTTPS URL without credentials")
		}
		urls = []string{downloadURL}
	}
	results := make([]Result, 0, len(urls))
	for _, target := range urls {
		if ctx.Err() != nil {
			return results, ctx.Err()
		}
		results = append(results, Check(ctx, p, family, target, download))
	}
	return results, nil
}

// Check reports DNS only when the selected provider exposes resolution through
// httptrace. An upstream SOCKS hostname is deliberately not resolved locally.
func Check(ctx context.Context, p outbound.Provider, family, target string, download bool) Result {
	u, err := url.Parse(target)
	if err != nil {
		return Result{Error: "invalid URL"}
	}
	result := Result{Host: u.Hostname(), Outbound: p.ID(), Family: family}
	network := "tcp"
	if family == "ipv4" {
		network = "tcp4"
	}
	if family == "ipv6" {
		network = "tcp6"
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var tlsStart time.Time
	trace := &httptrace.ClientTrace{
		DNSDone: func(info httptrace.DNSDoneInfo) {
			for _, a := range info.Addrs {
				result.DNSIPs = append(result.DNSIPs, a.IP.String())
			}
		},
		TLSHandshakeStart: func() { tlsStart = time.Now() },
		TLSHandshakeDone: func(tls.ConnectionState, error) {
			result.TLSMillis = float64(time.Since(tlsStart).Microseconds()) / 1000
		},
	}
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, ForceAttemptHTTP2: false, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
		start := time.Now()
		conn, e := p.DialContext(ctx, network, addr)
		result.ConnectMillis = float64(time.Since(start).Microseconds()) / 1000
		if e == nil {
			if known, ok := conn.(interface{ DestinationIP() string }); ok {
				result.DestinationIP = known.DestinationIP()
			} else {
				host, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
				if net.ParseIP(host) != nil {
					result.DestinationIP = host
				}
			}
		}
		return conn, e
	}}
	defer transport.CloseIdleConnections()
	client := http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	method := "HEAD"
	if download {
		method = "GET"
	}
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), method, target, nil)
	if err != nil {
		result.Error = "invalid request"
		return result
	}
	req.Header.Set("User-Agent", "Rillway-Diagnostic/0.1")
	if download {
		req.Header.Set("Range", "bytes=0-4194303")
		req.Header.Set("Accept-Encoding", "identity")
	}
	start := time.Now()
	res, err := client.Do(req)
	if err != nil {
		result.Error = "connection or TLS verification failed"
		if ctx.Err() != nil {
			result.Error = ctx.Err().Error()
		}
		return result
	}
	defer func() { _ = res.Body.Close() }()
	result.Status = res.StatusCode
	result.CDN = map[string]string{}
	for _, key := range []string{"Server", "Via", "X-Served-By", "X-Cache", "CF-Ray"} {
		if v := res.Header.Get(key); v != "" {
			result.CDN[key] = strings.TrimSpace(v)
		}
	}
	if download {
		if res.StatusCode != 200 && res.StatusCode != 206 {
			result.Error = fmt.Sprintf("download not measured: HTTP %d (redirects are not followed)", res.StatusCode)
			return result
		}
		result.Bytes, err = io.Copy(io.Discard, io.LimitReader(res.Body, 4<<20))
		result.BytesPerSecond = float64(result.Bytes) / time.Since(start).Seconds()
		if err != nil {
			result.Error = "download interrupted"
		}
	}
	return result
}
