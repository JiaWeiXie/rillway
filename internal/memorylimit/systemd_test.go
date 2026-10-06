package memorylimit

import (
	"strings"
	"testing"
)

func TestMemoryUnitsRemainNarrowAndPrivate(t *testing.T) {
	socket, service, err := Units("/usr/local/lib/rillway/rillway", "/etc/rillway/config.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"SocketMode=0660", "SocketGroup=rillway", "Backlog=8", "RemoveOnStop=yes"} {
		if !strings.Contains(socket, s) {
			t.Fatal(s)
		}
	}
	for _, s := range []string{"NoNewPrivileges=yes", "RestrictAddressFamilies=AF_UNIX", "MemoryMax=64M", "ProtectSystem=strict", "service memory-helper", "StateDirectoryMode=0755"} {
		if !strings.Contains(service, s) {
			t.Fatal(s)
		}
	}
	if _, _, err = Units("relative", "/etc/config"); err == nil {
		t.Fatal("relative binary accepted")
	}
	_, service, err = Units("/path/with space/rillway", "/etc/config%name")
	if err != nil || !strings.Contains(service, `"/path/with space/rillway"`) || !strings.Contains(service, "config%%name") {
		t.Fatal(service, err)
	}
}
