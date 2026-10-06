package memorylimit

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// Units returns a private Unix socket and root controller using the same binary.
// The helper cannot access the network or modify arbitrary service properties.
func Units(binary, configPath string) (socket, service string, err error) {
	for _, p := range []string{binary, configPath} {
		if !filepath.IsAbs(p) || strings.ContainsAny(p, "\n\r\x00") {
			return "", "", errors.New("service paths must be absolute")
		}
	}
	quote := func(p string) string { return strconv.Quote(strings.ReplaceAll(p, "%", "%%")) }
	socket = `[Unit]
Description=Rillway memory control socket

[Socket]
ListenStream=/run/rillway-memory.sock
SocketUser=root
SocketGroup=rillway
SocketMode=0660
Backlog=8
RemoveOnStop=yes

[Install]
WantedBy=sockets.target
`
	service = fmt.Sprintf(`[Unit]
Description=Rillway memory control
Requires=rillway-memory.socket
After=rillway-memory.socket

[Service]
Type=simple
User=root
Group=root
ExecStart=%s service memory-helper --config %s
StateDirectory=rillway-resource-control
StateDirectoryMode=0755
UMask=0077
NoNewPrivileges=yes
CapabilityBoundingSet=CAP_DAC_READ_SEARCH
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
RestrictAddressFamilies=AF_UNIX
MemoryMax=64M
CPUQuota=20%%
TasksMax=32
Environment=GOMEMLIMIT=32MiB
Restart=no
`, quote(binary), quote(configPath))
	return socket, service, nil
}
