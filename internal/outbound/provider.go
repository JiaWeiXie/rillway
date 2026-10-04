package outbound

import (
	"context"
	"net"
	"time"
)

// Provider returns a connected TCP stream. It must never silently fall back to
// another provider. address is a hostname or IP plus port; network is tcp/4/6.
type Provider interface {
	ID() string
	DialContext(context.Context, string, string) (net.Conn, error)
	Status(context.Context) Status
	Close() error
}

type Controller interface {
	Action(context.Context, string, string) error
}

type Status struct {
	ID             string    `json:"id"`
	Type           string    `json:"type"`
	State          string    `json:"state"`
	Detail         string    `json:"detail,omitempty"`
	Account        string    `json:"account,omitempty"`
	Version        string    `json:"version,omitempty"`
	Mode           string    `json:"mode,omitempty"`
	Listener       bool      `json:"listener"`
	AuthURL        string    `json:"auth_url,omitempty"`
	VerifiedAt     time.Time `json:"verified_at,omitempty"`
	PublicInternet bool      `json:"public_internet"`
}
