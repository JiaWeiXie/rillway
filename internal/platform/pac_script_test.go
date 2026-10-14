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

func TestPACDNSResolveMemoizationAndSkipWhenNoCIDR(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node unavailable: PAC JavaScript execution test requires a JS evaluator")
	}

	// Valid DNS answers are reused briefly; failure is never memoized.
	c := config.Default(t.TempDir())
	script, err := PAC(c.PAC)
	if err != nil {
		t.Fatal(err)
	}
	script += `
var now=1000,calls={};
Date.now=function(){return now;};
function isPlainHostName(h){return h.indexOf('.')<0;}
function dnsResolve(h){
 calls[h]=(calls[h]||0)+1;
 if(h==='private.example')return calls[h]===1?'192.168.1.2':'8.8.8.8';
 if(h==='public.example')return calls[h]===1?'8.8.8.8':'192.168.1.2';
 if(h==='failure.example')return calls[h]===1?null:'192.168.1.2';
 if(h==='empty.example')return calls[h]===1?'':'192.168.1.2';
 if(h==='invalid.example')return calls[h]===1?'not-an-ip;also-invalid':'192.168.1.2';
 if(h==='throw.example'&&calls[h]===1)throw new Error('temporary DNS failure');
 if(h==='throw.example')return '192.168.1.2';
 return '8.8.8.8';
}
function expect(host,want){
 var got=FindProxyForURL('https://'+host+'/',host);
 if(got!==want)throw new Error(host+': '+got+' != '+want);
}
expect('private.example','DIRECT');expect('public.example','PROXY 127.0.0.1:17890');
expect('failure.example','PROXY 127.0.0.1:17890');
expect('empty.example','PROXY 127.0.0.1:17890');
expect('invalid.example','PROXY 127.0.0.1:17890');
expect('throw.example','PROXY 127.0.0.1:17890');
expect('throw.example','DIRECT');
expect('failure.example','DIRECT');expect('empty.example','DIRECT');expect('invalid.example','DIRECT');
now+=29999;
expect('private.example','DIRECT');expect('public.example','PROXY 127.0.0.1:17890');
if(calls['private.example']!==1||calls['public.example']!==1)throw new Error('cached valid answers must be reused before expiry');
now++;
expect('private.example','PROXY 127.0.0.1:17890');expect('public.example','DIRECT');
if(calls['private.example']!==2||calls['public.example']!==2)throw new Error('both decisions must refresh at 30 seconds');
for(var i=0;i<507;i++)expect('h'+i+'.example','PROXY 127.0.0.1:17890');
expect('h507.example','PROXY 127.0.0.1:17890');
expect('h506.example','PROXY 127.0.0.1:17890');
if(calls['h506.example']!==1)throw new Error('inserting 513th hostname flushed non-evicted cache entries');
expect('private.example','PROXY 127.0.0.1:17890');
if(calls['private.example']!==3)throw new Error('oldest cache entry not evicted');
`
	cmd := exec.CommandContext(t.Context(), node)
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("PAC memoization execution: %v\n%s", err, out)
	}

	// 2. When ProxyAddress is a hostname and no bypass CIDRs are enabled, dnsResolve is never called.
	noCIDR, err := PAC(config.PAC{ProxyAddress: "proxy.example:17890"})
	if err != nil {
		t.Fatal(err)
	}
	noCIDR += `
function isPlainHostName(h){return h.indexOf('.')<0;}
function dnsResolveEx(h){throw new Error('dnsResolveEx should not be called when no CIDRs exist: '+h);}
function dnsResolve(h){throw new Error('dnsResolve should not be called when no CIDRs exist: '+h);}
if(FindProxyForURL('https://chatgpt.com/','chatgpt.com')!=='PROXY proxy.example:17890')throw new Error('unexpected result');
`
	cmd2 := exec.CommandContext(t.Context(), node)
	cmd2.Stdin = strings.NewReader(noCIDR)
	if out, err := cmd2.CombinedOutput(); err != nil {
		t.Fatalf("PAC no-CIDR execution: %v\n%s", err, out)
	}
}

func TestPACDNSResolveExMultipleAnswers(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node unavailable: PAC JavaScript execution test requires a JS evaluator")
	}
	c := config.Default(t.TempDir())
	script, err := PAC(c.PAC)
	if err != nil {
		t.Fatal(err)
	}
	script += `
function isPlainHostName(h){return h.indexOf('.')<0;}
function dnsResolve(h){throw new Error('dnsResolve should not be used when dnsResolveEx exists: '+h);}
var calls=0;
function dnsResolveEx(h){
 calls++;
 if(h==='multi.example')return '8.8.8.8; ::ffff:192.168.1.2';
 return calls===2?'invalid;also-invalid':'8.8.8.8';
}
if(FindProxyForURL('https://multi.example/','multi.example')!=='DIRECT')throw new Error('later private answer not bypassed');
if(FindProxyForURL('https://multi.example/','multi.example')!=='DIRECT'||calls!==1)throw new Error('multi-answer decision not cached');
if(FindProxyForURL('https://invalid.example/','invalid.example')!=='PROXY 127.0.0.1:17890')throw new Error('invalid DNS result');
if(FindProxyForURL('https://invalid.example/','invalid.example')!=='PROXY 127.0.0.1:17890'||calls!==3)throw new Error('invalid DNS result was cached');
`
	cmd := exec.CommandContext(t.Context(), node)
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("PAC dnsResolveEx execution: %v\n%s", err, out)
	}
}
