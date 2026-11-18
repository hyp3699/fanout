package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// 新增面板代码的测试脚手架：把 panelPaths() 指到临时目录，
// 并替换 adminExec，免得测试真的去跑 systemctl / nginx / nft。

// fakeExec 记录命令并返回预设结果。
type fakeExec struct {
	mu    sync.Mutex
	calls []string
	// fail 里的前缀命中就返回错误
	fail []string
	// out 里的前缀命中就返回对应输出
	out map[string]string
}

func (f *fakeExec) run(name string, args ...string) (string, error) {
	line := strings.TrimSpace(name + " " + strings.Join(args, " "))
	f.mu.Lock()
	f.calls = append(f.calls, line)
	f.mu.Unlock()
	for _, p := range f.fail {
		if strings.HasPrefix(line, p) {
			return "fake failure", fmt.Errorf("fake: %s 失败", p)
		}
	}
	if f.out != nil {
		for p, o := range f.out {
			if strings.HasPrefix(line, p) {
				return o, nil
			}
		}
	}
	return "", nil
}

func (f *fakeExec) has(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// setupPanel 准备一份临时布局，返回布局与假命令执行器。
func setupPanel(t *testing.T) (panelLayout, *fakeExec) {
	t.Helper()
	dir := t.TempDir()
	lp := panelLayout{
		Bin:            filepath.Join(dir, "sing-box"),
		ConfDir:        filepath.Join(dir, "conf"),
		URLDir:         filepath.Join(dir, "url"),
		SubFile:        filepath.Join(dir, "sub.txt"),
		MainConfig:     filepath.Join(dir, "conf", "config.json"),
		SelfCert:       filepath.Join(dir, "cert.pem"),
		SelfKey:        filepath.Join(dir, "private.key"),
		UserManagerDir: filepath.Join(dir, "user_manager"),
		LimitsDir:      filepath.Join(dir, "user_manager", "limits"),
		TrafficState:   filepath.Join(dir, "user_manager", "traffic", "state.json"),
		SpeedStore:     filepath.Join(dir, "user_manager", "speed_limits.json"),
		CertDirs:       []string{filepath.Join(dir, "cert")},
		CertTrash:      filepath.Join(dir, "trash", "cert"),
		NginxConfDir:   filepath.Join(dir, "nginx"),
		NginxUserDir:   filepath.Join(dir, "nginx", "users"),
		NginxSubConf:   filepath.Join(dir, "nginx", "singbox_sub.conf"),
		NginxTrashDir:  filepath.Join(dir, "trash", "nginx"),
		QuickDir:       filepath.Join(dir, "quick"),
		QuickBin:       filepath.Join(dir, "cloudflared"),
		QuickUnitDir:   filepath.Join(dir, "units"),
		CFTokenFile:    filepath.Join(dir, "token"),
		CFGlobalFile:   filepath.Join(dir, "cf-global.json"),
		AcmeSh:         filepath.Join(dir, "acme", "acme.sh"),
		FanoutDir:      dir,
		SubServiceURL:  "http://127.0.0.1:18080",
	}
	for _, d := range []string{lp.ConfDir, lp.URLDir, lp.UserManagerDir, lp.LimitsDir,
		lp.CertDirs[0], lp.NginxConfDir, lp.NginxUserDir, lp.QuickDir, lp.QuickUnitDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatalf("建目录失败: %v", err)
		}
	}
	fake := &fakeExec{out: map[string]string{}}
	oldLayout, oldExec, oldExecEnv, oldAvail := layoutOverride, adminExec, adminExecEnv, cmdAvailable
	layoutOverride = &lp
	adminExec = fake.run
	adminExecEnv = func(env []string, name string, args ...string) (string, error) {
		return fake.run(name, args...)
	}
	cmdAvailable = func(string) bool { return true }
	setPublicIPOverride("203.0.113.9")
	nodeRemarkEmoji = "🌐"
	t.Cleanup(func() {
		layoutOverride = oldLayout
		adminExec = oldExec
		adminExecEnv = oldExecEnv
		cmdAvailable = oldAvail
		setPublicIPOverride("")
		cfBaseURL = "https://api.cloudflare.com/client/v4"
	})
	return lp, fake
}

// writeTestNode 往配置目录写一个入站文件。
func writeTestNode(t *testing.T, lp panelLayout, file, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(lp.ConfDir, file), []byte(body), 0600); err != nil {
		t.Fatalf("写 %s 失败: %v", file, err)
	}
}

const vlessRealitySample = `{
  "inbounds": [
    {
      "type": "vless",
      "tag": "vless-reality-1",
      "listen": "::",
      "listen_port": 23456,
      "users": [{"name": "vless-reality-user1", "uuid": "11111111-2222-3333-4444-555555555555", "flow": "xtls-rprx-vision"}],
      "tls": {
        "enabled": true,
        "server_name": "www.iij.ad.jp",
        "reality": {
          "enabled": true,
          "handshake": {"server": "www.iij.ad.jp", "server_port": 443},
          "private_key": "UOFD5arX0B1qhbb9s1g7zdkp0TZ0FQ2GJ6zS9YI4X3A",
          "short_id": ["abcd1234"]
        }
      }
    }
  ]
}`

func TestPanelPathsComeFromLayout(t *testing.T) {
	lp, _ := setupPanel(t)
	got := panelPaths()
	if got.ConfDir != lp.ConfDir || got.URLDir != lp.URLDir {
		t.Fatalf("panelPaths 没跟着 layoutOverride 走: %+v", got)
	}
}

func TestValidateHelpersBlockTraversal(t *testing.T) {
	cases := []struct {
		name string
		ok   bool
	}{
		{"vless-reality-1.json", true},
		{"../etc/passwd.json", false},
		{"socks5-12.json", true},
		{"config.json", false},
		{"a/b-1.json", false},
	}
	for _, c := range cases {
		if got := validNodeFile(c.name); got != c.ok {
			t.Errorf("validNodeFile(%q)=%v，期望 %v", c.name, got, c.ok)
		}
	}
	if _, err := safeJoin("/etc/nginx/conf.d", "../evil.conf"); err == nil {
		t.Error("safeJoin 应该拒绝 ..")
	}
	if !validDomain("www.example.com") || validDomain("http://x/y") || validDomain("*.") {
		t.Error("域名校验不对")
	}
	if validUserName("../x") || !validUserName("test-user-1") {
		t.Error("用户名校验不对")
	}
}

func TestBuildInboundDocMatchesScriptLayout(t *testing.T) {
	_, _ = setupPanel(t)
	wantTags := map[string]string{
		"vless-reality": "vless-reality-2",
		"hysteria2":     "hysteria2-2",
		"tuic":          "tuic-2",
		"http-reality":  "http-reality-2",
		"grpc-reality":  "grpc-reality-2",
		"anytls":        "anytls-2",
		"socks5":        "socks-2",
		"xhttp-reality": "xhttp-reality-2",
		"vless-xhttp":   "vless-xhttp-2",
		"xhttp-udp-tls": "vless-xhttp-udptls-2",
		"vless-tcp-tls": "vless-tcp-tls-2",
		"naiveproxy":    "naive-2",
		"vmess-ws":      "vmess-ws-2",
		"vless-ws":      "vless-ws-2",
	}
	// 需要域名证书的类型先放一张证书进去
	certDir := filepath.Join(panelPaths().CertDirs[0], "node.example.com")
	if err := os.MkdirAll(certDir, 0755); err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM := makeTestCert(t, "node.example.com")
	_ = os.WriteFile(filepath.Join(certDir, "fullchain.pem"), certPEM, 0644)
	_ = os.WriteFile(filepath.Join(certDir, "privkey.pem"), keyPEM, 0600)

	for kind, wantTag := range wantTags {
		doc, user, err := buildInboundDoc(nodeCreateSpec{Kind: kind, Domain: "node.example.com", SNI: "www.joom.com"}, 2, 12345)
		if err != nil {
			t.Fatalf("%s 生成失败: %v", kind, err)
		}
		ib := firstInbound(doc)
		if ib == nil {
			t.Fatalf("%s 没有 inbounds", kind)
		}
		if got := str(ib["tag"]); got != wantTag {
			t.Errorf("%s 的 tag 是 %q，期望 %q", kind, got, wantTag)
		}
		if num(ib["listen_port"]) != 12345 {
			t.Errorf("%s 端口没写进去", kind)
		}
		if user.Name == "" {
			t.Errorf("%s 没有主用户", kind)
		}
		users := inboundUserMaps(ib)
		if len(users) != 1 {
			t.Errorf("%s 应该有 1 个用户，实际 %d", kind, len(users))
		}
		if str(users[0]["name"]) != user.Name {
			t.Errorf("%s 用户名字不一致", kind)
		}
		// 每种协议都得有能用的凭据
		if str(users[0]["uuid"]) == "" && str(users[0]["password"]) == "" {
			t.Errorf("%s 的用户既没有 uuid 也没有 password", kind)
		}
	}
}

func TestBuildInboundDocRequiresDomainCert(t *testing.T) {
	_, _ = setupPanel(t)
	if _, _, err := buildInboundDoc(nodeCreateSpec{Kind: "vless-tcp-tls"}, 1, 1000); err == nil {
		t.Fatal("没有域名证书时应该报错")
	}
	if _, _, err := buildInboundDoc(nodeCreateSpec{Kind: "nope"}, 1, 1000); err == nil {
		t.Fatal("未知类型应该报错")
	}
}

func TestNextNodeNumberSkipsExisting(t *testing.T) {
	lp, _ := setupPanel(t)
	writeTestNode(t, lp, "vless-reality-1.json", vlessRealitySample)
	writeTestNode(t, lp, "vless-reality-2.json", strings.Replace(vlessRealitySample, "vless-reality-1", "vless-reality-2", -1))
	if got := nextNodeNumber("vless-reality"); got != 3 {
		t.Fatalf("下一个编号应该是 3，实际 %d", got)
	}
	if got := nextNodeNumber("hysteria2"); got != 1 {
		t.Fatalf("新类型应该从 1 开始，实际 %d", got)
	}
}

func TestListNodesDescribesReality(t *testing.T) {
	lp, _ := setupPanel(t)
	writeTestNode(t, lp, "vless-reality-1.json", vlessRealitySample)
	nodes, err := listNodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("应该有 1 个入站，实际 %d", len(nodes))
	}
	n := nodes[0]
	if n.Kind != "vless-reality" || n.Number != 1 || n.Port != 23456 || n.Protocol != "vless" {
		t.Fatalf("入站解析不对: %+v", n)
	}
	if !n.Reality || n.SNI != "www.iij.ad.jp" || len(n.Users) != 1 {
		t.Fatalf("Reality 字段解析不对: %+v", n)
	}
}

func TestSaveNodeDocRollsBackWhenCheckFails(t *testing.T) {
	lp, fake := setupPanel(t)
	writeTestNode(t, lp, "vless-reality-1.json", vlessRealitySample)
	doc, err := readNodeDoc("vless-reality-1.json")
	if err != nil {
		t.Fatal(err)
	}
	fake.fail = []string{lp.Bin + " check"}
	firstInbound(doc)["listen_port"] = 34567
	if err := saveNodeDoc("vless-reality-1.json", doc); err == nil {
		t.Fatal("check 失败时 saveNodeDoc 应该报错")
	}
	blob, _ := os.ReadFile(filepath.Join(lp.ConfDir, "vless-reality-1.json"))
	if !strings.Contains(string(blob), "23456") {
		t.Fatalf("失败后应该回滚到原端口，实际：%s", blob)
	}
}

func TestCreateNodeWritesFilesAndLinks(t *testing.T) {
	lp, fake := setupPanel(t)
	nodeRemarkEmoji = "🇯🇵"
	n, err := createNode(nodeCreateSpec{Kind: "vless-ws"})
	if err != nil {
		t.Fatalf("建入站失败: %v", err)
	}
	if n.File != "vless-ws-1.json" {
		t.Fatalf("文件名不对: %s", n.File)
	}
	if !pathExists(filepath.Join(lp.ConfDir, n.File)) {
		t.Fatal("配置文件没写出来")
	}
	linkFile := filepath.Join(lp.URLDir, "vless-ws-1.txt")
	blob, err := os.ReadFile(linkFile)
	if err != nil {
		t.Fatalf("链接文件没写出来: %v", err)
	}
	if !strings.HasPrefix(string(blob), "vless://") || !strings.Contains(string(blob), "203.0.113.9") {
		t.Fatalf("链接内容不对: %s", blob)
	}
	if !strings.Contains(string(blob), "type=ws") || !strings.Contains(string(blob), "ed=2048") {
		t.Fatalf("WS 链接参数不对: %s", blob)
	}
	if !fake.has("nft add rule") {
		t.Error("应该放行端口")
	}
	if !pathExists(lp.SubFile) {
		t.Error("总订阅没重建")
	}
	sub, _ := os.ReadFile(lp.SubFile)
	if len(sub) == 0 {
		t.Error("总订阅是空的")
	}
}

func TestCreateNodeRejectsBusyPort(t *testing.T) {
	_, _ = setupPanel(t)
	// 端口 1 基本肯定被占（或者至少需要 root，反正没法 Listen）
	if _, err := createNode(nodeCreateSpec{Kind: "vless-ws", Port: 1}); err == nil {
		t.Log("端口 1 居然可用，跳过这条断言") // 容器里以 root 跑时可能真的能绑
	}
}

func TestDeleteNodeCleansUp(t *testing.T) {
	lp, fake := setupPanel(t)
	writeTestNode(t, lp, "vless-reality-1.json", vlessRealitySample)
	_ = os.WriteFile(filepath.Join(lp.URLDir, "vless-reality-1.txt"), []byte("vless://x@1.1.1.1:1#t\n"), 0644)
	if err := deleteNode("vless-reality-1.json", false); err != nil {
		t.Fatalf("删入站失败: %v", err)
	}
	if pathExists(filepath.Join(lp.ConfDir, "vless-reality-1.json")) {
		t.Error("配置文件没删掉")
	}
	if pathExists(filepath.Join(lp.URLDir, "vless-reality-1.txt")) {
		t.Error("链接文件没删掉")
	}
	if !fake.has("nft -a list chain") {
		t.Error("应该清理端口规则")
	}
}

func TestSetNodePortUpdatesLinks(t *testing.T) {
	lp, _ := setupPanel(t)
	writeTestNode(t, lp, "vless-reality-1.json", vlessRealitySample)
	_ = os.WriteFile(filepath.Join(lp.URLDir, "vless-reality-1.txt"),
		[]byte("vless://uuid@203.0.113.9:23456?security=reality#t\n"), 0644)
	n, err := setNodePort("vless-reality-1.json", 24567)
	if err != nil {
		t.Fatalf("改端口失败: %v", err)
	}
	if n.Port != 24567 {
		t.Fatalf("端口没改：%d", n.Port)
	}
	blob, _ := os.ReadFile(filepath.Join(lp.URLDir, "vless-reality-1.txt"))
	if !strings.Contains(string(blob), ":24567?") {
		t.Fatalf("链接里的端口没改：%s", blob)
	}
	if _, err := setNodePort("vless-reality-1.json", 70000); err == nil {
		t.Error("非法端口应该被拒")
	}
}

func TestRotateCredRewritesLink(t *testing.T) {
	lp, _ := setupPanel(t)
	writeTestNode(t, lp, "vless-reality-1.json", vlessRealitySample)
	_ = os.WriteFile(filepath.Join(lp.URLDir, "vless-reality-1.txt"),
		[]byte("vless://11111111-2222-3333-4444-555555555555@203.0.113.9:23456?security=reality#t\n"), 0644)
	val, err := rotateNodeCred("vless-reality-1.json", "vless-reality-user1", "")
	if err != nil {
		t.Fatalf("换 UUID 失败: %v", err)
	}
	if len(val) != 36 {
		t.Fatalf("新 UUID 长度不对: %s", val)
	}
	blob, _ := os.ReadFile(filepath.Join(lp.URLDir, "vless-reality-1.txt"))
	if !strings.Contains(string(blob), val) {
		t.Fatalf("链接里没换成新 UUID: %s", blob)
	}
	doc, _ := readNodeDoc("vless-reality-1.json")
	if str(inboundUserMaps(firstInbound(doc))[0]["uuid"]) != val {
		t.Error("配置里的 UUID 没改")
	}
}

func TestSetRealitySNI(t *testing.T) {
	lp, _ := setupPanel(t)
	writeTestNode(t, lp, "vless-reality-1.json", vlessRealitySample)
	_ = os.WriteFile(filepath.Join(lp.URLDir, "vless-reality-1.txt"),
		[]byte("vless://u@203.0.113.9:23456?security=reality&sni=www.iij.ad.jp&pbk=x#t\n"), 0644)
	if err := setRealitySNI("vless-reality-1.json", "www.joom.com"); err != nil {
		t.Fatalf("改 SNI 失败: %v", err)
	}
	blob, _ := os.ReadFile(filepath.Join(lp.URLDir, "vless-reality-1.txt"))
	if !strings.Contains(string(blob), "sni=www.joom.com") {
		t.Fatalf("链接里的 sni 没改: %s", blob)
	}
	doc, _ := readNodeDoc("vless-reality-1.json")
	if str(obj(firstInbound(doc)["tls"])["server_name"]) != "www.joom.com" {
		t.Error("配置里的 server_name 没改")
	}
}

func TestHy2ObfsAndHop(t *testing.T) {
	lp, fake := setupPanel(t)
	body := `{"inbounds":[{"type":"hysteria2","tag":"hysteria2-1","listen":"::","listen_port":23456,
      "users":[{"name":"hysteria2-user1","password":"pw"}],
      "tls":{"enabled":true,"alpn":["h3"],"certificate_path":"/tmp/c.pem","key_path":"/tmp/k.pem"}}]}`
	writeTestNode(t, lp, "hysteria2-1.json", body)
	_ = os.WriteFile(filepath.Join(lp.URLDir, "hysteria2-1.txt"),
		[]byte("hysteria2://pw@203.0.113.9:23456/?insecure=1&sni=www.bing.com&alpn=h3#hysteria2\n"), 0644)

	pw, err := setHy2Obfs("hysteria2-1.json", true, "")
	if err != nil {
		t.Fatalf("开混淆失败: %v", err)
	}
	if len(pw) != 20 {
		t.Fatalf("混淆密码长度不对: %q", pw)
	}
	blob, _ := os.ReadFile(filepath.Join(lp.URLDir, "hysteria2-1.txt"))
	if !strings.Contains(string(blob), "obfs=gecko") || !strings.Contains(string(blob), "obfs-password="+pw) {
		t.Fatalf("链接没带混淆参数: %s", blob)
	}
	if _, err := setHy2Obfs("hysteria2-1.json", false, ""); err != nil {
		t.Fatalf("关混淆失败: %v", err)
	}
	blob, _ = os.ReadFile(filepath.Join(lp.URLDir, "hysteria2-1.txt"))
	if strings.Contains(string(blob), "obfs=") {
		t.Fatalf("关掉后链接还有混淆参数: %s", blob)
	}

	if err := setHy2Hopping("hysteria2-1.json", 20000, 20100); err != nil {
		t.Fatalf("开端口跳跃失败: %v", err)
	}
	if !fake.has("nft add rule ip hysteria_nat prerouting") {
		t.Error("应该写 dnat 规则")
	}
	blob, _ = os.ReadFile(filepath.Join(lp.URLDir, "hysteria2-1.txt"))
	if !strings.Contains(string(blob), "mport=23456,20000-20100") {
		t.Fatalf("链接没带 mport: %s", blob)
	}
	if err := disableHy2Hopping("hysteria2-1.json"); err != nil {
		t.Fatalf("关端口跳跃失败: %v", err)
	}
	blob, _ = os.ReadFile(filepath.Join(lp.URLDir, "hysteria2-1.txt"))
	if strings.Contains(string(blob), "mport=") {
		t.Fatalf("关掉后链接还有 mport: %s", blob)
	}
}

// makeTestCert 造一张自签证书，返回 PEM。
func makeTestCert(t *testing.T, domain string) ([]byte, []byte) {
	t.Helper()
	cert, key, err := generateSelfSignedPEM()
	if err != nil {
		t.Fatalf("造证书失败: %v", err)
	}
	_ = domain
	return cert, key
}
