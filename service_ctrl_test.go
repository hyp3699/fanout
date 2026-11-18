package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUptimeSince(t *testing.T) {
	ts := time.Now().Add(-90 * time.Minute).Format("Mon 2006-01-02 15:04:05 MST")
	if got := uptimeSince(ts); !strings.Contains(got, "小时") {
		t.Errorf("解析失败: %q → %q", ts, got)
	}
	if got := uptimeSince("garbage"); got != "" {
		t.Errorf("解析不了应该返回空: %q", got)
	}
}

func TestTailLines(t *testing.T) {
	text := "1\n2\n3\n4\n5\n"
	if got := tailLines(text, 2); got != "4\n5" {
		t.Errorf("tailLines 不对: %q", got)
	}
}

func TestFirewallSnapshotAndSync(t *testing.T) {
	lp, fake := setupPanel(t)
	writeTestNode(t, lp, "vless-ws-1.json", vlessWSSample)
	writeTestNode(t, lp, "hysteria2-1.json", `{"inbounds":[{"type":"hysteria2","tag":"hysteria2-1","listen":"::","listen_port":34567,
      "users":[{"name":"u","password":"p"}],"tls":{"enabled":true,"certificate_path":"/tmp/c","key_path":"/tmp/k"}}]}`)
	// nft 输出：只有 vless 的 TCP 端口放行了
	port := "23457"
	fake.out["nft list chain inet filter script_input"] =
		"\t\ttcp dport " + port + " accept comment \"ScriptManaged\"\n"
	fake.out["nft list chain inet filter input"] = "\t\tpolicy drop; \n"

	info := firewallSnapshot()
	if !info.Available || info.Policy != "drop" {
		t.Fatalf("防火墙信息不对: %+v", info)
	}
	if len(info.Inbounds) != 2 {
		t.Fatalf("应该列出两个入站: %+v", info.Inbounds)
	}
	for _, p := range info.Inbounds {
		switch p.Kind {
		case "vless-ws":
			if !p.TCP || p.UDP {
				t.Errorf("vless 的 TCP 应该已放行: %+v", p)
			}
		case "hysteria2":
			if p.TCP || p.UDP {
				t.Errorf("hy2 端口还没放行: %+v", p)
			}
			if len(p.Need) != 1 || p.Need[0] != "udp" {
				t.Errorf("hy2 需要 UDP: %+v", p)
			}
		}
	}

	n, err := syncFirewall()
	if err != nil {
		t.Fatalf("补齐放行失败: %v", err)
	}
	if n < 2 {
		t.Errorf("应该补 2 条以上规则，实际 %d", n)
	}
	if !fake.has("nft add rule inet filter script_input udp dport 34567") {
		t.Error("应该放行 hy2 的 UDP 端口")
	}
}

func TestFirewallPolicyParsing(t *testing.T) {
	_, fake := setupPanel(t)
	fake.out["nft list chain inet filter input"] = "chain input {\n\ttype filter hook input priority filter; policy drop; \n}\n"
	if got := firewallPolicy(); got != "drop" {
		t.Errorf("策略解析不对: %q", got)
	}
	fake.out["nft list chain inet filter input"] = ""
	if got := firewallPolicy(); got != "" {
		t.Errorf("没有链时应该返回空: %q", got)
	}
}

func TestForeignOutboundsPutsWarpFirst(t *testing.T) {
	lp, _ := setupPanel(t)
	_ = writeFileAtomic(filepath.Join(lp.ConfDir, "outbounds.json"), []byte(`{
      "outbounds": [
        {"type": "direct", "tag": "direct"},
        {"type": "socks", "tag": "company-proxy", "server": "1.1.1.1", "server_port": 1080},
        {"type": "direct", "tag": "wireguard-out"}
      ]
    }`), 0644)
	list := foreignOutbounds()
	if len(list) != 3 {
		t.Fatalf("应该列出 3 个出站: %+v", list)
	}
	if list[0].Tag != "wireguard-out" || list[0].Note == "" {
		t.Errorf("WARP 应该排第一并带说明: %+v", list)
	}
}

func TestServiceStatesAndLogs(t *testing.T) {
	lp, fake := setupPanel(t)
	_ = os.MkdirAll(lp.FanoutDir, 0755)
	_ = os.WriteFile(filepath.Join(lp.FanoutDir, "sing-box.log"), []byte("line1\nline2\n"), 0600)
	states := serviceStates()
	if len(states) != 3 {
		t.Fatalf("应该有三个服务状态: %+v", states)
	}
	// 没有 systemd 时日志应该退回到 fanout 自己写的文件
	log := serviceLogs("sing-box", 10)
	if !strings.Contains(log, "line2") {
		t.Errorf("没读到 fanout 托管的日志: %q", log)
	}
	_ = fake
}
