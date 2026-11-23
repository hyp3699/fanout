package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const vlessWSSample = `{
  "inbounds": [
    {
      "type": "vless",
      "tag": "vless-ws-1",
      "listen": "::",
      "listen_port": 23457,
      "users": [{"name": "vless-ws-user1", "uuid": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"}],
      "transport": {"type": "ws", "path": "/abcd-ws", "max_early_data": 2048, "early_data_header_name": "Sec-WebSocket-Protocol"}
    }
  ]
}`

func TestNextSubscriberName(t *testing.T) {
	lp, _ := setupPanel(t)
	if got := nextSubscriberName(); got != "test-user-1" {
		t.Fatalf("第一个应该是 test-user-1，实际 %s", got)
	}
	_ = os.MkdirAll(filepath.Join(lp.URLDir, "test-user-1"), 0755)
	if got := nextSubscriberName(); got != "test-user-2" {
		t.Fatalf("应该是 test-user-2，实际 %s", got)
	}
}

func TestAddSubscriberEndToEnd(t *testing.T) {
	lp, _ := setupPanel(t)
	writeTestSubSite(t, lp)
	writeTestNode(t, lp, "vless-ws-1.json", vlessWSSample)
	_ = os.WriteFile(filepath.Join(lp.URLDir, "vless-ws-1.txt"),
		[]byte("vless://aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee@203.0.113.9:23457?ed=2048&encryption=none&security=tls&sni=node.example.com&type=ws&path=/abcd-ws?ed=2048#🇯🇵vless_ws\n"), 0644)

	sub, err := addSubscriber(subAddRequest{Name: "alice", Files: []string{"vless-ws-1.json"}})
	if err != nil {
		t.Fatalf("加用户失败: %v", err)
	}
	if sub.Name != "alice" || sub.UUID == "" || sub.Path == "" {
		t.Fatalf("返回的用户信息不全: %+v", sub)
	}
	// 入站里多了 alice
	doc, _ := readNodeDoc("vless-ws-1.json")
	users := inboundUserMaps(firstInbound(doc))
	if len(users) != 2 || str(users[1]["name"]) != "alice" {
		t.Fatalf("入站里没加上用户: %+v", users)
	}
	if str(users[1]["uuid"]) != sub.UUID {
		t.Errorf("入站的 uuid 和订阅对不上")
	}
	// 用户目录四个文件
	for _, f := range []string{"alice", "alice-uuid", "alice-sub", "alice-path"} {
		if !pathExists(filepath.Join(lp.URLDir, "alice", f)) {
			t.Errorf("缺文件 %s", f)
		}
	}
	// 链接里是这个用户的 UUID
	blob, _ := os.ReadFile(filepath.Join(lp.URLDir, "alice", "alice"))
	if !strings.Contains(string(blob), sub.UUID) || !strings.Contains(string(blob), ":23457?") {
		t.Fatalf("用户链接不对: %s", blob)
	}
	// 订阅是 base64 且带流量备注
	raw, _ := os.ReadFile(filepath.Join(lp.URLDir, "alice", "alice-sub"))
	decoded, err := base64.StdEncoding.DecodeString(string(raw))
	if err != nil {
		t.Fatalf("订阅不是 base64: %v", err)
	}
	if !strings.Contains(string(decoded), "📊") || !strings.Contains(string(decoded), sub.UUID) {
		t.Fatalf("订阅内容不对: %s", decoded)
	}
	// nginx 片段指向订阅服务
	conf, _ := os.ReadFile(filepath.Join(lp.NginxUserDir, "alice.conf"))
	if !strings.Contains(string(conf), "location = "+sub.Path) ||
		!strings.Contains(string(conf), "127.0.0.1:18080/sub/alice") {
		t.Fatalf("nginx 片段不对: %s", conf)
	}
}

func TestAddSubscriberRejectsDuplicateAndBadName(t *testing.T) {
	lp, _ := setupPanel(t)
	writeTestNode(t, lp, "vless-ws-1.json", vlessWSSample)
	_ = os.WriteFile(filepath.Join(lp.URLDir, "vless-ws-1.txt"), []byte(""), 0644)
	if _, err := addSubscriber(subAddRequest{Name: "vless-ws-user1", Files: []string{"vless-ws-1.json"}}); err == nil {
		t.Fatal("同名用户应该报错")
	}
	if _, err := addSubscriber(subAddRequest{Name: "../evil", Files: []string{"vless-ws-1.json"}}); err == nil {
		t.Fatal("非法用户名应该报错")
	}
	if _, err := addSubscriber(subAddRequest{Name: "bob", Files: nil}); err == nil {
		t.Fatal("没选入站应该报错")
	}
	if _, err := addSubscriber(subAddRequest{Name: "bob", Files: []string{"../x.json"}}); err == nil {
		t.Fatal("非法文件名应该报错")
	}
}

func TestDeleteSubscriber(t *testing.T) {
	lp, _ := setupPanel(t)
	writeTestSubSite(t, lp)
	writeTestNode(t, lp, "vless-ws-1.json", vlessWSSample)
	_ = os.WriteFile(filepath.Join(lp.URLDir, "vless-ws-1.txt"),
		[]byte("vless://u@203.0.113.9:23457?security=tls&sni=node.example.com&type=ws&path=/abcd-ws#t\n"), 0644)
	if _, err := addSubscriber(subAddRequest{Name: "carol", Files: []string{"vless-ws-1.json"}}); err != nil {
		t.Fatal(err)
	}
	if err := deleteSubscriber("carol", false); err != nil {
		t.Fatalf("删用户失败: %v", err)
	}
	doc, _ := readNodeDoc("vless-ws-1.json")
	for _, u := range inboundUserMaps(firstInbound(doc)) {
		if str(u["name"]) == "carol" {
			t.Fatal("入站里还留着 carol")
		}
	}
	if pathExists(filepath.Join(lp.URLDir, "carol")) {
		t.Error("用户目录应该被移走")
	}
	if pathExists(filepath.Join(lp.NginxUserDir, "carol.conf")) {
		t.Error("nginx 片段应该被移走")
	}
	if err := deleteSubscriber("nobody", false); err == nil {
		t.Error("删不存在的用户应该报错")
	}
}

func TestRebuildSubscriberKeepsUUID(t *testing.T) {
	lp, _ := setupPanel(t)
	writeTestSubSite(t, lp)
	writeTestNode(t, lp, "vless-ws-1.json", vlessWSSample)
	_ = os.WriteFile(filepath.Join(lp.URLDir, "vless-ws-1.txt"),
		[]byte("vless://u@203.0.113.9:23457?security=tls&sni=node.example.com&type=ws&path=/abcd-ws#t\n"), 0644)
	sub, err := addSubscriber(subAddRequest{Name: "dave", UUID: "fixed-uuid-1234", Files: []string{"vless-ws-1.json"}})
	if err != nil {
		t.Fatal(err)
	}
	// 模拟协议从入站里丢了
	doc, _ := readNodeDoc("vless-ws-1.json")
	ib := firstInbound(doc)
	ib["users"] = []any{inboundUserMaps(ib)[0]}
	if err := saveNodeDoc("vless-ws-1.json", doc); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := rebuildSubscriber("dave")
	if err != nil {
		t.Fatalf("重建失败: %v", err)
	}
	if rebuilt.UUID != sub.UUID {
		t.Errorf("UUID 应该保持不变: %s vs %s", rebuilt.UUID, sub.UUID)
	}
	doc, _ = readNodeDoc("vless-ws-1.json")
	found := false
	for _, u := range inboundUserMaps(firstInbound(doc)) {
		if str(u["name"]) == "dave" && str(u["uuid"]) == sub.UUID {
			found = true
		}
	}
	if !found {
		t.Error("用户没被加回去")
	}
}

func TestAddUserToInboundTuicGetsOwnPassword(t *testing.T) {
	ib := map[string]any{
		"type":  "tuic",
		"users": []any{map[string]any{"name": "main", "uuid": "u1", "password": "shared"}},
	}
	if err := addUserToInbound(ib, "newbie", "uuid-2"); err != nil {
		t.Fatal(err)
	}
	users := inboundUserMaps(ib)
	if len(users) != 2 {
		t.Fatalf("应该有两个用户: %+v", users)
	}
	if str(users[1]["password"]) == "shared" || str(users[1]["password"]) == "" {
		t.Errorf("TUIC 新用户应该有独立密码: %+v", users[1])
	}
	if str(users[1]["uuid"]) != "uuid-2" {
		t.Errorf("UUID 没写对: %+v", users[1])
	}
}

func TestLinkUsableFiltersBareIP(t *testing.T) {
	if linkUsable("vless://u@1.2.3.4:443?security=none&type=tcp#x") {
		t.Error("纯 IP 且无 SNI 的链接应该被过滤")
	}
	if !linkUsable("vless://u@1.2.3.4:443?security=tls&sni=a.com#x") {
		t.Error("有 SNI 应该保留")
	}
	if !linkUsable("vless://u@node.example.com:443?security=none") {
		t.Error("域名地址应该保留")
	}
}

func writeTestSubSite(t *testing.T, lp panelLayout) {
	t.Helper()
	conf := "server {\n    listen 443 ssl http2;\n    server_name sub.example.com;\n}\n"
	if err := os.WriteFile(lp.NginxSubConf, []byte(conf), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestAddSubscriberCreatesSubSiteAndRejectsDuplicate(t *testing.T) {
	lp, _ := setupPanel(t)
	writeTestNode(t, lp, "vless-ws-1.json", vlessWSSample)
	_ = os.WriteFile(filepath.Join(lp.URLDir, "vless-ws-1.txt"),
		[]byte("vless://aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee@203.0.113.9:23457?security=tls&type=ws#x\n"), 0644)
	if _, err := addSubscriber(subAddRequest{Name: "bob", Files: []string{"vless-ws-1.json"}}); err == nil {
		t.Fatal("没有订阅主站也没选证书，应该报错")
	}
	if pathExists(filepath.Join(lp.URLDir, "bob")) {
		t.Fatal("失败时不该留下用户目录")
	}
	cdir := filepath.Join(lp.CertDirs[0], "sub.example.com")
	_ = os.MkdirAll(cdir, 0755)
	_ = os.WriteFile(filepath.Join(cdir, "fullchain.pem"), []byte("x"), 0644)
	_ = os.WriteFile(filepath.Join(cdir, "privkey.pem"), []byte("x"), 0644)
	sub, err := addSubscriber(subAddRequest{Name: "bob", Files: []string{"vless-ws-1.json"},
		SubCert: "sub.example.com", SubPort: 2053})
	if err != nil {
		t.Fatalf("加用户失败: %v", err)
	}
	if !strings.HasPrefix(sub.SubURL, "https://sub.example.com:2053/") {
		t.Fatalf("订阅地址不对: %q", sub.SubURL)
	}
	if _, err := addSubscriber(subAddRequest{Name: "bob", Files: []string{"vless-ws-1.json"}}); err == nil ||
		!strings.Contains(err.Error(), "已存在") {
		t.Fatalf("重名应该拒绝，实际 %v", err)
	}
}
