// loadtest is an explicitly enabled, bounded load generator for owned test hosts.
// It never discovers targets, uses environment proxies, or prints destinations.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"time"
)

const maxPayload = 8 << 20

type options struct {
	target, proxy, protocol string
	workers                 int
	duration                time.Duration
	timeout                 time.Duration
	fresh                   bool
}

type result struct {
	Protocol       string  `json:"protocol"`
	Workers        int     `json:"workers"`
	Fresh          bool    `json:"fresh_connections"`
	Seconds        float64 `json:"seconds"`
	TimeoutSeconds float64 `json:"timeout_seconds"`
	LatencyCapMS   int     `json:"latency_cap_ms"`
	LatencyCapped  bool    `json:"latency_capped"`
	Completed      int64   `json:"completed"`
	Failures       int64   `json:"failures"`
	Cancelled      int64   `json:"cancelled_at_deadline"`
	Bytes          int64   `json:"bytes"`
	Mbps           float64 `json:"mbps"`
	RequestsSecond float64 `json:"requests_per_second"`
	P50MS          int     `json:"p50_ms_upper_bound"`
	P95MS          int     `json:"p95_ms_upper_bound"`
}

func main() {
	owned := flag.Bool("owned-target", false, "Required: confirm every endpoint is an owned disposable test host")
	listen := flag.String("listen", "", "Run an origin server at host:port instead of generating load")
	var o options
	flag.StringVar(&o.target, "target", "", "Owned HTTP origin URL; responses are discarded")
	flag.StringVar(&o.proxy, "proxy", "", "Proxy host:port (not printed)")
	flag.StringVar(&o.protocol, "protocol", "direct", "direct, http, connect, or socks5")
	flag.IntVar(&o.workers, "workers", 8, "Concurrent workers (1..1024)")
	flag.DurationVar(&o.duration, "duration", 15*time.Second, "Duration (1s..5m)")
	flag.DurationVar(&o.timeout, "timeout", 15*time.Second, "Per-request timeout (100ms..1m); also bounds tunnel setup")
	flag.BoolVar(&o.fresh, "fresh", false, "Use a new TCP connection per request")
	flag.Parse()
	if !*owned {
		fmt.Fprintln(os.Stderr, "Refusing load without --owned-target")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if *listen != "" {
		s := &http.Server{Addr: *listen, Handler: origin(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
		go func() {
			<-ctx.Done()
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = s.Shutdown(shutdown)
		}()
		if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintln(os.Stderr, "Origin listener failed")
			os.Exit(1)
		}
		return
	}
	r, err := run(ctx, o)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Invalid load configuration or target")
		os.Exit(2)
	}
	if err := json.NewEncoder(os.Stdout).Encode(r); err != nil {
		os.Exit(1)
	}
	if r.Completed == 0 || r.Failures > 0 {
		os.Exit(1)
	}
}

// Origin has no filesystem access and allocates one shared, bounded payload.
func origin() http.Handler {
	payload := bytes.Repeat([]byte("rillway-test-data"), maxPayload/17+1)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, err := strconv.Atoi(r.URL.Query().Get("bytes"))
		if r.Method != http.MethodGet || r.URL.Path != "/bytes" || err != nil || n < 1 || n > maxPayload {
			http.Error(w, "Use GET /bytes?bytes=1..8388608", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(n))
		_, _ = io.CopyN(w, bytes.NewReader(payload), int64(n))
	})
}

func transport(o options) (*http.Transport, error) {
	t := &http.Transport{DisableKeepAlives: o.fresh, MaxIdleConns: o.workers, MaxIdleConnsPerHost: o.workers, MaxConnsPerHost: o.workers, IdleConnTimeout: 5 * time.Second}
	d := &net.Dialer{Timeout: o.timeout}
	t.DialContext = d.DialContext
	if o.protocol == "direct" {
		return t, nil
	}
	if _, _, err := net.SplitHostPort(o.proxy); err != nil {
		return nil, err
	}
	switch o.protocol {
	case "http":
		u := &url.URL{Scheme: "http", Host: o.proxy}
		t.Proxy = http.ProxyURL(u)
	case "connect", "socks5":
		t.DialContext = func(ctx context.Context, _, address string) (net.Conn, error) {
			c, err := d.DialContext(ctx, "tcp", o.proxy)
			if err != nil {
				return nil, err
			}
			deadline := time.Now().Add(o.timeout)
			if end, ok := ctx.Deadline(); ok && end.Before(deadline) {
				deadline = end
			}
			if err = c.SetDeadline(deadline); err == nil {
				if o.protocol == "connect" {
					err = connect(c, address)
				} else {
					err = socks(c, address)
				}
			}
			if err != nil {
				_ = c.Close()
				return nil, err
			}
			if err = c.SetDeadline(time.Time{}); err != nil {
				_ = c.Close()
				return nil, err
			}
			return c, nil
		}
	default:
		return nil, errors.New("unknown protocol")
	}
	return t, nil
}

func connect(c net.Conn, address string) error {
	r := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: address}, Host: address, Header: make(http.Header)}
	if err := r.Write(c); err != nil {
		return err
	}
	response, err := http.ReadResponse(bufio.NewReader(c), r)
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
		return errors.New("CONNECT rejected")
	}
	return nil
}

func socks(c net.Conn, address string) error {
	host, portText, err := net.SplitHostPort(address)
	if err != nil || len(host) == 0 || len(host) > 255 {
		return errors.New("invalid SOCKS destination")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("invalid SOCKS port")
	}
	if _, err = c.Write([]byte{5, 1, 0}); err != nil {
		return err
	}
	var greeting [2]byte
	if _, err = io.ReadFull(c, greeting[:]); err != nil {
		return err
	}
	if greeting != [2]byte{5, 0} {
		return errors.New("SOCKS authentication rejected")
	}
	request := append([]byte{5, 1, 0, 3, byte(len(host))}, host...)
	request = binary.BigEndian.AppendUint16(request, uint16(port))
	if _, err = c.Write(request); err != nil {
		return err
	}
	var reply [4]byte
	if _, err = io.ReadFull(c, reply[:]); err != nil {
		return err
	}
	if reply[0] != 5 || reply[1] != 0 || reply[2] != 0 {
		return errors.New("SOCKS connection rejected")
	}
	length := 0
	switch reply[3] {
	case 1:
		length = 4
	case 4:
		length = 16
	case 3:
		var n [1]byte
		if _, err = io.ReadFull(c, n[:]); err != nil {
			return err
		}
		length = int(n[0])
	default:
		return errors.New("invalid SOCKS reply")
	}
	_, err = io.CopyN(io.Discard, c, int64(length+2))
	return err
}

func run(parent context.Context, o options) (result, error) {
	if o.timeout == 0 {
		o.timeout = 15 * time.Second
	}
	u, err := url.Parse(o.target)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || o.workers < 1 || o.workers > 1024 || o.duration < time.Second || o.duration > 5*time.Minute || o.timeout < 100*time.Millisecond || o.timeout > time.Minute {
		return result{}, errors.New("invalid options")
	}
	t, err := transport(o)
	if err != nil {
		return result{}, err
	}
	defer t.CloseIdleConnections()
	client := &http.Client{Transport: t, Timeout: o.timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r := result{Protocol: o.protocol, Workers: o.workers, Fresh: o.fresh, TimeoutSeconds: o.timeout.Seconds(), LatencyCapMS: 60000}
	// Bounded 1 ms histogram: no payloads, URLs, or per-request samples retained.
	var histogram [60001]int64
	var mu sync.Mutex
	var wg sync.WaitGroup
	started := time.Now()
	ctx, cancel := context.WithTimeout(parent, o.duration)
	defer cancel()
	for range o.workers {
		wg.Go(func() {
			for ctx.Err() == nil {
				request, err := http.NewRequestWithContext(ctx, http.MethodGet, o.target, nil)
				if err != nil {
					return
				}
				begin := time.Now()
				response, requestErr := client.Do(request)
				var n int64
				if requestErr == nil {
					n, requestErr = io.Copy(io.Discard, response.Body)
					_ = response.Body.Close()
					if response.StatusCode != http.StatusOK || n != response.ContentLength {
						requestErr = errors.New("unexpected response")
					}
				}
				ms := min(int(time.Since(begin).Milliseconds())+1, len(histogram)-1)
				mu.Lock()
				r.Bytes += n
				if requestErr != nil {
					if ctx.Err() != nil {
						r.Cancelled++
					} else {
						r.Failures++
					}
				} else {
					r.Completed++
					histogram[ms]++
					if ms == len(histogram)-1 {
						r.LatencyCapped = true
					}
				}
				mu.Unlock()
				if requestErr != nil {
					select {
					case <-ctx.Done():
					case <-time.After(20 * time.Millisecond):
					}
				}
			}
		})
	}
	wg.Wait()
	r.Seconds = time.Since(started).Seconds()
	r.Mbps = float64(r.Bytes) * 8 / r.Seconds / 1e6
	r.RequestsSecond = float64(r.Completed) / r.Seconds
	r.P50MS = percentile(histogram[:], r.Completed, 50)
	r.P95MS = percentile(histogram[:], r.Completed, 95)
	return r, nil
}

func percentile(histogram []int64, count int64, percent int64) int {
	if count == 0 {
		return 0
	}
	threshold := (count*percent + 99) / 100
	var sum int64
	for ms, n := range histogram {
		sum += n
		if sum >= threshold {
			return ms
		}
	}
	return len(histogram) - 1
}
