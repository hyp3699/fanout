package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const vmessWSSample = `{
  "inbounds": [
    {
      "type": "vmess",
      "tag": "vmess-ws-1",
      "listen": "127.0.0.1",
      "listen_port": 8101,
      "users": [{"name": "vmess-ws-user1", "uuid": "bbbbbbbb-cccc-dddd-eeee-ffffffffffff"}],
      "transport": {"type": "ws", "path": "/qt-ws", "max_early_data": 2048, "early_data_header_name": "Sec-WebSocket-Protocol"}
    }
  ]
}`

func TestQuickUnitNameAndState(t *testing.T) {
	lp, _ := setupPanel(t)
	if got := quickUnitName("vmess-ws", 1); got != "cf-quick-vmess-ws-1" {
		t.Fatalf("单元名不对: %s", got)
	}
	// 没有单元时状态是「不存在」
	domain, active, exists := quickTunnelState("cf-quick-vmess-ws-1")
	if exists || active || domain != "" {
		t.Fatalf("空环境状态不对: %q %v %v", domain, active, exists)
	}
	unit := "cf-quick-vmess-ws-1"
	_ = os.WriteFile(filepath.Join(lp.QuickUnitDir, unit+".service"), []byte("[Unit]\n"), 0644)
	_ = os.WriteFile(filepath.Join(lp.QuickDir, unit+".log"),
		[]byte("INF |  https://aaa-bbb.trycloudflare.com  |\nINF |  https://ccc-ddd.trycloudflare.com  |\n"), 0644)
	domain, _, exists = quickTunnelState(unit)
	if !exists {
		t.Fatal("单元应该存在")
	}
	if domain != "ccc-ddd.trycloudflare.com" {
		t.Fatalf("应该取最后一个域名: %q", domain)
	}
}

func TestQuickLinkRecognitionAndStrip(t *testing.T) {
	if !isQuickLink("vless://u@1.1.1.1:443?type=ws#🇯🇵临时隧道") {
		t.Error("临时隧道链接没认出来")
	}
	if !isQuickLink("vless://u@1.1.1.1:443?type=ws#QuickTunnel-old") {
		t.Error("旧版 QuickTunnel 备注没认出来")
	}
	if isQuickLink("vless://u@1.1.1.1:443?type=ws#普通节点") {
		t.Error("普通链接被误判")
	}
	vm := vmessLink(map[string]any{"ps": "🇯🇵临时隧道", "add": "1.1.1.1"})
	if !isQuickLink(vm) {
		t.Error("vmess 临时隧道链接没认出来")
	}
}

func TestQuickRefreshLinkVlessWS(t *testing.T) {
	lp, _ := setupPanel(t)
	writeTestNode(t, lp, "vless-ws-1.json", vlessWSSample)
	_ = os.WriteFile(filepath.Join(lp.URLDir, "vless-ws-1.txt"),
		[]byte("vless://aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee@203.0.113.9:23457?security=none&type=ws&path=/abcd-ws#旧链接\n"+
			"vless://aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee@1.1.1.1:443?type=ws#旧临时隧道\n"), 0644)
	n, _, err := loadNode("vless-ws-1.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := quickRefreshLink(n, "abc-def.trycloudflare.com"); err != nil {
		t.Fatalf("刷新链接失败: %v", err)
	}
	blob, _ := os.ReadFile(filepath.Join(lp.URLDir, "vless-ws-1.txt"))
	text := string(blob)
	if !strings.Contains(text, "sni=abc-def.trycloudflare.com") || !strings.Contains(text, "host=abc-def.trycloudflare.com") {
		t.Fatalf("隧道域名没写进链接: %s", text)
	}
	if !strings.Contains(text, cdnPreferredHost+":443") {
		t.Errorf("应该用优选域名做地址: %s", text)
	}
	if strings.Contains(text, "旧临时隧道") {
		t.Errorf("旧的临时隧道链接应该被清掉: %s", text)
	}
	if !strings.Contains(text, "旧链接") {
		t.Errorf("普通链接不该被动: %s", text)
	}
}

func TestQuickRefreshLinkVmessWS(t *testing.T) {
	lp, _ := setupPanel(t)
	writeTestNode(t, lp, "vmess-ws-1.json", vmessWSSample)
	_ = os.WriteFile(filepath.Join(lp.URLDir, "vmess-ws-1.txt"), []byte(""), 0644)
	n, _, err := loadNode("vmess-ws-1.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := quickRefreshLink(n, "xyz.trycloudflare.com"); err != nil {
		t.Fatalf("刷新失败: %v", err)
	}
	blob, _ := os.ReadFile(filepath.Join(lp.URLDir, "vmess-ws-1.txt"))
	vm, ok := decodeVmess(firstLinkLine(string(blob)))
	if !ok {
		t.Fatalf("vmess 链接解不开: %s", blob)
	}
	if str(vm["add"]) != cdnPreferredHost || str(vm["sni"]) != "xyz.trycloudflare.com" || str(vm["port"]) != "443" {
		t.Fatalf("vmess 隧道链接不对: %+v", vm)
	}
	if !strings.Contains(str(vm["ps"]), "临时隧道") {
		t.Errorf("备注应该标出临时隧道: %v", vm["ps"])
	}
}

func TestQuickWriteUnitContent(t *testing.T) {
	lp, _ := setupPanel(t)
	writeTestNode(t, lp, "vless-ws-1.json", vlessWSSample)
	n, _, err := loadNode("vless-ws-1.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := quickWriteUnit(n); err != nil {
		t.Fatalf("写单元失败: %v", err)
	}
	blob, _ := os.ReadFile(filepath.Join(lp.QuickUnitDir, "cf-quick-vless-ws-1.service"))
	text := string(blob)
	for _, want := range []string{
		"ExecStart=" + lp.QuickBin + " tunnel",
		"--url http://localhost:23457",
		"--logfile " + lp.QuickDir + "/cf-quick-vless-ws-1.log",
		"Restart=always",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("单元里缺 %q:\n%s", want, text)
		}
	}
}

func TestListQuickTunnels(t *testing.T) {
	lp, _ := setupPanel(t)
	for _, name := range []string{"cf-quick-vmess-ws-1.service", "cf-quick-watchdog.timer", "other.service"} {
		_ = os.WriteFile(filepath.Join(lp.QuickUnitDir, name), []byte("x"), 0644)
	}
	_ = os.WriteFile(filepath.Join(lp.QuickDir, "cf-quick-vmess-ws-1.log"),
		[]byte("https://q1.trycloudflare.com\n"), 0644)
	writeTestNode(t, lp, "vmess-ws-1.json", vmessWSSample)
	list := listQuickTunnels()
	if len(list) != 1 {
		t.Fatalf("应该只列出一条临时隧道: %+v", list)
	}
	if list[0].Kind != "vmess-ws" || list[0].Number != 1 || list[0].Domain != "q1.trycloudflare.com" {
		t.Fatalf("临时隧道信息不对: %+v", list[0])
	}
	if list[0].Port != 8101 {
		t.Errorf("端口没读出来: %+v", list[0])
	}
}

func TestQuickResetCreds(t *testing.T) {
	lp, _ := setupPanel(t)
	writeTestNode(t, lp, "vmess-ws-1.json", vmessWSSample)
	id, path, err := quickResetCreds("vmess-ws-1.json")
	if err != nil {
		t.Fatalf("重置凭据失败: %v", err)
	}
	if len(id) != 36 || !strings.HasSuffix(path, "-ws") {
		t.Fatalf("新凭据不对: %s %s", id, path)
	}
	doc, _ := readNodeDoc("vmess-ws-1.json")
	ib := firstInbound(doc)
	if str(inboundUserMaps(ib)[0]["uuid"]) != id {
		t.Error("UUID 没写进配置")
	}
	if str(obj(ib["transport"])["path"]) != path {
		t.Error("路径没写进配置")
	}
	_ = lp
}

func TestQuickSwitchProtocolOnlyToWS(t *testing.T) {
	lp, _ := setupPanel(t)
	// 一个 XHTTP 临时隧道入站
	body := `{"inbounds":[{"type":"vless","tag":"vless-xhttp-1","listen":"127.0.0.1","listen_port":8101,
      "users":[{"name":"u1","uuid":"11111111-2222-3333-4444-555555555555"}],
      "transport":{"type":"xhttp","path":"/abc-xhttp"}}]}`
	writeTestNode(t, lp, "vless-xhttp-1.json", body)
	_ = os.MkdirAll(lp.QuickDir, 0755)
	_ = writeFileAtomic(filepath.Join(lp.QuickDir, "vless-xhttp-1.dedicated"), nil, 0644)
	_ = os.WriteFile(filepath.Join(lp.URLDir, "vless-xhttp-1.txt"), []byte(""), 0644)

	if _, err := quickSwitchProtocol("vless-xhttp-1.json", "vless-xhttp"); err == nil {
		t.Fatal("不该允许切到 XHTTP")
	}
	n, err := quickSwitchProtocol("vless-xhttp-1.json", "vless-ws")
	if err != nil {
		t.Fatalf("XHTTP 切回 VLESS WS 失败: %v", err)
	}
	if n.Kind != "vless-ws" || n.File != "vless-ws-1.json" {
		t.Fatalf("切换结果不对: %+v", n)
	}
	if n.Path != "/abc-xhttp" {
		t.Errorf("路径应该保持不变（订阅按备注匹配）: %s", n.Path)
	}
	if n.Transport != "ws" {
		t.Errorf("传输应该改成 ws: %s", n.Transport)
	}
	// 旧文件要清掉
	if pathExists(filepath.Join(lp.ConfDir, "vless-xhttp-1.json")) {
		t.Error("旧配置文件没删掉")
	}
	// 再切回 VMess WS
	n2, err := quickSwitchProtocol("vless-ws-1.json", "vmess-ws")
	if err != nil {
		t.Fatalf("WS 之间切换失败: %v", err)
	}
	if n2.Protocol != "vmess" || n2.Kind != "vmess-ws" {
		t.Fatalf("切换结果不对: %+v", n2)
	}
}

func TestQuickResetCredsUsesXhttpPath(t *testing.T) {
	lp, _ := setupPanel(t)
	body := `{"inbounds":[{"type":"vless","tag":"vless-xhttp-1","listen":"127.0.0.1","listen_port":8101,
      "users":[{"name":"u1","uuid":"11111111-2222-3333-4444-555555555555"}],
      "transport":{"type":"xhttp","path":"/abc-xhttp"}}]}`
	writeTestNode(t, lp, "vless-xhttp-1.json", body)
	_, path, err := quickResetCreds("vless-xhttp-1.json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(path, "-xhttp") {
		t.Errorf("XHTTP 入站的路径后缀应该是 -xhttp: %s", path)
	}
}

func TestCreateQuickTunnelInbound(t *testing.T) {
	lp, fake := setupPanel(t)
	// 假装 cloudflared 已装、systemd 可用、隧道已经拿到域名
	_ = os.WriteFile(lp.QuickBin, []byte("#!/bin/sh\n"), 0755)
	_ = os.WriteFile(filepath.Join(lp.QuickDir, "cf-quick-vmess-ws-1.log"),
		[]byte("INF |  https://qt-test.trycloudflare.com  |\n"), 0644)
	oldSystemd := systemdAvailable
	systemdAvailable = func() bool { return true }
	t.Cleanup(func() { systemdAvailable = oldSystemd })
	fake.out["systemctl is-active"] = "active"

	n, err := createNode(nodeCreateSpec{Kind: quickKindAlias, Quick: true})
	if err != nil {
		t.Fatalf("建临时隧道入站失败: %v", err)
	}
	if n.File != "vmess-ws-1.json" || n.Kind != "vmess-ws" {
		t.Fatalf("应该建一条 vmess-ws 入站: %+v", n)
	}
	if n.Port != 8101 {
		t.Errorf("临时隧道专用入站默认端口应该是 8101: %d", n.Port)
	}
	doc, _ := readNodeDoc(n.File)
	if listen := str(firstInbound(doc)["listen"]); listen != "127.0.0.1" {
		t.Errorf("只应监听本机: %q", listen)
	}
	if !pathExists(filepath.Join(lp.QuickDir, "vmess-ws-1.dedicated")) {
		t.Error("应该写 dedicated 标记")
	}
	if !pathExists(filepath.Join(lp.QuickUnitDir, "cf-quick-vmess-ws-1.service")) {
		t.Error("应该写 cf-quick 单元")
	}
	blob, _ := os.ReadFile(filepath.Join(lp.URLDir, "vmess-ws-1.txt"))
	lines := strings.Fields(strings.TrimSpace(string(blob)))
	if len(lines) != 1 {
		t.Fatalf("专用隧道入站只该有一条隧道链接，实际 %d 条:\n%s", len(lines), blob)
	}
	vm, ok := decodeVmess(lines[0])
	if !ok || str(vm["sni"]) != "qt-test.trycloudflare.com" || str(vm["port"]) != "443" {
		t.Fatalf("隧道链接不对: %s", lines[0])
	}
	if n.QuickDomain != "qt-test.trycloudflare.com" {
		t.Errorf("返回的节点应该带上隧道域名: %+v", n)
	}
	if !fake.has("systemctl restart cf-quick-vmess-ws-1") {
		t.Error("应该启动隧道服务")
	}
}
