package platform

import (
	"os/exec"
	"rillway/internal/config"
	"strings"
	"testing"
)

// Node is only a test evaluator. Rillway's runtime and build have no JS runtime
// dependency. GitHub's Linux/macOS runners provide Node; minimal Go hosts skip.
func TestGeneratedPACJavaScript(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node unavailable: PAC JavaScript execution test requires a JS evaluator")
	}
	c := config.Default(t.TempDir())
	c.PAC.BypassDomains = append(c.PAC.BypassDomains, "corp.example")
	script, err := PAC(c.PAC)
	if err != nil {
		t.Fatal(err)
	}
	script += `
function isPlainHostName(h){return h.indexOf('.')<0;}
function dnsResolve(h){return h==='private.example'?'192.168.1.2':h==='v6-private.example'?'fd00::1':null;}
const tests=[
 ['github.com','PROXY 127.0.0.1:17890'],
 ['api.corp.example','DIRECT'],['corp.example.attacker.net','PROXY 127.0.0.1:17890'],
 ['printer','DIRECT'],['machine.tail.ts.net','DIRECT'],
 ['100.64.0.7','DIRECT'],['192.0.2.1','DIRECT'],['8.8.8.8','PROXY 127.0.0.1:17890'],
 ['[fd00::1]','DIRECT'],['[::1]','DIRECT'],['[::ffff:192.168.1.1]','DIRECT'],
 ['[2606:4700:4700::1111]','PROXY 127.0.0.1:17890'],
 ['private.example','DIRECT'],['v6-private.example','DIRECT'],['missing.example','PROXY 127.0.0.1:17890']
];
for(const [host,want] of tests){const got=FindProxyForURL('https://'+host+'/',host);if(got!==want)throw new Error(host+': '+got+' != '+want);}
`
	cmd := exec.CommandContext(t.Context(), node)
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("PAC execution: %v\n%s", err, out)
	}
}
