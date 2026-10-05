package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"rillway/internal/config"
	"rillway/internal/proxy"
	"strings"
	"testing"
	"time"
)

func TestOwnedOriginBounds(t *testing.T) {
	h := origin()
	for _, path := range []string{"/bytes?bytes=0", "/bytes?bytes=8388609", "/etc/passwd", "/bytes?bytes=bad"} {
		r := httptest.NewRecorder()
		h.ServeHTTP(r, httptest.NewRequest(http.MethodGet, path, nil))
		if r.Code != http.StatusBadRequest {
			t.Fatalf("unbounded request accepted: %s", path)
		}
	}
	r := httptest.NewRecorder()
	h.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/bytes?bytes=1024", nil))
	if r.Code != http.StatusOK || r.Body.Len() != 1024 || r.Header().Get("Content-Length") != "1024" {
		t.Fatal("origin did not return exactly the requested payload")
	}
}

func TestProtocolsAgainstLocalRillway(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("NO_PROXY", "")
	origin := httptest.NewServer(origin())
	defer origin.Close()
	s, err := proxy.New(&net.Dialer{Timeout: time.Second}, config.Security{}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	front := httptest.NewServer(s.HTTPHandler())
	defer front.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.ServeSOCKS(ctx, listener) }()
	for _, protocol := range []string{"direct", "http", "connect", "socks5"} {
		t.Run(protocol, func(t *testing.T) {
			address := strings.TrimPrefix(front.URL, "http://")
			if protocol == "socks5" {
				address = listener.Addr().String()
			}
			r, err := run(ctx, options{target: origin.URL + "/bytes?bytes=64", proxy: address, protocol: protocol, workers: 1, duration: time.Second, fresh: true})
			if err != nil || r.Completed == 0 || r.Failures != 0 || r.Bytes < r.Completed*64 || r.Seconds > 6 {
				t.Fatalf("load failed or used environment proxy: %+v / %v", r, err)
			}
		})
	}
}

func TestSOCKSRejectsMalformedReply(t *testing.T) {
	for _, reply := range [][]byte{{4, 0}, {5, 255}, {5, 0, 5, 0, 0, 99}} {
		client, server := net.Pipe()
		_ = client.SetDeadline(time.Now().Add(time.Second))
		go func() {
			defer func() { _ = server.Close() }()
			var greeting [3]byte
			_, _ = io.ReadFull(server, greeting[:])
			_, _ = server.Write(reply[:2])
			if len(reply) > 2 {
				request := make([]byte, 5+len("example.test")+2)
				_, _ = io.ReadFull(server, request)
				// Write individual bytes to exercise fragmented network replies.
				for _, b := range reply[2:] {
					_, _ = server.Write([]byte{b})
				}
			}
		}()
		if err := socks(client, "example.test:80"); err == nil {
			t.Fatal("malformed SOCKS reply accepted")
		}
		_ = client.Close()
	}
}

func TestLoadRejectsUnboundedOrCredentialTargets(t *testing.T) {
	for _, o := range []options{
		{target: "http://user:secret@example.test", workers: 1, duration: time.Second},
		{target: "https://example.test", workers: 1, duration: time.Second},
		{target: "http://example.test", workers: 1025, duration: time.Second},
		{target: "http://example.test", workers: 1, duration: 6 * time.Minute},
		{target: "http://example.test", workers: 1, duration: time.Second, timeout: 2 * time.Minute},
	} {
		if _, err := run(context.Background(), o); err == nil {
			t.Fatal("invalid options accepted")
		}
	}
}

func TestPercentileCountsSuccessesOnly(t *testing.T) {
	histogram := []int64{0, 8, 1, 1}
	if percentile(histogram, 10, 50) != 1 || percentile(histogram, 10, 95) != 3 || percentile(histogram, 0, 95) != 0 {
		t.Fatal("incorrect percentile")
	}
}

func TestOverallDeadlineIsReportedAsCancellation(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "64")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer origin.Close()
	r, err := run(context.Background(), options{target: origin.URL, protocol: "direct", workers: 2, duration: time.Second})
	if err != nil || r.Completed != 0 || r.Failures != 0 || r.Cancelled != 2 {
		t.Fatalf("deadline was misreported: %+v / %v", r, err)
	}
}
