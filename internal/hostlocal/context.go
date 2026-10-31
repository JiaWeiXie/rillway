package hostlocal

import "context"

type eventKey struct{}

func WithEvent(ctx context.Context, event *Event) context.Context {
	return context.WithValue(ctx, eventKey{}, event)
}

func EventFrom(ctx context.Context) *Event {
	event, _ := ctx.Value(eventKey{}).(*Event)
	return event
}

// Denial retains a fixed policy reason without exposing a source or destination
// in its client-facing error. Numeric candidate errors may wrap it in net.OpError.
type Denial struct{ Reason string }

func (d *Denial) Error() string {
	if d.Reason == "proxy_listener" || d.Reason == "management_endpoint" {
		return "access to Rillway listeners through the proxy is prohibited"
	}
	if d.Reason == "unverified_localhost_exception" {
		return "host-local access requires a verified destination IP; this provider resolves localhost remotely"
	}
	return ErrDenied.Error()
}

func (d *Denial) Unwrap() error { return ErrDenied }
