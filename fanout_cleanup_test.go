package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 临时隧道单元不再 . sb.sh，改为 fanout 自己带 -refresh-quick 参数刷新链接。
func TestQuickUnitUsesSelfRefresh(t *testing.T) {
	lp, _ := setupPanel(t)
	writeTestNode(t, lp, "vless-ws-1.json", vlessWSSample)
	n, _, err := loadNode("vless-ws-1.json")
	if err != nil {
		t.Fatal(err)
	}

	oldSelf := fanoutSelfPath
	defer func() { fanoutSelfPath = oldSelf }()
	fanoutSelfPath = func() string { return "/usr/local/bin/fanout" }

	if err := quickWriteUnit(n); err != nil {
		t.Fatalf("写单元失败: %v", err)
	}
	blob, _ := os.ReadFile(filepath.Join(lp.QuickUnitDir, "cf-quick-vless-ws-1.service"))
	text := string(blob)
	if !strings.Contains(text, "ExecStartPost=-/usr/local/bin/fanout -refresh-quick cf-quick-vless-ws-1") {
		t.Errorf("ExecStartPost 应调用 fanout 自己刷新链接，实际:\n%s", text)
	}
	if strings.Contains(text, "sb.sh") {
		t.Errorf("单元里不该再依赖 sb.sh:\n%s", text)
	}
}

func TestQuickRefreshUnitRejectsBadUnit(t *testing.T) {
	if code := quickRefreshUnit("vless-ws-1"); code != 2 {
		t.Errorf("非 cf-quick 单元应返回 2，实际 %d", code)
	}
	if code := quickRefreshUnit("cf-quick-../../etc/passwd"); code != 2 {
		t.Errorf("带斜杠的单元名应被拒，实际 %d", code)
	}
}

func TestQuickRefreshUnitTimeout(t *testing.T) {
	lp, _ := setupPanel(t)
	writeTestNode(t, lp, "vless-ws-1.json", vlessWSSample)
	oldTries := quickDomainWaitTries
	defer func() { quickDomainWaitTries = oldTries }()
	quickDomainWaitTries = 0 // 不等，直接判超时
	if code := quickRefreshUnit("cf-quick-vless-ws-1"); code != 1 {
		t.Errorf("等不到域名应返回 1，实际 %d", code)
	}
}

func TestQuickRefreshUnitRefreshesLink(t *testing.T) {
	lp, _ := setupPanel(t)
	writeTestNode(t, lp, "vless-ws-1.json", vlessWSSample)
	_ = os.WriteFile(filepath.Join(lp.URLDir, "vless-ws-1.txt"), []byte(""), 0644)
	_ = os.WriteFile(filepath.Join(lp.QuickDir, "cf-quick-vless-ws-1.log"),
		[]byte("INF Registered tunnel connection https://fresh-domain.trycloudflare.com\n"), 0644)

	if code := quickRefreshUnit("cf-quick-vless-ws-1"); code != 0 {
		t.Fatalf("刷新应成功，返回 %d", code)
	}
	blob, err := os.ReadFile(filepath.Join(lp.URLDir, "vless-ws-1.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(blob), "fresh-domain.trycloudflare.com") {
		t.Fatalf("链接里应该带上日志里的域名: %s", blob)
	}
}

// 升级路径顺手清掉历史残留的 /usr/local/bin/f。
func TestRemoveLegacyMenuScript(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	if err := os.WriteFile(path, []byte("#!/bin/bash\n"), 0755); err != nil {
		t.Fatal(err)
	}
	old := legacyMenuPath
	defer func() { legacyMenuPath = old }()
	legacyMenuPath = path

	removeLegacyMenuScript()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("旧 f 快捷命令应该被删掉: %v", err)
	}
	// 本来就没有时也不能出错
	removeLegacyMenuScript()
}

// 仓库只留代码：没有 f.sh / README，也没有推广外链与介绍文案。
func TestRepoHasNoLegacyMenuOrPromo(t *testing.T) {
	for _, gone := range []string{"f.sh", "README.md", filepath.Join("worker", "README.md")} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("%s 应该已经从仓库删掉", gone)
		}
	}

	forbidden := []string{
		"t.me/", "youtube.com/@joeyblog", "joeyblog", "交流群", "油管", "博客",
	}
	for _, f := range []string{"install.sh", "web.go", "auth.go", filepath.Join("web", "app.js"), filepath.Join("web", "admin.js")} {
		blob, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("读 %s 失败: %v", f, err)
		}
		text := string(blob)
		for _, bad := range forbidden {
			if strings.Contains(text, bad) {
				t.Errorf("%s 里还有推广/介绍内容 %q", f, bad)
			}
		}
		if strings.Contains(text, "f.sh") {
			t.Errorf("%s 还在引用 f.sh", f)
		}
	}

	// install.sh 只允许清理旧的 f，不允许再安装它
	inst, _ := os.ReadFile("install.sh")
	if !strings.Contains(string(inst), "rm -f /usr/local/bin/f") {
		t.Error("install.sh 应该顺手清理旧的 /usr/local/bin/f")
	}

	// 升级逻辑不再解包 f.sh / README.md
	upd, _ := os.ReadFile("update.go")
	for _, bad := range []string{"f.sh", "README.md", "updateMenuScript"} {
		if strings.Contains(string(upd), bad) {
			t.Errorf("update.go 里不该再有 %q", bad)
		}
	}
	// 临时隧道不再依赖仓库里的 sb.sh
	q, _ := os.ReadFile("tunnel_quick.go")
	if strings.Contains(string(q), "SB_LOAD_ONLY") || strings.Contains(string(q), "qt_refresh_url \"") {
		t.Error("tunnel_quick.go 里不该再调用 sb.sh 的 qt_refresh_url")
	}
}
