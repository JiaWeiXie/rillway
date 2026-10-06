package platform

import (
	"encoding/json"
	"os/exec"
	"rillway/internal/config"
	"strings"
	"testing"
)

func TestPACAlwaysBypassesProxyHost(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node unavailable: PAC JavaScript execution test requires a JS evaluator")
	}
	for _, proxyAddress := range []string{"192.0.2.20:17890", "[2001:db8::20]:17890", "proxy.example:17890"} {
		t.Run(proxyAddress, func(t *testing.T) {
			c := config.PAC{ProxyAddress: proxyAddress} // no user bypass rules
			script, err := PAC(c)
			if err != nil {
				t.Fatal(err)
			}
			tests := [][]string{{"unrelated.example", "PROXY " + proxyAddress}, {"proxy.example.attacker.invalid", "PROXY " + proxyAddress}}
			switch proxyAddress {
			case "192.0.2.20:17890":
				tests = append(tests, []string{"192.0.2.20", "DIRECT"}, []string{"[::ffff:192.0.2.20]", "DIRECT"}, []string{"self.example", "DIRECT"}, []string{"192.0.2.21", "PROXY " + proxyAddress})
			case "[2001:db8::20]:17890":
				tests = append(tests, []string{"[2001:db8::20]", "DIRECT"}, []string{"self-v6.example", "DIRECT"}, []string{"[2001:db8::21]", "PROXY " + proxyAddress})
			case "proxy.example:17890":
				tests = append(tests, []string{"PROXY.EXAMPLE.", "DIRECT"}, []string{"other.proxy.example", "PROXY " + proxyAddress})
			}
			cases, _ := json.Marshal(tests)
			script += `
function isPlainHostName(h){return h.indexOf('.')<0;}
function dnsResolve(h){return h==='self.example'?'192.0.2.20':h==='self-v6.example'?'2001:db8::20':null;}
const tests=` + string(cases) + `;
for(const [host,want] of tests){const got=FindProxyForURL('https://'+host+'/',host);if(got!==want)throw new Error(host+': '+got+' != '+want);}
`
			cmd := exec.CommandContext(t.Context(), node)
			cmd.Stdin = strings.NewReader(script)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("PAC execution: %v\n%s", err, out)
			}
		})
	}
}

// Node is only a test evaluator. Rillway's runtime and build have no JS runtime
// dependency. GitHub's Linux/macOS runners provide Node; minimal Go hosts skip.
func TestGeneratedPACJavaScript(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node unavailable: PAC JavaScript execution test requires a JS evaluator")
	}
	c := config.Default(t.TempDir())
	c.PAC.BypassDomains = append(c.PAC.BypassDomains, config.PACBypass{Value: "corp.example", Enabled: true})
	c.PAC.BypassDomains[3].Enabled = false // disabled ts.net must not reach the PAC script
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
	 ['printer','DIRECT'],['machine.tail.ts.net','PROXY 127.0.0.1:17890'],
 ['100.64.0.7','DIRECT'],['192.168.50.1','DIRECT'],['8.8.8.8','PROXY 127.0.0.1:17890'],
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
