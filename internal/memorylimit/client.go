package memorylimit

import (
	"context"
	"encoding/json"
	"net"
	"runtime"
	"runtime/debug"
	"time"
)

type wireRequest struct {
	Action string   `json:"action"`
	Limit  *Request `json:"limit,omitempty"`
}

type wireResponse struct {
	Status   Status `json:"status"`
	Error    string `json:"error,omitempty"`
	Conflict bool   `json:"conflict,omitempty"`
}

var ErrConflict = messageError("Memory settings changed elsewhere. Refresh limits before saving.")

// Control contacts the socket-activated, narrowly scoped local controller. The
// HTTPS API/TUI never needs root, sudo or permission to change arbitrary units.
func Control(ctx context.Context, request *Request) (Status, error) {
	if runtime.GOOS != "linux" {
		return Status{Reason: "Service memory limits require a Linux systemd installation."}, nil
	}
	conn, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", SocketPath)
	if err != nil {
		return Status{Reason: "Memory control is unavailable. Upgrade the installed service to enable it."}, nil
	}
	defer func() { _ = conn.Close() }()
	deadline := time.Now().Add(10 * time.Second)
	if value, ok := ctx.Deadline(); ok && value.Before(deadline) {
		deadline = value
	}
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	action := "status"
	if request != nil {
		action = "apply"
	}
	if err = json.NewEncoder(conn).Encode(wireRequest{Action: action, Limit: request}); err != nil {
		return Status{}, messageError("Memory control did not respond. Read current limits before retrying.")
	}
	var reply wireResponse
	if err = json.NewDecoder(conn).Decode(&reply); err != nil {
		return Status{}, messageError("Memory control did not respond. Read current limits before retrying.")
	}
	if reply.Conflict {
		return reply.Status, ErrConflict
	}
	if reply.Error != "" {
		return reply.Status, messageError(reply.Error)
	}
	if reply.Status.Supported && reply.Status.GoLimitBytes > 0 {
		// This is Go-managed memory only. The OS cgroup remains the hard cap.
		debug.SetMemoryLimit(int64(reply.Status.GoLimitBytes))
	}
	return reply.Status, nil
}
