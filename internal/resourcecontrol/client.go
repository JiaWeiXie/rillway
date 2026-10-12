package resourcecontrol

import (
	"context"
	"encoding/json"
	"net"
	"runtime"
	"runtime/debug"
	"time"
)

type wireRequest struct {
	Action string         `json:"action"`
	Memory *MemoryRequest `json:"memory,omitempty"`
}

type wireResponse struct {
	Memory   MemoryStatus `json:"memory,omitempty"`
	Error    string       `json:"error,omitempty"`
	Conflict bool         `json:"conflict,omitempty"`
}

var ErrMemoryConflict = messageError("Memory settings changed elsewhere. Refresh limits before saving.")

// MemoryControl contacts the socket-activated, narrowly scoped local controller. The
// HTTPS API/TUI never needs root, sudo or permission to change arbitrary units.
func MemoryControl(ctx context.Context, request *MemoryRequest) (MemoryStatus, error) {
	if runtime.GOOS != "linux" {
		return MemoryStatus{Reason: "Service memory limits require a Linux systemd installation."}, nil
	}
	conn, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", SocketPath)
	if err != nil {
		return MemoryStatus{Reason: "Memory control is unavailable. Upgrade the installed service to enable it."}, nil
	}
	defer func() { _ = conn.Close() }()
	deadline := time.Now().Add(10 * time.Second)
	if value, ok := ctx.Deadline(); ok && value.Before(deadline) {
		deadline = value
	}
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	action := "memory.status"
	if request != nil {
		action = "memory.apply"
	}
	if err = json.NewEncoder(conn).Encode(wireRequest{Action: action, Memory: request}); err != nil {
		return MemoryStatus{}, messageError("Memory control did not respond. Read current limits before retrying.")
	}
	var reply wireResponse
	if err = json.NewDecoder(conn).Decode(&reply); err != nil {
		return MemoryStatus{}, messageError("Memory control did not respond. Read current limits before retrying.")
	}
	if reply.Conflict {
		return reply.Memory, ErrMemoryConflict
	}
	if reply.Error != "" {
		return reply.Memory, messageError(reply.Error)
	}
	if reply.Memory.Supported && reply.Memory.GoLimitBytes > 0 {
		// This is Go-managed memory only. The OS cgroup remains the hard cap.
		debug.SetMemoryLimit(int64(reply.Memory.GoLimitBytes))
	}
	return reply.Memory, nil
}
