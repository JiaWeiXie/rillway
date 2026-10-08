package app

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"rillway/internal/config"
	"rillway/internal/engine"
	"rillway/internal/memorylimit"
	"rillway/internal/outbound"
	"rillway/internal/proxy"
	"sync"
	"time"
)

type Runtime struct {
	mu              sync.Mutex
	cfg             config.Config
	path            string
	providers       map[string]outbound.Provider
	tailscaleOwners map[string]*managed
	Engine          *engine.Engine
	appliedAt       time.Time
	restart         chan struct{}
	admission       func() proxy.AdmissionRejections
}

// PrepareRestart validates the saved configuration before the HTTP handler
// acknowledges the request. The callback runs only after that response flushes.
func (r *Runtime) PrepareRestart(ctx context.Context) (func(), error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.restart == nil || ctx.Err() != nil {
		return nil, config.PublicError{Message: "Service restart is not available in this session."}
	}
	next, err := config.Load(r.path)
	if err != nil {
		return nil, config.PublicError{Message: "The saved Server configuration is invalid or unreadable. Fix it before restarting."}
	}
	if _, err := tls.LoadX509KeyPair(next.Security.TLSCertFile, next.Security.TLSKeyFile); err != nil {
		return nil, config.PublicError{Message: "The saved Server credentials are unreadable or invalid. Fix them before restarting."}
	}
	for _, filename := range []string{next.Security.AdminTokenFile, next.Security.ProxyPasswordFile} {
		if filename == "" {
			continue
		}
		data, err := os.ReadFile(filename)
		if err != nil || len(bytes.TrimSpace(data)) == 0 {
			return nil, config.PublicError{Message: "The saved Server credentials are unreadable or invalid. Fix them before restarting."}
		}
	}
	currentAddresses := map[string]bool{}
	heldAddresses := make([]string, 0, 4)
	for _, address := range []string{r.cfg.Listeners.HTTP, r.cfg.Listeners.SOCKS5, r.cfg.Listeners.Admin, r.cfg.Listeners.PAC} {
		if address != "" {
			currentAddresses[address] = true
			heldAddresses = append(heldAddresses, address)
		}
	}
	for _, address := range []string{next.Listeners.HTTP, next.Listeners.SOCKS5, next.Listeners.Admin, next.Listeners.PAC} {
		if address == "" || currentAddresses[address] {
			continue
		}
		host, _, _ := net.SplitHostPort(address)
		overlapsHeld := false
		for _, held := range heldAddresses {
			if config.ListenAddressesOverlap(held, address) {
				overlapsHeld = true
				break
			}
		}
		if overlapsHeld {
			if !localBindHost(host) {
				return nil, config.PublicError{Message: "A new listener address is unavailable. Fix the Server configuration before restarting."}
			}
			continue
		}
		listener, err := net.Listen("tcp", address)
		if err != nil {
			return nil, config.PublicError{Message: "A new listener address is unavailable. Fix the Server configuration before restarting."}
		}
		_ = listener.Close()
	}
	signal := r.restart
	return func() {
		select {
		case signal <- struct{}{}:
		default:
		}
	}, nil
}

func localBindHost(host string) bool {
	if host == "" {
		return true
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	ip = ip.Unmap()
	if ip.IsUnspecified() {
		return true
	}
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, address := range addresses {
		if prefix, err := netip.ParsePrefix(address.String()); err == nil && prefix.Addr().Unmap() == ip {
			return true
		}
	}
	return false
}

func New(ctx context.Context, path string, c config.Config) (*Runtime, error) {
	if err := config.Validate(c); err != nil {
		return nil, err
	}
	r := &Runtime{cfg: clone(c), path: path, providers: map[string]outbound.Provider{}, tailscaleOwners: map[string]*managed{}, appliedAt: time.Now().UTC()}
	// Restore a saved Go GC budget, if the installed resource controller exists.
	// This read never changes OS limits and must not prevent foreground startup.
	_, _ = memorylimit.Control(ctx, nil)
	for _, o := range c.Outbounds {
		if err := r.ensureTailscaleStateAvailable(o); err != nil {
			_ = r.closeProviders()
			return nil, err
		}
		p := create(ctx, o)
		r.providers[o.ID] = p
		r.trackTailscaleOwner(o, p)
	}
	r.Engine = engine.New(c, r.providers)
	return r, nil
}

func tailscaleStateKey(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = filepath.Clean(path)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	if parent, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
		return filepath.Join(parent, filepath.Base(abs))
	}
	return abs
}

func (r *Runtime) ensureTailscaleStateAvailable(o config.Outbound) error {
	if o.Type != "tailscale" || !o.Enabled {
		return nil
	}
	r.pruneTailscaleOwners()
	key := tailscaleStateKey(o.StateDir)
	if owner := r.tailscaleOwners[key]; owner != nil && !owner.isClosed() {
		return config.PublicError{Message: fmt.Sprintf("Restart the daemon to use outbound %s because its Tailscale state directory is still in use.", o.ID)}
	}
	return nil
}

func (r *Runtime) pruneTailscaleOwners() {
	for key, owner := range r.tailscaleOwners {
		if owner.isClosed() {
			delete(r.tailscaleOwners, key)
		}
	}
}

func (r *Runtime) trackTailscaleOwner(o config.Outbound, p outbound.Provider) {
	if o.Type != "tailscale" || !o.Enabled {
		return
	}
	if owner, ok := p.(*managed); ok {
		r.tailscaleOwners[tailscaleStateKey(o.StateDir)] = owner
	}
}

func create(ctx context.Context, c config.Outbound) outbound.Provider {
	if !c.Enabled {
		return &unavailable{cfg: c, reason: "disabled"}
	}
	p, err := outbound.New(ctx, c)
	if err != nil {
		return &unavailable{cfg: c, reason: err.Error()}
	}
	return &managed{Provider: p}
}

func clone(c config.Config) config.Config {
	b, _ := json.Marshal(c)
	var d config.Config
	_ = json.Unmarshal(b, &d)
	return d
}
func (r *Runtime) Config() config.Config { r.mu.Lock(); defer r.mu.Unlock(); return clone(r.cfg) }

// RestartRequiredOutbounds includes retired nodes with open connections. Their
// state directories cannot be safely opened by a replacement provider yet.
func (r *Runtime) RestartRequiredOutbounds() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var ids []string
	for _, o := range r.cfg.Outbounds {
		if o.Type != "tailscale" {
			continue
		}
		if owner := r.tailscaleOwners[tailscaleStateKey(o.StateDir)]; owner != nil && !owner.isClosed() {
			ids = append(ids, o.ID)
		}
	}
	return ids
}

func (r *Runtime) Snapshot() any {
	r.mu.Lock()
	defer r.mu.Unlock()
	var rejections proxy.AdmissionRejections
	if r.admission != nil {
		rejections = r.admission()
	}
	return struct {
		engine.Snapshot
		Revision   uint64                    `json:"config_revision"`
		AppliedAt  time.Time                 `json:"applied_at"`
		Rejections proxy.AdmissionRejections `json:"proxy_admission_rejections"`
	}{r.Engine.Snapshot(), r.cfg.Revision, r.appliedAt, rejections}
}

func (r *Runtime) Apply(ctx context.Context, c config.Config) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c.Revision != r.cfg.Revision {
		return config.ErrConflict
	}
	if err := config.Validate(c); err != nil {
		return config.PublicError{Message: "Configuration validation failed: " + err.Error(), Err: err}
	}
	if !reflect.DeepEqual(c.Listeners, r.cfg.Listeners) || !reflect.DeepEqual(c.Security, r.cfg.Security) {
		return config.PublicError{Message: "To change listener addresses or security settings, edit the configuration file on the host and restart the daemon."}
	}
	old := map[string]config.Outbound{}
	for _, o := range r.cfg.Outbounds {
		old[o.ID] = o
	}
	for _, o := range c.Outbounds {
		previous := old[o.ID]
		if o.Enabled && (o.Type == "tailscale" || o.Type == "wireguard") && (!previous.Enabled || previous.Type != o.Type) {
			s, err := memorylimit.Control(ctx, nil)
			if err != nil {
				return memoryPublicError(err)
			}
			minimum, _ := memorylimit.SafeMinimum(c, s.CurrentBytes)
			if s.Supported && s.LimitBytes != 0 && s.LimitBytes < minimum {
				return config.PublicError{Message: "Increase the service memory limit before enabling an embedded VPN."}
			}
			break
		}
	}
	next := map[string]outbound.Provider{}
	var created []outbound.Provider
	for _, o := range c.Outbounds {
		if reflect.DeepEqual(o, old[o.ID]) {
			next[o.ID] = r.providers[o.ID]
			continue
		}
		if err := r.ensureTailscaleStateAvailable(o); err != nil {
			for _, v := range created {
				_ = v.Close()
			}
			r.pruneTailscaleOwners()
			return err
		}
		p := create(ctx, o)
		if u, ok := p.(*unavailable); ok && o.Enabled {
			for _, v := range created {
				_ = v.Close()
			}
			r.pruneTailscaleOwners()
			return fmt.Errorf("outbound %s: %s", o.ID, u.reason)
		}
		next[o.ID] = p
		created = append(created, p)
		r.trackTailscaleOwner(o, p)
	}
	c.Revision++
	if err := config.Save(r.path, c); err != nil {
		for _, p := range created {
			_ = p.Close()
		}
		r.pruneTailscaleOwners()
		return err
	}
	r.Engine.Update(c, next)
	for id, p := range r.providers {
		if p != next[id] {
			_ = p.Close()
		}
	}
	r.pruneTailscaleOwners()
	r.cfg = clone(c)
	r.providers = next
	r.appliedAt = time.Now().UTC()
	return nil
}

func (r *Runtime) Statuses(ctx context.Context) []outbound.Status {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	r.mu.Lock()
	ps := make([]outbound.Provider, 0, len(r.providers))
	for _, o := range r.cfg.Outbounds {
		ps = append(ps, r.providers[o.ID])
	}
	r.mu.Unlock()
	out := make([]outbound.Status, len(ps))
	var wg sync.WaitGroup
	for i, p := range ps {
		wg.Add(1)
		go func() { defer wg.Done(); out[i] = p.Status(ctx) }()
	}
	wg.Wait()
	return out
}

func (r *Runtime) Action(ctx context.Context, id, action, value string) error {
	r.mu.Lock()
	p := r.providers[id]
	r.mu.Unlock()
	if p == nil {
		return errors.New("unknown outbound")
	}
	ctl, ok := p.(outbound.Controller)
	if !ok {
		return errors.New("enable this outbound before controlling it")
	}
	return ctl.Action(ctx, action, value)
}

func (r *Runtime) Close() error {
	_ = r.Engine.Close()
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closeProviders()
}

func (r *Runtime) closeProviders() error {
	var errs []error
	closed := map[*managed]bool{}
	for _, p := range r.providers {
		errs = append(errs, p.Close())
		if owner, ok := p.(*managed); ok {
			closed[owner] = true
		}
	}
	for _, owner := range r.tailscaleOwners {
		if !closed[owner] {
			errs = append(errs, owner.Close())
		}
	}
	r.pruneTailscaleOwners()
	return errors.Join(errs...)
}

type unavailable struct {
	cfg    config.Outbound
	reason string
}

func (u *unavailable) ID() string   { return u.cfg.ID }
func (u *unavailable) Close() error { return nil }
func (u *unavailable) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, fmt.Errorf("outbound %s unavailable: %s", u.cfg.ID, u.reason)
}

func (u *unavailable) Status(context.Context) outbound.Status {
	state := "unavailable"
	if !u.cfg.Enabled {
		state = "disabled"
	}
	return outbound.Status{ID: u.cfg.ID, Type: u.cfg.Type, State: state, Detail: u.reason, PublicInternet: u.cfg.PublicInternet}
}

// managed keeps a retired provider alive until its last existing stream closes.
type managed struct {
	outbound.Provider
	mu      sync.Mutex
	active  int
	retired bool
	closed  bool
}

func (m *managed) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	m.mu.Lock()
	if m.retired {
		m.mu.Unlock()
		return nil, errors.New("outbound configuration retired")
	}
	m.active++
	m.mu.Unlock()
	c, err := m.Provider.DialContext(ctx, network, address)
	if err != nil {
		m.release()
		return nil, err
	}
	return &managedConn{Conn: c, owner: m}, nil
}

func (m *managed) release() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.active--
	if m.retired && m.active == 0 && !m.closed {
		m.closed = true
		_ = m.Provider.Close()
	}
}

func (m *managed) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.retired = true
	if m.active == 0 && !m.closed {
		m.closed = true
		return m.Provider.Close()
	}
	return nil
}

func (m *managed) isClosed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
}

func (m *managed) Action(ctx context.Context, a, v string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.retired {
		return errors.New("outbound configuration retired")
	}
	if p, ok := m.Provider.(outbound.Controller); ok {
		return p.Action(ctx, a, v)
	}
	return errors.New("outbound does not support this action")
}

type managedConn struct {
	net.Conn
	owner *managed
	once  sync.Once
}

func (c *managedConn) Close() error { err := c.Conn.Close(); c.once.Do(c.owner.release); return err }

func (c *managedConn) CloseWrite() error {
	if v, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return v.CloseWrite()
	}
	return c.Close()
}

func (c *managedConn) DestinationIP() string {
	if v, ok := c.Conn.(interface{ DestinationIP() string }); ok {
		return v.DestinationIP()
	}
	return ""
}
