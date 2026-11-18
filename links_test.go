package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSplitShareLinkAndKeys(t *testing.T) {
	a := splitShareLink("vless://uuid-1@1.2.3.4:443?security=tls&sni=a.com&type=ws#节点")
	b := splitShareLink("vless://uuid-2@1.2.3.4:8443?type=ws&sni=a.com&security=tls#节点")
	if a == nil || b == nil {
		t.Fatal("解析失败")
	}
	if exactKey(a) == exactKey(b) {
		t.Error("端口不同，强匹配不该相等")
	}
	if looseKey(a) != looseKey(b) {
		t.Errorf("忽略端口与 sni 后应该相等：%q vs %q", looseKey(a), looseKey(b))
	}
	if _, ok := quickTunnelKey(a); ok {
		t.Error("普通链接不该被当成临时隧道")
	}
	qt := splitShareLink("vless://x@1.1.1.1:443?type=ws#🇯🇵临时隧道")
	if _, ok := quickTunnelKey(qt); !ok {
		t.Error("临时隧道链接没识别出来")
	}
}

func TestApplyUserCred(t *testing.T) {
	src := "vless://1111@1.2.3.4:443?security=reality&pbk=k&sni=s.com#t"
	got := applyUserCred(src, "vless://2222@0.0.0.0:1", "2222")
	if !strings.HasPrefix(got, "vless://2222@1.2.3.4:443?") {
		t.Fatalf("vless 换凭据失败: %s", got)
	}

	tuic := "tuic://1111:pass1@1.2.3.4:443?alpn=h3#t"
	got = applyUserCred(tuic, "tuic://2222:userpw@0.0.0.0:1", "2222")
	if !strings.HasPrefix(got, "tuic://2222:userpw@") {
		t.Fatalf("tuic 应保留用户自己的密码: %s", got)
	}

	socks := "socks://u1:p1@1.2.3.4:1080#t"
	got = applyUserCred(socks, "socks://u2:p2@0.0.0.0:1", "2222")
	if !strings.HasPrefix(got, "socks://u2:p2@") {
		t.Fatalf("socks 应使用用户的用户名密码: %s", got)
	}

	vm := "vmess://" + base64.StdEncoding.EncodeToString([]byte(`{"v":"2","add":"1.2.3.4","port":"443","id":"1111","net":"ws","ps":"x"}`))
	got = applyUserCred(vm, "vmess://x", "2222")
	decoded, ok := decodeVmess(got)
	if !ok || str(decoded["id"]) != "2222" {
		t.Fatalf("vmess 换 id 失败: %s", got)
	}
	if str(decoded["port"]) != "443" {
		t.Errorf("vmess 端口不该变: %v", decoded["port"])
	}
}

func TestRebuildSubFile(t *testing.T) {
	lp, _ := setupPanel(t)
	_ = os.WriteFile(filepath.Join(lp.URLDir, "b-1.txt"), []byte("line-b\n"), 0644)
	_ = os.WriteFile(filepath.Join(lp.URLDir, "a-1.txt"), []byte("line-a\n"), 0644)
	if err := rebuildSubFile(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(lp.SubFile)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(string(raw))
	if err != nil {
		t.Fatalf("订阅不是合法 base64: %v", err)
	}
	text := string(decoded)
	if strings.Index(text, "line-a") > strings.Index(text, "line-b") {
		t.Errorf("订阅应该按文件名排序: %s", text)
	}
}

func TestUpdateURLFilePort(t *testing.T) {
	lp, _ := setupPanel(t)
	path := filepath.Join(lp.URLDir, "mix.txt")
	body := "vless://u@1.2.3.4:443?type=ws&path=/a443#t\n" +
		"vmess://" + base64.StdEncoding.EncodeToString([]byte(`{"add":"1.2.3.4","port":"443","id":"x","ps":"v"}`)) + "\n"
	_ = os.WriteFile(path, []byte(body), 0644)
	if err := updateURLFilePort(path, 443, 2053); err != nil {
		t.Fatal(err)
	}
	blob, _ := os.ReadFile(path)
	text := string(blob)
	if !strings.Contains(text, ":2053?") {
		t.Errorf("vless 端口没改: %s", text)
	}
	if !strings.Contains(text, "/a443") {
		t.Errorf("路径里的 443 不该被改: %s", text)
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "vmess://") {
			vm, ok := decodeVmess(line)
			if !ok || str(vm["port"]) != "2053" {
				t.Errorf("vmess 端口没改: %s", line)
			}
		}
	}
}

func TestRefreshUserSubsFollowsInboundChanges(t *testing.T) {
	lp, _ := setupPanel(t)
	// 入站链接换成了新端口和新 SNI
	_ = os.WriteFile(filepath.Join(lp.URLDir, "vless-ws-1.txt"),
		[]byte("vless://inbound-uuid@1.2.3.4:2053?security=tls&sni=new.example.com&type=ws&path=/w#node\n"), 0644)
	// 用户还是老链接（旧端口、旧 SNI、旧 UUID）
	userDir := filepath.Join(lp.URLDir, "test-user-1")
	_ = os.MkdirAll(userDir, 0755)
	_ = os.WriteFile(filepath.Join(userDir, "test-user-1-uuid"), []byte("test-user-1\nuser-uuid-1\n"), 0644)
	_ = os.WriteFile(filepath.Join(userDir, "test-user-1"),
		[]byte("vless://user-uuid-1@1.2.3.4:443?security=tls&sni=old.example.com&type=ws&path=/w#node\n"), 0644)
	_ = os.WriteFile(filepath.Join(userDir, "test-user-1-sub"), []byte("old"), 0644)

	if err := refreshUserSubs(); err != nil {
		t.Fatal(err)
	}
	blob, _ := os.ReadFile(filepath.Join(userDir, "test-user-1"))
	text := string(blob)
	if !strings.Contains(text, ":2053?") || !strings.Contains(text, "sni=new.example.com") {
		t.Fatalf("订阅没跟上入站变化: %s", text)
	}
	if !strings.Contains(text, "user-uuid-1") {
		t.Fatalf("用户的 UUID 应该被保留: %s", text)
	}
	sub, _ := os.ReadFile(filepath.Join(userDir, "test-user-1-sub"))
	decoded, err := base64.StdEncoding.DecodeString(string(sub))
	if err != nil {
		t.Fatal("订阅文件不是 base64")
	}
	if !strings.Contains(string(decoded), "📊") {
		t.Errorf("订阅第一行应该是流量备注: %s", decoded)
	}
}

func TestRefreshUserSubsParksMissingQuickTunnel(t *testing.T) {
	lp, _ := setupPanel(t)
	_ = os.WriteFile(filepath.Join(lp.URLDir, "vless-ws-1.txt"),
		[]byte("vless://u@1.2.3.4:443?security=none&type=ws&path=/w#node\n"), 0644)
	userDir := filepath.Join(lp.URLDir, "u1")
	_ = os.MkdirAll(userDir, 0755)
	_ = os.WriteFile(filepath.Join(userDir, "u1-uuid"), []byte("u1\nuuid-1\n"), 0644)
	_ = os.WriteFile(filepath.Join(userDir, "u1"),
		[]byte("vless://uuid-1@1.2.3.4:443?security=none&type=ws&path=/w#node\n"+
			"vless://uuid-1@5.6.7.8:443?security=tls&sni=x.trycloudflare.com&type=ws&path=/w#临时隧道\n"), 0644)
	if err := refreshUserSubs(); err != nil {
		t.Fatal(err)
	}
	blob, _ := os.ReadFile(filepath.Join(userDir, "u1"))
	if strings.Contains(string(blob), "trycloudflare") {
		t.Error("隧道没了，链接应该从订阅里拿出来")
	}
	pending, err := os.ReadFile(filepath.Join(userDir, "u1-qtpending"))
	if err != nil || !strings.Contains(string(pending), "临时隧道") {
		t.Error("暂停的临时隧道链接应写进 -qtpending")
	}
}

func TestTrafficLineFor(t *testing.T) {
	lp, _ := setupPanel(t)
	_ = os.MkdirAll(filepath.Join(lp.UserManagerDir, "traffic"), 0755)
	_ = os.WriteFile(lp.TrafficState, []byte(`{"users":{"u1":{"period_total":1610612736}}}`), 0644)
	_ = os.WriteFile(filepath.Join(lp.LimitsDir, "u1.json"),
		[]byte(`{"enabled":true,"limit_bytes":5368709120}`), 0644)
	line := trafficLineFor("u1")
	decoded, err := unescapePath(line)
	if err != nil {
		t.Fatal(err)
	}
	line = decoded
	if !strings.Contains(line, "剩余流量: 3.50 GB") || !strings.Contains(line, "已用: 1.50 GB") {
		t.Fatalf("流量备注不对: %s", line)
	}
	if line := trafficLineFor("nobody"); !strings.Contains(line, "无限制") {
		t.Fatalf("没有限额时应该显示无限制: %s", line)
	}
}

func TestV2RayUsersEdit(t *testing.T) {
	lp, _ := setupPanel(t)
	_ = os.WriteFile(lp.MainConfig, []byte(`{"experimental":{"v2ray_api":{"listen":"127.0.0.1:9094","stats":{"enabled":true,"users":["a"]}}}}`), 0600)
	if err := addV2RayUser("b"); err != nil {
		t.Fatal(err)
	}
	if err := addV2RayUser("b"); err != nil {
		t.Fatal(err)
	}
	blob, _ := os.ReadFile(lp.MainConfig)
	if strings.Count(string(blob), `"b"`) != 1 {
		t.Fatalf("重复添加应该去重: %s", blob)
	}
	if err := removeV2RayUser("a"); err != nil {
		t.Fatal(err)
	}
	blob, _ = os.ReadFile(lp.MainConfig)
	if strings.Contains(string(blob), `"a"`) {
		t.Fatalf("没删掉: %s", blob)
	}
}
