package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// sing-box 的安装布局。全部集中在这里，命令行 -singbox-* 可以改。
//
// sing-box 用 `run -C <目录>` 加载整个配置目录：目录里每个 .json 按文件名排序后
// 依次合并，对象逐键合并、数组按顺序追加。fanout 只写自己的两个文件
// fanout-outbounds.json 和 fanout-route.json；别的脚本（比如 sb.sh）放在同一目录里的
// config.json / route.json / anytls-1.json 一律只读不写。入站也归别的脚本管，
// fanout 不建、不改、不删任何入站。
var singbox = struct {
	Bin     string // sing-box 可执行文件
	ConfDir string // -C 加载的配置目录
	Service string // systemd / OpenRC 服务名；存在时交给服务管理进程
	// 以下由 Bin 所在目录推出来
	URLDir string // 第三方脚本自己导出的分享链接 *.txt，只读
}{}

func init() { configureSingBox("/etc/sing-box/sing-box", "/etc/sing-box/conf", "sing-box") }

// configureSingBox 设置 sing-box 路径。空值保持默认。
func configureSingBox(bin, confDir, service string) {
	if bin != "" {
		singbox.Bin = bin
	}
	if confDir != "" {
		singbox.ConfDir = confDir
	}
	if service != "" {
		singbox.Service = service
	}
	singbox.URLDir = filepath.Join(filepath.Dir(singbox.Bin), "url")
}

const (
	fanoutOutboundsFile = "fanout-outbounds.json"
	fanoutRouteFile     = "fanout-route.json"
	// fanoutDirectTag 是兜底的直连出站。fanout 优先借用配置目录里已有的 direct 出站
	// （见 foreignConf.directTag），只有目录里一个 type=direct 的出站都没有时才自己建它。
	// 不用 "direct" 这个名字：免得别的脚本以后加上 direct 时重名，sing-box 会拒绝启动。
	fanoutDirectTag = "fanout-direct"
)

// fanoutManagedFiles 是 fanout 在配置目录里拥有、会重写和回滚的全部文件。
//
// 老版本 fanout 自建的入站文件 fanout-in-*.json 不在其中：新版不再建入站，
// 这些文件原样留在盘上，当作普通的已有入站只读显示，绝不删除。
var fanoutManagedFiles = []string{fanoutOutboundsFile, fanoutRouteFile}

// reservedPorts 是本机已知被占用、但未必正在监听的端口（warp socks、本地 API），
// 给出口随机分配 SOCKS5 端口时避开。
var reservedPorts = []int{40000, 9093, 9094}

func isManagedFile(name string) bool {
	for _, f := range fanoutManagedFiles {
		if f == name {
			return true
		}
	}
	return false
}

// ---- 配置生成（纯函数，便于测试） ----

// buildFanoutFiles 由分流规则、现有入站和当前隧道生成 fanout 拥有的两个文件。
//
//	fanout-outbounds.json {"outbounds":[...]}  每条连通隧道一个 socks 出站（目录里没有 direct 出站时才加 fanout-direct）
//	fanout-route.json     {"route":{"rules":[...],"rule_set":[...]}}
//
// 直连复用目录里已有的 direct 出站，不再自建。fanout-outbounds.json 的文件名排在
// outbounds.json 前面，合并后 fanout-exit-* 会排在第一个；目录里没人写 route.final 时
// sing-box 会把第一个出站当默认，所以这时 fanout-route.json 显式写 final，
// 值取"没有 fanout 时本来的默认出站"（别的文件里的第一个出站），整机默认走向不变。
// 别的文件写了 final 时 fanout 不写。
func buildFanoutFiles(rules []*RouteRule, existing []*adoptedInbound, tunnels []*Tunnel, opts buildOptions) map[string]any {
	live := map[string]*Tunnel{}
	for _, t := range tunnels {
		if t.Status == "up" {
			live[sanitizeTag(t.Node.HostName)] = t
		}
	}

	outs := []any{}
	opts.DirectTag = opts.Foreign.directTag()
	if opts.DirectTag == "" {
		opts.DirectTag = fanoutDirectTag
		outs = append(outs, map[string]any{"type": "direct", "tag": fanoutDirectTag})
	}
	hosts := make([]string, 0, len(live))
	for h := range live {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	liveTags := map[string]string{}
	for _, h := range hosts {
		outs = append(outs, socksOutbound(live[h]))
		liveTags[h] = tunnelTag(live[h])
	}

	have := map[string]bool{}
	if opts.InboundUsers == nil {
		opts.InboundUsers = map[string][]string{}
	}
	for _, a := range existing {
		opts.InboundUsers[a.Tag] = a.Users
		for _, u := range a.Users {
			have[u] = true
		}
	}
	routeRules, sets := buildRouteRules(rules, liveTags, have, opts)
	route := map[string]any{"rules": routeRules}
	if len(sets) > 0 {
		route["rule_set"] = sets
	}
	if !opts.Foreign.HasFinal {
		final := opts.Foreign.defaultOutbound()
		if final == "" {
			final = opts.DirectTag
		}
		route["final"] = final
	}
	return map[string]any{
		fanoutOutboundsFile: map[string]any{"outbounds": outs},
		fanoutRouteFile:     map[string]any{"route": route},
	}
}

// socksOutbound 把一条隧道变成指向 fanout 本地 SOCKS5 端口的 sing-box 出站。
func socksOutbound(t *Tunnel) map[string]any {
	out := map[string]any{
		"type":        "socks",
		"tag":         tunnelTag(t),
		"server":      "127.0.0.1",
		"server_port": t.Port,
		"version":     "5",
	}
	if c := t.credential(); c.User != "" {
		out["username"] = c.User
		out["password"] = c.Pass
	}
	return out
}

func toAnySlice(in []string) []any {
	out := make([]any, 0, len(in))
	for _, s := range in {
		out = append(out, s)
	}
	return out
}

func trimOutput(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 600 {
		s = s[:600] + "..."
	}
	return s
}

// ---- 写盘与回滚 ----

// snapshotFanoutFiles 读出 fanout 拥有的文件当前内容，用于回滚。
// 不存在的文件记为 nil，回滚时删掉。
func snapshotFanoutFiles(dir string) (map[string][]byte, error) {
	snap := map[string][]byte{}
	for _, name := range fanoutManagedFiles {
		blob, err := os.ReadFile(filepath.Join(dir, name))
		if os.IsNotExist(err) {
			snap[name] = nil
			continue
		}
		if err != nil {
			return nil, err
		}
		snap[name] = blob
	}
	return snap, nil
}

// writeFanoutFiles 把 files 写进目录。值为 nil 的文件删掉（回滚到"原来没有"）。
// 只碰 fanoutManagedFiles 里的文件名，别的一概不动。
// 临时文件不以 .json 结尾，sing-box 即使恰好此刻读目录也不会读到半截文件。
func writeFanoutFiles(dir string, files map[string][]byte) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	for name, blob := range files {
		if !isManagedFile(name) {
			return fmt.Errorf("拒绝写入不归 fanout 管的文件 %s", name)
		}
		path := filepath.Join(dir, name)
		if blob == nil {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
			continue
		}
		tmp := path + ".fanout-tmp"
		if err := os.WriteFile(tmp, blob, 0600); err != nil {
			return err
		}
		if err := os.Rename(tmp, path); err != nil {
			return err
		}
	}
	return nil
}

func encodeFiles(files map[string]any) (map[string][]byte, error) {
	out := make(map[string][]byte, len(files))
	for name, v := range files {
		blob, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return nil, err
		}
		out[name] = append(blob, '\n')
	}
	return out, nil
}

// ---- 进程管理 ----

// singboxRunner 负责让 sing-box 用上新配置。
//
// 本机有 sing-box 服务（systemd / OpenRC）时交给服务：先 check 再 reload，
// reload 失败退回 restart，fanout 绝不另起一个进程跟服务抢端口。
// 没有服务时 fanout 自己 `sing-box run -C <目录>` 拉一个子进程管着。
type singboxRunner struct {
	bin     string
	confDir string
	service string
	initSys string // "systemd" | "openrc" | ""
	workDir string
	cmd     *exec.Cmd
	logf    *os.File
	exited  chan struct{} // 自己托管的子进程退出时关闭

	verOnce sync.Once
	ver     [3]int // sing-box 版本号，取不到时全 0
}

func newSingboxRunner(workDir string) *singboxRunner {
	r := &singboxRunner{bin: singbox.Bin, confDir: singbox.ConfDir, service: singbox.Service, workDir: workDir}
	r.initSys = detectService(r.service)
	return r
}

// detectService 判断本机是否装了名为 name 的 sing-box 服务。
func detectService(name string) string {
	if name == "" || name == "none" {
		return ""
	}
	if _, err := exec.LookPath("systemctl"); err == nil {
		if err := cmdRun(exec.Command("systemctl", "cat", name+".service")); err == nil {
			return "systemd"
		}
	}
	if st, err := os.Stat("/etc/init.d/" + name); err == nil && !st.IsDir() {
		if _, err := exec.LookPath("rc-service"); err == nil {
			return "openrc"
		}
	}
	return ""
}

// version 返回 sing-box 的版本号（主、次、补丁），取不到时全 0。结果缓存。
func (r *singboxRunner) version() [3]int {
	r.verOnce.Do(func() {
		out, err := cmdCombined(exec.Command(r.bin, "version"))
		if err == nil {
			r.ver = parseSingboxVersion(string(out))
		}
	})
	return r.ver
}

// parseSingboxVersion 从 `sing-box version` 的输出里取版本号，形如 "sing-box version 1.14.1"。
func parseSingboxVersion(out string) [3]int {
	var v [3]int
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 3 && f[0] == "sing-box" && f[1] == "version" {
			parts := strings.SplitN(strings.TrimPrefix(f[2], "v"), ".", 3)
			for i, p := range parts {
				// 1.14.0-beta.1 这种，补丁号只取数字部分
				n := 0
				for _, c := range p {
					if c < '0' || c > '9' {
						break
					}
					n = n*10 + int(c-'0')
				}
				v[i] = n
			}
			break
		}
	}
	return v
}

// legacyRuleSet 判断 sing-box 是否早于 1.14（没有 http_client / initial_path）。
// 版本取不到时按新版处理：目标机都是 1.14+，写错了 check 也会拦住。
func (r *singboxRunner) legacyRuleSet() bool {
	v := r.version()
	if v == [3]int{} {
		return false
	}
	return v[0] == 1 && v[1] < 14
}

func (r *singboxRunner) describe() string {
	if r.initSys != "" {
		return fmt.Sprintf("%s 服务 %s，配置目录 %s", r.initSys, r.service, r.confDir)
	}
	return fmt.Sprintf("fanout 托管 %s run -C %s", r.bin, r.confDir)
}

// check 用 sing-box 自己的校验器检查整个目录（含别的脚本的文件）。
//
// 先校验再重载：配置写坏时服务会起不来，所有节点一起断，校验能挡在重载之前。
func (r *singboxRunner) check() error {
	out, err := cmdCombined(exec.Command(r.bin, "check", "-C", r.confDir))
	if err != nil {
		return fmt.Errorf("sing-box 配置校验失败: %s", trimOutput(out))
	}
	return nil
}

// reload 让 sing-box 用上目录里的新配置。
func (r *singboxRunner) reload(wantRunning bool) error {
	switch r.initSys {
	case "systemd":
		if err := cmdRun(exec.Command("systemctl", "reload", r.service)); err == nil {
			return nil
		}
		if out, err := cmdCombined(exec.Command("systemctl", "restart", r.service)); err != nil {
			return fmt.Errorf("重启 %s 服务失败: %s", r.service, trimOutput(out))
		}
		return nil
	case "openrc":
		if err := cmdRun(exec.Command("rc-service", r.service, "reload")); err == nil {
			return nil
		}
		if out, err := cmdCombined(exec.Command("rc-service", r.service, "restart")); err != nil {
			return fmt.Errorf("重启 %s 服务失败: %s", r.service, trimOutput(out))
		}
		return nil
	}
	if !wantRunning {
		r.stop()
		return nil
	}
	return r.restart()
}

func (r *singboxRunner) restart() error {
	r.stop()

	logPath := filepath.Join(r.workDir, "sing-box.log")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("打开 sing-box 日志失败: %w", err)
	}
	cmd := exec.Command(r.bin, "run", "-C", r.confDir)
	cmd.Stdout, cmd.Stderr = f, f
	// 必须从母机的网络命名空间拉起，否则入站端口可能只在某条隧道内部监听（见 netnsguard.go）
	if err := cmdStart(cmd); err != nil {
		f.Close()
		return fmt.Errorf("启动 sing-box 失败: %w", err)
	}
	r.cmd, r.logf = cmd, f
	done := make(chan struct{})
	r.exited = done
	go func() { _ = cmd.Wait(); close(done) }()
	r.writePID(cmd.Process.Pid)

	// 起得来但立刻退出的情况要能被发现，否则界面会显示成功而实际不通
	select {
	case <-done:
		return fmt.Errorf("sing-box 启动后立刻退出，详见 %s", logPath)
	case <-time.After(500 * time.Millisecond):
	}
	return nil
}

// verifyDelay 是重载后等多久再确认 sing-box 还活着。测试里调小。
var verifyDelay = 3 * time.Second

// verifyAlive 在重载之后确认 sing-box 还在跑。
//
// sing-box check 只做静态校验，有些错误要到启动时才暴露——典型的是远程规则集
// 下载失败（没有缓存、也没有 initial_path 时直接 FATAL 退出）。服务一退出，
// 同目录里别的脚本的节点也全断了，所以重载后要再确认一次，挂了就回滚。
func (r *singboxRunner) verifyAlive() error {
	time.Sleep(verifyDelay)
	switch r.initSys {
	case "systemd":
		out, _ := cmdOutput(exec.Command("systemctl", "is-active", r.service))
		if st := strings.TrimSpace(string(out)); st != "active" {
			return fmt.Errorf("sing-box 服务重载后状态是 %q（journalctl -u %s 看原因）", st, r.service)
		}
	case "openrc":
		if err := cmdRun(exec.Command("rc-service", r.service, "status")); err != nil {
			return fmt.Errorf("sing-box 服务重载后没在运行")
		}
	default:
		if r.exited == nil {
			return nil
		}
		select {
		case <-r.exited:
			return fmt.Errorf("sing-box 启动后退出了，详见 %s", filepath.Join(r.workDir, "sing-box.log"))
		default:
		}
	}
	return nil
}

// stop 只停 fanout 自己拉起的子进程；服务模式下什么都不做。
func (r *singboxRunner) stop() {
	if r.initSys != "" {
		return
	}
	if r.cmd != nil && r.cmd.Process != nil {
		_ = r.cmd.Process.Signal(syscall.SIGTERM)
		time.Sleep(200 * time.Millisecond)
		_ = r.cmd.Process.Kill()
		r.cmd = nil
		r.exited = nil
	}
	if r.logf != nil {
		r.logf.Close()
		r.logf = nil
	}
	_ = os.Remove(r.pidPath())
}

func (r *singboxRunner) pidPath() string { return filepath.Join(r.workDir, "sing-box.pid") }

func (r *singboxRunner) writePID(pid int) {
	_ = os.WriteFile(r.pidPath(), []byte(strconv.Itoa(pid)), 0600)
}

// reapOrphan 清掉上次 fanout 被 SIGKILL 时遗留的子进程。
// 按 pidfile 精确定位，并核对可执行文件确实是我们启动的那个，避免误杀。
func (r *singboxRunner) reapOrphan() {
	if r.initSys != "" {
		return
	}
	blob, err := os.ReadFile(r.pidPath())
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(blob)))
	if err == nil && pid > 1 {
		if exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid)); err == nil && exe == r.bin {
			if proc, err := os.FindProcess(pid); err == nil {
				_ = proc.Kill()
			}
		}
	}
	_ = os.Remove(r.pidPath())
}

// limiterFile 是 sb.sh 生成的限速配置，内部路由会复制 fanout 的分流规则。
const limiterFile = "00-limiter.json"

// warnShadowingRoutes 提醒排在 fanout-route.json 前面、且带路由规则的外部文件。
//
// sing-box 按文件名排序合并，rules 数组按顺序追加、先匹配先生效。
// 名字排在 "fanout-route.json" 前面的文件里如果有能匹配同一入站的规则，
// fanout 的分流规则会被它抢先。
func warnShadowingRoutes(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") || isManagedFile(name) || name >= fanoutRouteFile {
			continue
		}
		// sb.sh 的限速文件：限速用户进入限速出站后，会在里面按 fanout 的规则继续分流，不算抢先
		if name == limiterFile {
			continue
		}
		blob, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		var doc struct {
			Route struct {
				Rules []json.RawMessage `json:"rules"`
			} `json:"route"`
		}
		if json.Unmarshal(bytes.TrimSpace(blob), &doc) == nil && len(doc.Route.Rules) > 0 {
			log.Printf("注意: %s 排在 %s 前面且有 %d 条路由规则，可能先于 fanout 的分流规则生效",
				name, fanoutRouteFile, len(doc.Route.Rules))
		}
	}
}
