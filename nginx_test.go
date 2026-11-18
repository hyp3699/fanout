package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildProxyConf(t *testing.T) {
	_, _ = setupPanel(t)
	conf, err := buildProxyConf(proxySpec{Domain: "app.example.com", ListenPort: 80,
		Target: "127.0.0.1:9393", WebSocket: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"server_name app.example.com;", "listen 80;",
		"proxy_pass http://127.0.0.1:9393;", "Upgrade $http_upgrade", "Connection \"upgrade\""} {
		if !strings.Contains(conf, want) {
			t.Errorf("配置里缺 %q:\n%s", want, conf)
		}
	}
	if _, err := buildProxyConf(proxySpec{Domain: "bad domain", ListenPort: 80, Target: "127.0.0.1:1"}); err == nil {
		t.Error("非法域名应该报错")
	}
	if _, err := buildProxyConf(proxySpec{Domain: "a.example.com", ListenPort: 80, Target: "ftp://x"}); err == nil {
		t.Error("非 http(s) 目标应该报错")
	}
	if _, err := buildProxyConf(proxySpec{Domain: "a.example.com", ListenPort: 443, Target: "127.0.0.1:1", TLS: true}); err == nil {
		t.Error("没有证书时开 TLS 应该报错")
	}
}

func TestBuildProxyConfWithTLS(t *testing.T) {
	lp, _ := setupPanel(t)
	dir := filepath.Join(lp.CertDirs[0], "app.example.com")
	_ = os.MkdirAll(dir, 0755)
	certPEM, keyPEM := makeTestCert(t, "app.example.com")
	_ = os.WriteFile(filepath.Join(dir, "fullchain.pem"), certPEM, 0644)
	_ = os.WriteFile(filepath.Join(dir, "privkey.pem"), keyPEM, 0600)
	conf, err := buildProxyConf(proxySpec{Domain: "app.example.com", ListenPort: 443,
		Target: "https://127.0.0.1:8443", TLS: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(conf, "ssl_certificate "+filepath.Join(dir, "fullchain.pem")) {
		t.Errorf("没绑证书:\n%s", conf)
	}
	if !strings.Contains(conf, "proxy_pass https://127.0.0.1:8443;") {
		t.Errorf("https 回源没保留:\n%s", conf)
	}
}

func TestNginxSitePathRejectsTraversal(t *testing.T) {
	_, _ = setupPanel(t)
	if _, err := nginxSitePath("../evil.conf", false); err == nil {
		t.Error("应该拒绝 ..")
	}
	if _, err := nginxSitePath("good.conf", false); err != nil {
		t.Errorf("正常文件名不该被拒: %v", err)
	}
}

func TestSaveNginxSiteRollsBackOnBadConfig(t *testing.T) {
	lp, fake := setupPanel(t)
	path := filepath.Join(lp.NginxConfDir, "site.conf")
	_ = os.WriteFile(path, []byte("server { listen 80; }\n"), 0644)
	fake.fail = []string{"nginx -t"}
	if err := os.WriteFile(path, []byte("server { listen ; }\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// 用 saveNginxSite 的路径：它自己写文件 + 校验 + 回滚
	fake.fail = nil
	if err := os.WriteFile(path, []byte("server { listen 80; }\n"), 0644); err != nil {
		t.Fatal(err)
	}
	fake.fail = []string{"nginx -t"}
	err := saveNginxSite("site.conf", "server { listen ; }", false)
	if err == nil {
		t.Fatal("nginx -t 失败时应该报错")
	}
	blob, _ := os.ReadFile(path)
	if !strings.Contains(string(blob), "listen 80;") {
		t.Fatalf("应该回滚到旧内容，实际：%s", blob)
	}
}

func TestDeleteNginxSiteProtectsScriptFiles(t *testing.T) {
	lp, _ := setupPanel(t)
	_ = os.WriteFile(filepath.Join(lp.NginxConfDir, "singbox_sub.conf"), []byte("x"), 0644)
	if err := deleteNginxSite("singbox_sub.conf", false); err == nil {
		t.Fatal("脚本的订阅配置不能删")
	}
	if !pathExists(filepath.Join(lp.NginxConfDir, "singbox_sub.conf")) {
		t.Error("文件不该被删掉")
	}
}

func TestDeleteNginxSiteMovesToTrash(t *testing.T) {
	lp, _ := setupPanel(t)
	_ = os.WriteFile(filepath.Join(lp.NginxConfDir, "old.conf"), []byte("server { listen 8080; }\n"), 0644)
	if err := deleteNginxSite("old.conf", false); err != nil {
		t.Fatalf("删站点失败: %v", err)
	}
	if pathExists(filepath.Join(lp.NginxConfDir, "old.conf")) {
		t.Error("文件应该被移走")
	}
	entries, _ := os.ReadDir(lp.NginxTrashDir)
	if len(entries) == 0 {
		t.Error("应该留在回收目录里")
	}
}

func TestListNginxSitesParsesServerBlock(t *testing.T) {
	lp, _ := setupPanel(t)
	_ = os.WriteFile(filepath.Join(lp.NginxConfDir, "app.conf"), []byte(`server {
    listen 443 ssl http2;
    server_name app.example.com;
    location / { proxy_pass http://127.0.0.1:9393; }
}
`), 0644)
	_ = os.WriteFile(filepath.Join(lp.NginxUserDir, "u1.conf"), []byte("location = /abc {\nproxy_pass http://127.0.0.1:18080/sub/u1;\n}\n"), 0644)
	_ = os.WriteFile(filepath.Join(lp.NginxConfDir, "singbox_sub.conf"), []byte("server { listen 443 ssl; server_name sub.example.com; }\n"), 0644)

	sites := listNginxSites()
	if len(sites) != 3 {
		t.Fatalf("应该列出 3 个文件，实际 %d", len(sites))
	}
	var app *nginxSite
	var sub *nginxSite
	var user *nginxSite
	for i := range sites {
		switch sites[i].Name {
		case "app.conf":
			app = &sites[i]
		case "singbox_sub.conf":
			sub = &sites[i]
		case "u1.conf":
			user = &sites[i]
		}
	}
	if app == nil || len(app.ServerNames) == 0 || app.ServerNames[0] != "app.example.com" {
		t.Fatalf("server_name 没解析出来: %+v", app)
	}
	if len(app.ProxyPass) == 0 || !strings.Contains(app.ProxyPass[0], "9393") {
		t.Errorf("proxy_pass 没解析出来: %+v", app)
	}
	if sub == nil || !sub.Protected {
		t.Error("singbox_sub.conf 应该被标记为受保护")
	}
	if user == nil || !user.UserSite {
		t.Error("用户订阅片段应该标记出来")
	}
}

func TestNginxSubDomain(t *testing.T) {
	lp, _ := setupPanel(t)
	_ = os.WriteFile(lp.NginxSubConf, []byte("server {\n\tlisten 8443 ssl http2;\n    server_name sub.example.com;\n}\n"), 0644)
	domain, port := nginxSubDomain()
	if domain != "sub.example.com" || port != "8443" {
		t.Fatalf("解析不对: %q %q", domain, port)
	}
	if got := userSubURL("nobody"); got != "" {
		t.Errorf("没有路径时不该拼出订阅地址: %s", got)
	}
	_ = os.MkdirAll(filepath.Join(lp.URLDir, "u1"), 0755)
	_ = os.WriteFile(filepath.Join(lp.URLDir, "u1", "u1-path"), []byte("/abc123"), 0644)
	if got := userSubURL("u1"); got != "https://sub.example.com:8443/abc123" {
		t.Errorf("订阅地址不对: %s", got)
	}
}
