package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"reflect"
	"rillway/internal/config"
	"rillway/internal/engine"
	"rillway/internal/outbound"
	"sync"
	"time"
)

type Runtime struct {
	mu        sync.Mutex
	cfg       config.Config
	path      string
	providers map[string]outbound.Provider
	Engine    *engine.Engine
	appliedAt time.Time
}

func New(ctx context.Context, path string, c config.Config) (*Runtime, error) {
	if err := config.Validate(c); err != nil {
		return nil, err
	}
	r := &Runtime{cfg: clone(c), path: path, providers: map[string]outbound.Provider{}, appliedAt: time.Now().UTC()}
	for _, o := range c.Outbounds {
		r.providers[o.ID] = create(ctx, o)
	}
	r.Engine = engine.New(c, r.providers)
	return r, nil
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
func (r *Runtime) Snapshot() any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return struct {
		engine.Snapshot
		Revision  uint64    `json:"config_revision"`
		AppliedAt time.Time `json:"applied_at"`
	}{r.Engine.Snapshot(), r.cfg.Revision, r.appliedAt}
}

func (r *Runtime) Apply(ctx context.Context, c config.Config) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c.Revision != r.cfg.Revision {
		return config.ErrConflict
	}
	if err := config.Validate(c); err != nil {
		return config.PublicError{Message: "設定驗證失敗：" + err.Error(), Err: err}
	}
	if !reflect.DeepEqual(c.Listeners, r.cfg.Listeners) || !reflect.DeepEqual(c.Security, r.cfg.Security) {
		return config.PublicError{Message: "監聽位址與安全設定需要在主機修改設定檔後重啟 daemon"}
	}
	old := map[string]config.Outbound{}
	for _, o := range r.cfg.Outbounds {
		old[o.ID] = o
	}
	next := map[string]outbound.Provider{}
	var created []outbound.Provider
	for _, o := range c.Outbounds {
		if reflect.DeepEqual(o, old[o.ID]) {
			next[o.ID] = r.providers[o.ID]
			continue
		}
		previous := old[o.ID]
		if o.Type == "tailscale" && o.Enabled && previous.Enabled && previous.Type == "tailscale" && o.StateDir == previous.StateDir {
			for _, v := range created {
				_ = v.Close()
			}
			return config.PublicError{Message: fmt.Sprintf("出口 %s 使用中的 Tailscale state directory 需要重啟 daemon 才能變更", o.ID)}
		}
		p := create(ctx, o)
		if u, ok := p.(*unavailable); ok && o.Enabled {
			for _, v := range created {
				_ = v.Close()
			}
			return fmt.Errorf("outbound %s: %s", o.ID, u.reason)
		}
		next[o.ID] = p
		created = append(created, p)
	}
	c.Revision++
	if err := config.Save(r.path, c); err != nil {
		for _, p := range created {
			_ = p.Close()
		}
		return err
	}
	r.Engine.Update(c, next)
	for id, p := range r.providers {
		if p != next[id] {
			_ = p.Close()
		}
	}
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
	var errs []error
	for _, p := range r.providers {
		errs = append(errs, p.Close())
	}
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
