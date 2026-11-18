package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 面板启动顺序：口令 / 访问路径 / Web 设置必须在毫秒级就位。
// 这一组测试盯的就是「启动网页面板没有口令、没有路径」那个故障不再回来。
func TestInitPanelCoreWritesAuthFilesFast(t *testing.T) {
	dir := t.TempDir()

	start := time.Now()
	auth, cfg, pwCreated, bpCreated, err := initPanelCore(dir, 8899, false)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("initPanelCore 失败: %v", err)
	}
	if auth == nil {
		t.Fatal("没拿到 Auth")
	}
	if !pwCreated || !bpCreated {
		t.Errorf("首次运行应生成口令与访问路径: pw=%v bp=%v", pwCreated, bpCreated)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("初始化用了 %v，应该远快于这个数（不能碰网络）", elapsed)
	}

	pw, err := os.ReadFile(filepath.Join(dir, "password"))
	if err != nil || strings.TrimSpace(string(pw)) == "" {
		t.Fatalf("password 没写出来: %q %v", pw, err)
	}
	bp, err := os.ReadFile(filepath.Join(dir, "basepath"))
	if err != nil || strings.TrimSpace(string(bp)) == "" {
		t.Fatalf("basepath 没写出来: %q %v", bp, err)
	}
	if currentBasePath() == "" {
		t.Fatal("访问路径没有生效")
	}
	if _, err := os.Stat(filepath.Join(dir, "settings.json")); err != nil {
		t.Fatalf("settings.json 没写出来: %v", err)
	}
	if cfg.Port != 8899 {
		t.Errorf("端口应为 8899，实际 %d", cfg.Port)
	}

	// 第二次启动沿用已有口令与路径，不该重新生成
	_, _, pwCreated2, bpCreated2, err := initPanelCore(dir, 8899, false)
	if err != nil {
		t.Fatalf("二次 initPanelCore 失败: %v", err)
	}
	if pwCreated2 || bpCreated2 {
		t.Error("已存在口令/路径时不该重新生成")
	}
}

// 读源码断言启动顺序：面板先起来，拉节点/恢复隧道在后台 goroutine 里。
// 这是对故障本身的结构性保护——顺序被改回去时测试立刻挂。
func TestPanelsStartBeforeNetworkWork(t *testing.T) {
	blob, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("读 main.go 失败: %v", err)
	}
	src := string(blob)

	core := strings.Index(src, "initPanelCore(*workDir")
	refresh := strings.Index(src, "mgr.RefreshNodes()")
	restore := strings.Index(src, "mgr.restoreState()")
	serve := strings.Index(src, "srv.serve()")
	if core < 0 || refresh < 0 || restore < 0 || serve < 0 {
		t.Fatalf("main.go 里找不到关键调用: core=%d refresh=%d restore=%d serve=%d", core, refresh, restore, serve)
	}
	if !(core < refresh && core < restore) {
		t.Fatal("口令/路径/Web 设置必须在拉节点、恢复隧道之前初始化")
	}
	if !(refresh < serve && restore < serve) {
		t.Fatal("后台初始化应在启动 HTTP 监听之前排好")
	}

	// 拉节点与恢复必须在同一个后台 goroutine 里，不能阻塞主线程
	between := src[core:refresh]
	if !strings.Contains(between, "go func()") {
		t.Fatal("mgr.RefreshNodes 应该放在 go func() 里异步跑")
	}
	// 顶层出现直接的 `if n, err := mgr.RefreshNodes` 就说明又变成阻塞启动了
	if strings.Contains(src, "\n\tif n, err := mgr.RefreshNodes()") {
		t.Fatal("mgr.RefreshNodes 又被写成阻塞调用了")
	}
}

func TestPrepareHostAlreadyEnabled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ip_forward")
	if err := os.WriteFile(path, []byte("1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	oldPath, oldSysctl := ipForwardPath, runSysctl
	defer func() { ipForwardPath, runSysctl = oldPath, oldSysctl }()
	ipForwardPath = path
	called := false
	runSysctl = func(args ...string) error { called = true; return nil }

	if err := prepareHost(); err != nil {
		t.Fatalf("已是 1 时应直接返回 nil: %v", err)
	}
	if called {
		t.Error("已经是 1 就不该再调 sysctl")
	}
}

func TestPrepareHostWritesProcDirectly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ip_forward")
	if err := os.WriteFile(path, []byte("0\n"), 0644); err != nil {
		t.Fatal(err)
	}
	oldPath, oldSysctl := ipForwardPath, runSysctl
	defer func() { ipForwardPath, runSysctl = oldPath, oldSysctl }()
	ipForwardPath = path
	called := false
	runSysctl = func(args ...string) error { called = true; return nil }

	if err := prepareHost(); err != nil {
		t.Fatalf("直接写 /proc 成功时应返回 nil: %v", err)
	}
	blob, _ := os.ReadFile(path)
	if strings.TrimSpace(string(blob)) != "1" {
		t.Errorf("应该写进 1，实际 %q", blob)
	}
	if called {
		t.Error("直接写成功就不该再调 sysctl")
	}
}

func TestPrepareHostFallsBackToSysctl(t *testing.T) {
	oldPath, oldSysctl := ipForwardPath, runSysctl
	defer func() { ipForwardPath, runSysctl = oldPath, oldSysctl }()
	// 指向一个目录：读不到内容、写也必失败，模拟受限容器/只读 /proc/sys
	ipForwardPath = t.TempDir()
	var got []string
	runSysctl = func(args ...string) error { got = args; return nil }

	if err := prepareHost(); err != nil {
		t.Fatalf("回退 sysctl 成功时应返回 nil: %v", err)
	}
	if len(got) != 2 || got[0] != "-qw" || got[1] != "net.ipv4.ip_forward=1" {
		t.Errorf("sysctl 参数不对: %v", got)
	}
}

// 两样都失败（受限容器）也不能把整个面板拖死：只记日志、返回 nil。
func TestPrepareHostToleratesFailure(t *testing.T) {
	oldPath, oldSysctl := ipForwardPath, runSysctl
	defer func() { ipForwardPath, runSysctl = oldPath, oldSysctl }()
	ipForwardPath = t.TempDir()
	runSysctl = func(args ...string) error { return os.ErrPermission }

	if err := prepareHost(); err != nil {
		t.Fatalf("受限环境不应让启动失败: %v", err)
	}
}
