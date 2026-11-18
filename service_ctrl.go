package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// 系统状态与 sing-box / nginx 服务控制。分支与脚本一致：systemd 优先，OpenRC 兜底。

// serviceState 是一个服务在界面上的样子。
type serviceState struct {
	Name      string `json:"name"`
	Label     string `json:"label"`
	Installed bool   `json:"installed"`
	Active    bool   `json:"active"`
	Enabled   bool   `json:"enabled"`
	Init      string `json:"init"`
	Uptime    string `json:"uptime"`
	PID       string `json:"pid"`
}

// serviceStates 汇总 sing-box / nginx / fanout 自己的状态。
func serviceStates() []serviceState {
	return []serviceState{
		probeService(singbox.Service, "sing-box"),
		probeService("nginx", "nginx"),
		probeService("fanout", "fanout 面板"),
	}
}

// probeService 查一个服务的运行状态。
func probeService(unit, label string) serviceState {
	st := serviceState{Name: unit, Label: label}
	if unit == "" || unit == "none" {
		st.Label = label + "（fanout 托管）"
		st.Installed = true
		st.Active = singboxHostedByFanout()
		return st
	}
	init := detectService(unit)
	st.Init = init
	switch init {
	case "systemd":
		st.Installed = true
		if out, err := adminExec("systemctl", "is-active", unit); err == nil {
			st.Active = strings.TrimSpace(out) == "active"
		}
		if out, err := adminExec("systemctl", "is-enabled", unit); err == nil {
			st.Enabled = strings.TrimSpace(out) == "enabled"
		}
		if st.Active {
			if out, err := adminExec("systemctl", "show", "-p", "ExecMainStartTimestamp", "--value", unit); err == nil {
				st.Uptime = uptimeSince(strings.TrimSpace(out))
			}
			if out, err := adminExec("systemctl", "show", "-p", "MainPID", "--value", unit); err == nil {
				st.PID = strings.TrimSpace(out)
			}
		}
	case "openrc":
		st.Installed = true
		if _, err := adminExec("rc-service", unit, "status"); err == nil {
			st.Active = true
		}
		if out, err := adminExec("rc-update", "show", "default"); err == nil {
			st.Enabled = strings.Contains(out, unit)
		}
	}
	return st
}

// singboxHostedByFanout 判断 fanout 是不是自己拉着 sing-box 进程。
func singboxHostedByFanout() bool {
	blob, err := os.ReadFile(filepath.Join(panelPaths().FanoutDir, "sing-box.pid"))
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(blob)))
	if err != nil || pid <= 1 {
		return false
	}
	// 用 0 号信号探活：进程在就返回 nil，不在就 ESRCH
	return syscall.Kill(pid, 0) == nil
}

// uptimeSince 把 systemctl 的启动时间换成「x天x小时x分」。
func uptimeSince(timestamp string) string {
	const layout = "Mon 2006-01-02 15:04:05 MST"
	t, err := time.Parse(layout, timestamp)
	if err != nil {
		// 有些 systemd 输出是 "2006-01-02 15:04:05 MST"
		t, err = time.Parse("2006-01-02 15:04:05 MST", timestamp)
	}
	if err != nil || t.IsZero() {
		return ""
	}
	d := time.Since(t)
	if d < 0 {
		return ""
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	if days > 0 {
		return fmt.Sprintf("%d天 %02d小时 %02d分", days, hours, mins)
	}
	return fmt.Sprintf("%02d小时 %02d分", hours, mins)
}

// controlService 执行 start / stop / restart / reload。
func controlService(unit, action string) error {
	switch action {
	case "start", "stop", "restart", "reload":
	default:
		return fmt.Errorf("不支持的操作 %q", action)
	}
	if unit == "" || unit == "none" {
		return fmt.Errorf("这个服务由 fanout 自己托管，去「设置」里重启 fanout")
	}
	init := detectService(unit)
	var out string
	var err error
	switch init {
	case "systemd":
		out, err = adminExec("systemctl", action, unit)
	case "openrc":
		out, err = adminExec("rc-service", unit, action)
	default:
		return fmt.Errorf("没有找到 %s 的服务", unit)
	}
	if err != nil && action == "reload" {
		if init == "systemd" {
			out, err = adminExec("systemctl", "restart", unit)
		} else {
			out, err = adminExec("rc-service", unit, "restart")
		}
	}
	if err != nil {
		return fmt.Errorf("%s %s 失败：%s", unit, action, trimOut(out))
	}
	return nil
}

// serviceLogs 取一个服务最近的日志。
func serviceLogs(unit string, lines int) string {
	if lines <= 0 || lines > 500 {
		lines = 120
	}
	n := strconv.Itoa(lines)
	lp := panelPaths()
	// fanout 自己托管的 sing-box 写在工作目录下的日志文件里
	if unit == "sing-box" && detectService(singbox.Service) == "" {
		path := filepath.Join(lp.FanoutDir, "sing-box.log")
		if blob, err := os.ReadFile(path); err == nil {
			return tailLines(string(blob), lines)
		}
	}
	if detectService(unit) == "systemd" {
		cmd := exec.Command("journalctl", "-u", unit, "-n", n, "--no-pager", "--output=short")
		out, err := cmd.CombinedOutput()
		if err == nil {
			return trimOut(string(out))
		}
	}
	switch unit {
	case "nginx":
		return nginxLogs(lines)
	}
	// OpenRC 或没有 journal 的系统，退回日志文件
	for _, path := range []string{
		"/var/log/" + unit + ".log",
		filepath.Join(lp.FanoutDir, unit+".log"),
	} {
		if blob, err := os.ReadFile(path); err == nil {
			return tailLines(string(blob), lines)
		}
	}
	return "没有找到日志（" + unit + "）"
}

// tailLines 取文本最后 n 行。
func tailLines(text string, n int) string {
	parts := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(parts) > n {
		parts = parts[len(parts)-n:]
	}
	return trimOut(strings.Join(parts, "\n"))
}

// ---- 防火墙 ----

// firewallInfo 是「端口放行」面板的数据。
type firewallInfo struct {
	Available bool           `json:"available"`
	Policy    string         `json:"policy"`
	Inbounds  []firewallPort `json:"inbounds"`
}

type firewallPort struct {
	File string   `json:"file"`
	Kind string   `json:"kind"`
	Tag  string   `json:"tag"`
	Port int      `json:"port"`
	TCP  bool     `json:"tcp"`
	UDP  bool     `json:"udp"`
	Need []string `json:"need"`
}

// firewallSnapshot 列出每个入站端口要用什么协议、放行了没有。
func firewallSnapshot() firewallInfo {
	info := firewallInfo{Available: haveCmd("nft"), Policy: firewallPolicy()}
	nodes, _ := listNodes()
	for _, n := range nodes {
		need := []string{"tcp"}
		switch n.Kind {
		case "hysteria2", "tuic":
			need = []string{"udp"}
		case "xhttp-udp-tls", "naiveproxy":
			need = []string{"tcp", "udp"}
		}
		info.Inbounds = append(info.Inbounds, firewallPort{
			File: n.File, Kind: n.Kind, Tag: n.Tag, Port: n.Port,
			TCP: n.PortOK["tcp"], UDP: n.PortOK["udp"], Need: need,
		})
	}
	return info
}

// syncFirewall 给所有入站补齐端口放行（网页上一键「全部放行」）。
func syncFirewall() (int, error) {
	nodes, err := listNodes()
	if err != nil {
		return 0, err
	}
	done := 0
	var firstErr error
	for _, n := range nodes {
		if n.Port <= 0 {
			continue
		}
		need := []string{"tcp"}
		switch n.Kind {
		case "hysteria2", "tuic":
			need = []string{"udp"}
		case "xhttp-udp-tls", "naiveproxy":
			need = []string{"tcp", "udp"}
		}
		for _, proto := range need {
			if err := allowPort(n.Port, proto); err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			done++
		}
	}
	return done, firstErr
}

// ---- 出站（含 WARP） ----

// outboundOption 是分流规则可以选的出口。
type outboundOption struct {
	Tag  string `json:"tag"`
	Type string `json:"type"`
	Note string `json:"note"`
}

// foreignOutbounds 列出配置目录里别的文件定义的出站，WARP（wireguard-out）排前面。
func foreignOutbounds() []outboundOption {
	fc := scanForeign(panelPaths().ConfDir)
	var out []outboundOption
	for _, o := range fc.Outbounds {
		note := ""
		if o.Tag == "wireguard-out" {
			note = "WARP 换出口"
		}
		out = append(out, outboundOption{Tag: o.Tag, Type: o.Type, Note: note})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].Tag == "wireguard-out") != (out[j].Tag == "wireguard-out") {
			return out[i].Tag == "wireguard-out"
		}
		return out[i].Tag < out[j].Tag
	})
	return out
}

// systemOverview 是「系统」页顶部的信息卡。
type systemOverview struct {
	Version    string `json:"version"`
	SingboxVer string `json:"singbox_version"`
	PublicIP   string `json:"public_ip"`
	Uptime     string `json:"uptime"`
	Hostname   string `json:"hostname"`
	OS         string `json:"os"`
	ConfCount  int    `json:"conf_count"`
	NodeCount  int    `json:"node_count"`
	UserCount  int    `json:"user_count"`
	Firewall   string `json:"firewall"`
	WorkDir    string `json:"work_dir"`
	SubDomain  string `json:"sub_domain"`
}

func systemOverviewInfo() systemOverview {
	lp := panelPaths()
	ov := systemOverview{Version: version, PublicIP: hostPublicIP(), WorkDir: lp.FanoutDir}
	if host, err := os.Hostname(); err == nil {
		ov.Hostname = host
	}
	if out, err := adminExec(lp.Bin, "version"); err == nil {
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(line, "sing-box version") {
				ov.SingboxVer = strings.TrimSpace(strings.TrimPrefix(line, "sing-box version"))
				break
			}
		}
	}
	if entries, err := os.ReadDir(lp.ConfDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
				ov.ConfCount++
			}
		}
	}
	nodes, _ := listNodes()
	ov.NodeCount = len(nodes)
	if entries, err := os.ReadDir(lp.URLDir); err == nil {
		for _, e := range entries {
			if e.IsDir() && pathExists(filepath.Join(lp.URLDir, e.Name(), e.Name()+"-uuid")) {
				ov.UserCount++
			}
		}
	}
	ov.Firewall = firewallPolicy()
	domain, _ := nginxSubDomain()
	ov.SubDomain = domain
	return ov
}
