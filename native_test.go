package main

import (
	"strings"
	"testing"
)

func toStrings(v any) []string {
	var out []string
	for _, e := range v.([]any) {
		out = append(out, e.(string))
	}
	return out
}

func TestSocksOutboundCarriesCredential(t *testing.T) {
	tun := &Tunnel{Port: 2000, Status: "up", Node: Node{HostName: "kr.example"}}
	tun.setCredential(SocksCred{User: "fouser", Pass: "pw"})
	o := socksOutbound(tun)
	if o["tag"] != "fanout-exit-kr-example" || o["username"] != "fouser" || o["password"] != "pw" || o["version"] != "5" {
		t.Errorf("socks 出站不对: %v", o)
	}
}

func TestShareLinkPerProtocol(t *testing.T) {
	c := nativeClient{ID: "uuid-1", Password: "pw-1", Email: "e", Enable: true}

	vless := shareLink(&nativeInbound{Port: 100, Protocol: "vless", Remark: "r"}, c, "1.2.3.4")
	if !strings.HasPrefix(vless, "vless://uuid-1@1.2.3.4:100?") {
		t.Errorf("vless 链接格式不对: %s", vless)
	}
	if !strings.Contains(vless, "encryption=none") {
		t.Errorf("vless 需要 encryption=none: %s", vless)
	}

	tro := shareLink(&nativeInbound{Port: 200, Protocol: "trojan", Network: "ws", Path: "/p"}, c, "h")
	if !strings.HasPrefix(tro, "trojan://pw-1@h:200?") {
		t.Errorf("trojan 应当用密码而不是 UUID: %s", tro)
	}
	if !strings.Contains(tro, "path=%2Fp") {
		t.Errorf("ws 链接要带 path: %s", tro)
	}
}

func TestShareLinkCarriesSecurityParams(t *testing.T) {
	c := nativeClient{ID: "uuid-1", Enable: true, Flow: "xtls-rprx-vision"}

	re := shareLink(&nativeInbound{
		Port: 100, Protocol: "vless", Network: "tcp", Security: "reality", Remark: "r",
		Reality: &realityConfig{
			ServerNames: []string{"www.cloudflare.com"}, PublicKey: "PBK",
			ShortIDs: []string{"sid1"}, Fingerprint: "chrome",
		},
	}, c, "h")
	for _, want := range []string{"pbk=PBK", "sid=sid1", "fp=chrome",
		"sni=www.cloudflare.com", "flow=xtls-rprx-vision"} {
		if !strings.Contains(re, want) {
			t.Errorf("REALITY 链接缺少 %s: %s", want, re)
		}
	}

	// 自签证书验不过 CA，链接必须带指纹，否则客户端连不上
	tl := shareLink(&nativeInbound{
		Port: 200, Protocol: "vless", Network: "tcp", Security: "tls", Remark: "t",
		TLS: &tlsConfig{ServerName: "demo.local", SelfSigned: true, CertSha256: "AABB"},
	}, nativeClient{ID: "u", Enable: true}, "h")
	for _, want := range []string{"pinSHA256=AABB", "allowInsecure=1", "insecure=1", "sni=demo.local"} {
		if !strings.Contains(tl, want) {
			t.Errorf("自签 TLS 链接缺少 %s: %s", want, tl)
		}
	}
}
