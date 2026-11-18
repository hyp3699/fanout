package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Cloudflare 官方临时隧道（trycloudflare）与固定隧道节点（CDN / Tunnel 链接）。
//
// 临时隧道的单元文件、日志、域名提取方式与 sing-box-08.sh 完全一致：
//   unit  cf-quick-{类型}-{编号}.service
//   log   /etc/sing-box/quick/cf-quick-{类型}-{编号}.log
//   域名  grep https://xxx.trycloudflare.com
// 这样脚本的菜单、守护定时器、网页三方看到的是同一份东西。

// cdnPreferredHost 是脚本里出口用的 Cloudflare 优选域名（CFIP）。
var cdnPreferredHost = "cf.877774.xyz"

// quickUnitName 与脚本 qt_name 一致。
func quickUnitName(kind string, number int) string {
	return fmt.Sprintf("cf-quick-%s-%d", kind, number)
}

// quickUnitExists 单元文件在不在。
func quickUnitExists(unit string) bool {
	if unit == "" || strings.Contains(unit, "/") {
		return false
	}
	return pathExists(filepath.Join(panelPaths().QuickUnitDir, unit+".service"))
}

// quickTunnelState 返回 (域名, 是否在跑, 单元是否存在)。
func quickTunnelState(unit string) (string, bool, bool) {
	if unit == "" {
		return "", false, false
	}
	lp := panelPaths()
	exists := quickUnitExists(unit)
	domain := quickDomainFromLog(filepath.Join(lp.QuickDir, unit+".log"))
	active := false
	if exists && detectService(unit) == "systemd" {
		if out, err := adminExec("systemctl", "is-active", unit); err == nil {
			active = strings.TrimSpace(out) == "active"
		}
	}
	return domain, active, exists
}

var reTrycloudflare = regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)

// quickDomainFromLog 从 cloudflared 日志里取最后一次分配的域名。
func quickDomainFromLog(path string) string {
	blob, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	all := reTrycloudflare.FindAllString(string(blob), -1)
	if len(all) == 0 {
		return ""
	}
	return strings.TrimPrefix(all[len(all)-1], "https://")
}

// quickInstallCloudflared 下载 cloudflared（脚本 qt_install_cloudflared 的 Go 版）。
func quickInstallCloudflared() error {
	lp := panelPaths()
	if pathExists(lp.QuickBin) {
		if _, err := adminExec(lp.QuickBin, "--version"); err == nil {
			return nil
		}
	}
	arch := ""
	switch machineArch() {
	case "x86_64", "amd64":
		arch = "amd64"
	case "aarch64", "arm64":
		arch = "arm64"
	case "armv7l", "armv7":
		arch = "arm"
	case "i386", "i686":
		arch = "386"
	default:
		return fmt.Errorf("不支持的架构：%s", machineArch())
	}
	url := "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-" + arch
	tmp := lp.QuickBin + ".tmp"
	if out, err := adminExec("curl", "-fSL", "--connect-timeout", "10", "--max-time", "180", "-o", tmp, url); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("cloudflared 下载失败：%s", trimOut(out))
	}
	if err := os.Chmod(tmp, 0755); err != nil {
		return err
	}
	if err := os.Rename(tmp, lp.QuickBin); err != nil {
		return err
	}
	if out, err := adminExec(lp.QuickBin, "--version"); err != nil {
		return fmt.Errorf("cloudflared 无法运行：%s", trimOut(out))
	}
	return nil
}

func machineArch() string {
	out, err := adminExec("uname", "-m")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// quickWriteUnit 按脚本模板写 systemd 单元。
func quickWriteUnit(n *node) error {
	if n.Port <= 0 {
		return fmt.Errorf("读不到入站端口")
	}
	lp := panelPaths()
	if err := os.MkdirAll(lp.QuickDir, 0755); err != nil {
		return err
	}
	scheme, extra := "http", ""
	if n.TLS {
		scheme, extra = "https", " --no-tls-verify"
	}
	name := quickUnitName(n.Kind, n.Number)
	unit := fmt.Sprintf(`[Unit]
Description=Cloudflare Quick Tunnel for %s-%d
After=network-online.target sing-box.service
Wants=network-online.target

[Service]
Type=simple
Environment=HOME=%s
ExecStartPre=/bin/sh -c ': > %s/%s.log'
ExecStart=%s tunnel --no-autoupdate --edge-ip-version auto --protocol http2 --logfile %s/%s.log --url %s://localhost:%d%s
ExecStartPost=-/bin/bash -c 'SB_LOAD_ONLY=1 . %s/sb.sh >/dev/null 2>&1; qt_refresh_url "%s" "%s" "%d" >/dev/null 2>&1'
TimeoutStartSec=90
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
`, n.Kind, n.Number, lp.QuickDir, lp.QuickDir, name,
		lp.QuickBin, lp.QuickDir, name, scheme, n.Port, extra,
		filepath.Dir(lp.Bin), filepath.Join(lp.ConfDir, n.File), n.Kind, n.Number)
	path := filepath.Join(lp.QuickUnitDir, name+".service")
	if err := writeFileAtomic(path, []byte(unit), 0644); err != nil {
		return err
	}
	_, _ = adminExec("systemctl", "daemon-reload")
	return nil
}

// quickSyncWatchdog 有启用的隧道就开守护定时器，全部停掉就关（脚本 qt_sync_watchdog）。
func quickSyncWatchdog() {
	lp := panelPaths()
	entries, err := os.ReadDir(lp.QuickUnitDir)
	if err != nil {
		return
	}
	any := false
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "cf-quick-") || !strings.HasSuffix(name, ".service") ||
			strings.Contains(name, "watchdog") {
			continue
		}
		unit := strings.TrimSuffix(name, ".service")
		if out, err := adminExec("systemctl", "is-enabled", unit); err == nil &&
			strings.TrimSpace(out) == "enabled" {
			any = true
			break
		}
	}
	if any {
		if !pathExists(filepath.Join(lp.QuickUnitDir, "cf-quick-watchdog.timer")) {
			quickInstallWatchdog()
		}
		_, _ = adminExec("systemctl", "enable", "--now", "cf-quick-watchdog.timer")
	} else {
		_, _ = adminExec("systemctl", "disable", "--now", "cf-quick-watchdog.timer")
	}
}

// quickInstallWatchdog 装守护脚本与定时器（与脚本 qt_install_watchdog 同样的文件）。
func quickInstallWatchdog() {
	lp := panelPaths()
	_ = os.MkdirAll(lp.QuickDir, 0755)
	script := fmt.Sprintf(`#!/bin/bash
# CF 临时隧道守护：只检查"已启用"的隧道，手动停止的不会被拉起
QT_DIR="%s"
for unit in /etc/systemd/system/cf-quick-*.service; do
    [ -f "$unit" ] || continue
    name=$(basename "$unit" .service)
    [ "$name" = "cf-quick-watchdog" ] && continue
    systemctl is-enabled --quiet "$name" 2>/dev/null || continue
    fail_file="${QT_DIR}/${name}.fail"
    if ! systemctl is-active --quiet "$name"; then
        echo "$(date '+%%F %%T') ${name} 未运行，重启" >> "${QT_DIR}/watchdog.log"
        systemctl restart --no-block "$name"; rm -f "$fail_file"; continue
    fi
    domain=$(grep -oE 'https://[a-z0-9-]+\.trycloudflare\.com' "${QT_DIR}/${name}.log" 2>/dev/null | tail -n1 | sed 's#https://##')
    if [ -z "$domain" ]; then
        since=$(systemctl show -p ActiveEnterTimestamp --value "$name")
        start_ts=$(date -d "$since" +%%s 2>/dev/null || echo 0)
        if [ $(( $(date +%%s) - start_ts )) -gt 90 ]; then
            echo "$(date '+%%F %%T') ${name} 无域名，重启" >> "${QT_DIR}/watchdog.log"
            systemctl restart --no-block "$name"; rm -f "$fail_file"
        fi
        continue
    fi
    code=$(curl -s -o /dev/null -m 10 -w '%%{http_code}' "https://${domain}/" 2>/dev/null)
    case "$code" in
        000|502|530|1033)
            n=$(( $(cat "$fail_file" 2>/dev/null || echo 0) + 1 ))
            echo "$n" > "$fail_file"
            if [ "$n" -ge 2 ]; then
                echo "$(date '+%%F %%T') ${name} 域名 ${domain} 失效(${code})，重启" >> "${QT_DIR}/watchdog.log"
                systemctl restart --no-block "$name"; rm -f "$fail_file"
            fi ;;
        *) rm -f "$fail_file" ;;
    esac
done
[ -f "${QT_DIR}/watchdog.log" ] && tail -n 200 "${QT_DIR}/watchdog.log" > "${QT_DIR}/watchdog.log.tmp" && mv -f "${QT_DIR}/watchdog.log.tmp" "${QT_DIR}/watchdog.log"
`, lp.QuickDir)
	_ = writeFileAtomic(filepath.Join(lp.QuickDir, "watchdog.sh"), []byte(script), 0755)
	unit := fmt.Sprintf("[Unit]\nDescription=Cloudflare Quick Tunnel Watchdog\n\n[Service]\nType=oneshot\nExecStart=%s/watchdog.sh\n",
		lp.QuickDir)
	_ = writeFileAtomic(filepath.Join(lp.QuickUnitDir, "cf-quick-watchdog.service"), []byte(unit), 0644)
	timer := `[Unit]
Description=Cloudflare Quick Tunnel Watchdog Timer

[Timer]
OnBootSec=3min
OnUnitActiveSec=2min
AccuracySec=30s

[Install]
WantedBy=timers.target
`
	_ = writeFileAtomic(filepath.Join(lp.QuickUnitDir, "cf-quick-watchdog.timer"), []byte(timer), 0644)
	_, _ = adminExec("systemctl", "daemon-reload")
}

// quickStart 启动（或重启）临时隧道，等 Cloudflare 分配域名后把链接更新。
func quickStart(file string) (*node, error) {
	n, _, err := loadNode(file)
	if err != nil {
		return nil, err
	}
	switch n.Kind {
	case "vless-ws", "vmess-ws", "trojan-ws", "vless-xhttp":
	default:
		return nil, fmt.Errorf("%s 类型的入站不支持临时隧道（只支持 WS / XHTTP 类）", n.Kind)
	}
	if st, err := os.Stat("/run/systemd/system"); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("临时隧道目前只支持 systemd 系统")
	}
	if err := quickInstallCloudflared(); err != nil {
		return nil, err
	}
	if err := quickWriteUnit(n); err != nil {
		return nil, err
	}
	unit := quickUnitName(n.Kind, n.Number)
	_, _ = adminExec("systemctl", "enable", unit)
	if out, err := adminExec("systemctl", "restart", unit); err != nil {
		return nil, fmt.Errorf("启动临时隧道失败：%s", trimOut(out))
	}
	quickSyncWatchdog()

	lp := panelPaths()
	domain := ""
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		domain = quickDomainFromLog(filepath.Join(lp.QuickDir, unit+".log"))
		if domain != "" {
			break
		}
		time.Sleep(time.Second)
	}
	if domain == "" {
		return nil, fmt.Errorf("45 秒内没拿到临时隧道域名，日志：%s", filepath.Join(lp.QuickDir, unit+".log"))
	}
	if err := quickRefreshLink(n, domain); err != nil {
		return nil, err
	}
	n.QuickDomain, n.QuickOn = domain, true
	return n, nil
}

// quickStop 停止隧道：进程退出、取消自启，链接从订阅里暂时拿掉。
func quickStop(file string) error {
	n, _, err := loadNode(file)
	if err != nil {
		return err
	}
	unit := quickUnitName(n.Kind, n.Number)
	_, _ = adminExec("systemctl", "disable", "--now", unit)
	lp := panelPaths()
	_ = os.Remove(filepath.Join(lp.QuickDir, unit+".fail"))
	if err := stripQuickLinks(n); err != nil {
		return err
	}
	_ = rebuildSubFile()
	_ = refreshUserSubs()
	quickSyncWatchdog()
	return nil
}

// quickRemove 彻底删掉隧道单元（删入站 / 切协议时用）。
func quickRemove(kind string, number int) {
	unit := quickUnitName(kind, number)
	lp := panelPaths()
	_, _ = adminExec("systemctl", "disable", "--now", unit)
	_ = os.Remove(filepath.Join(lp.QuickUnitDir, unit+".service"))
	_ = os.Remove(filepath.Join(lp.QuickDir, unit+".log"))
	_ = os.Remove(filepath.Join(lp.QuickDir, unit+".fail"))
	_ = os.Remove(filepath.Join(lp.QuickDir, fmt.Sprintf("%s-%d.dedicated", kind, number)))
	_, _ = adminExec("systemctl", "daemon-reload")
}

// quickRefreshLink 按当前临时域名重写节点链接（脚本 qt_refresh_url 的 Go 版）。
func quickRefreshLink(n *node, domain string) error {
	if domain == "" {
		return fmt.Errorf("没有隧道域名")
	}
	uuid, password := "", ""
	if len(n.Users) > 0 {
		uuid, password = n.Users[0].UUID, n.Users[0].Password
	}
	path := n.Path
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	remark := nodeRemarkEmoji + "临时隧道"
	if !isQuickDedicated(n) {
		remark = fmt.Sprintf("%s临时隧道-%s-%d", nodeRemarkEmoji, n.Kind, n.Number)
	}
	eq := urlEscape(remark)
	host := cdnPreferredHost

	var link string
	switch n.Kind {
	case "vless-ws":
		link = fmt.Sprintf("vless://%s@%s:443?ed=2048&eh=Sec-WebSocket-Protocol&encryption=none&security=tls&sni=%s&type=ws&host=%s&path=%s?ed=2048#%s",
			uuid, host, domain, domain, path, eq)
	case "vmess-ws":
		vm := map[string]any{
			"v": "2", "ps": remark, "add": host, "port": "443", "id": uuid,
			"aid": "0", "encryption": "auto", "net": "ws", "type": "auto",
			"host": domain, "path": path + "?ed=2048", "tls": "tls", "sni": domain,
			"alpn": "", "fp": "firefox", "allowInsecure": false,
		}
		link = vmessLink(vm)
	case "trojan-ws":
		link = fmt.Sprintf("trojan://%s@%s:443?ed=2048&eh=Sec-WebSocket-Protocol&security=tls&sni=%s&type=ws&host=%s&path=%s?ed=2048#%s",
			password, host, domain, domain, path, eq)
	case "vless-xhttp":
		link = fmt.Sprintf("vless://%s@%s:443?encryption=none&security=tls&sni=%s&type=xhttp&host=%s&path=%s#%s",
			uuid, host, domain, domain, path, eq)
	default:
		return fmt.Errorf("%s 不支持临时隧道链接", n.Kind)
	}

	urlFile := nodeURLFile(n)
	_ = stripQuickLinks(n)
	blob, _ := os.ReadFile(urlFile)
	body := strings.TrimRight(string(blob), "\n")
	if body != "" {
		body += "\n"
	}
	body += link + "\n"
	if err := writeFileAtomic(urlFile, []byte(body), 0644); err != nil {
		return err
	}
	_ = rebuildSubFile()
	_ = refreshUserSubs()
	return nil
}

// isQuickDedicated 判断是不是「添加临时隧道」专门建的入站。
func isQuickDedicated(n *node) bool {
	lp := panelPaths()
	if pathExists(filepath.Join(lp.QuickDir, fmt.Sprintf("%s-%d.dedicated", n.Kind, n.Number))) {
		return true
	}
	return n.Listen == "127.0.0.1" && quickUnitExists(quickUnitName(n.Kind, n.Number))
}

// stripQuickLinks 从链接文件里删掉临时隧道链接（按备注识别）。
func stripQuickLinks(n *node) error {
	urlFile := nodeURLFile(n)
	blob, err := os.ReadFile(urlFile)
	if err != nil {
		return nil
	}
	var kept []string
	for _, line := range strings.Split(string(blob), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if isQuickLink(line) {
			continue
		}
		kept = append(kept, line)
	}
	return writeFileAtomic(urlFile, []byte(strings.Join(kept, "\n")+"\n"), 0644)
}

// isQuickLink 备注里含「临时隧道」或旧版 QuickTunnel- 的链接。
func isQuickLink(line string) bool {
	remark := ""
	if strings.HasPrefix(line, "vmess://") {
		if vm, ok := decodeVmess(line); ok {
			remark, _ = vm["ps"].(string)
		}
	} else if i := strings.Index(line, "#"); i >= 0 {
		remark = line[i+1:]
		if unescaped, err := unescapePath(remark); err == nil {
			remark = unescaped
		}
	}
	return strings.Contains(remark, "临时隧道") || strings.Contains(remark, "QuickTunnel-")
}

// unescapePath 是 url.QueryUnescape 的薄封装（避免各处都 import net/url）。
func unescapePath(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			v, err := strconv.ParseUint(s[i+1:i+3], 16, 8)
			if err == nil {
				b.WriteByte(byte(v))
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String(), nil
}

// quickResetCreds 重置临时隧道入站的 UUID 与 WS 路径（脚本 qt_reset_credentials）。
func quickResetCreds(file string) (string, string, error) {
	n, doc, err := loadNode(file)
	if err != nil {
		return "", "", err
	}
	ib := firstInbound(doc)
	if ib == nil {
		return "", "", fmt.Errorf("入站配置为空")
	}
	users := inboundUserMaps(ib)
	if len(users) == 0 {
		return "", "", fmt.Errorf("入站里没有用户")
	}
	newUUID := newUUID()
	suffix := "-ws"
	if n.Kind == "vless-xhttp" {
		suffix = "-xhttp"
	}
	newPath := "/" + randomHex(6) + suffix
	if users[0]["uuid"] != nil {
		users[0]["uuid"] = newUUID
	} else {
		users[0]["password"] = newUUID
	}
	tr := obj(ib["transport"])
	if tr == nil {
		tr = map[string]any{"type": "ws", "max_early_data": 2048, "early_data_header_name": "Sec-WebSocket-Protocol"}
		ib["transport"] = tr
	}
	tr["path"] = newPath
	if err := saveNodeDoc(file, doc); err != nil {
		return "", "", err
	}
	_ = applySingboxReload()
	if unit := quickUnitName(n.Kind, n.Number); quickUnitExists(unit) {
		if n.QuickOn {
			if dom := quickDomainFromLog(filepath.Join(panelPaths().QuickDir, unit+".log")); dom != "" {
				if fresh, _, err := loadNode(file); err == nil {
					_ = quickRefreshLink(fresh, dom)
				}
			}
		}
	}
	return newUUID, newPath, nil
}

// quickSwitchProtocol 在 VMess WS / VLESS WS 之间切换（脚本 qt_switch_protocol）。
//
// 只往 WebSocket 切：CF 临时隧道会缓冲 XHTTP 的流式响应，切到 XHTTP 就不通了
// （931981a 把这条写进了脚本），已经切成 XHTTP 的入站可以切回 WS。
func quickSwitchProtocol(file, target string) (*node, error) {
	n, doc, err := loadNode(file)
	if err != nil {
		return nil, err
	}
	switch n.Kind {
	case "vmess-ws", "vless-ws", "vless-xhttp":
	default:
		return nil, fmt.Errorf("只有 VMess WS / VLESS WS 临时隧道能切换协议")
	}
	if !isQuickDedicated(n) {
		return nil, fmt.Errorf("这不是临时隧道专用入站")
	}
	if target == "" {
		if n.Kind == "vless-ws" {
			target = "vmess-ws"
		} else {
			target = "vless-ws"
		}
	}
	var newProto string
	switch target {
	case "vmess-ws":
		newProto = "vmess"
	case "vless-ws":
		newProto = "vless"
	default:
		return nil, fmt.Errorf("只能切到 VMess WS 或 VLESS WS（XHTTP 过 trycloudflare 不通）")
	}
	if target == n.Kind {
		return nil, fmt.Errorf("已经是 %s 了", target)
	}
	number := nextNodeNumber(target)
	if number == 0 {
		return nil, fmt.Errorf("编号用完了")
	}
	ib := firstInbound(doc)
	users := inboundUserMaps(ib)
	var kept []any
	for _, u := range users {
		m := map[string]any{"name": u["name"]}
		if v, ok := u["uuid"]; ok {
			m["uuid"] = v
		} else if v, ok := u["password"]; ok {
			m["uuid"] = v
		}
		kept = append(kept, m)
	}
	// 路径保持不变（用户订阅按备注匹配，路径一变旧链接就废了）
	path := "/"
	if tr := obj(ib["transport"]); tr != nil {
		if p := str(tr["path"]); p != "" {
			path = p
		}
	}
	ib["type"] = newProto
	ib["tag"] = fmt.Sprintf("%s-%d", target, number)
	ib["users"] = kept
	ib["transport"] = map[string]any{
		"type": "ws", "path": path,
		"max_early_data": 2048, "early_data_header_name": "Sec-WebSocket-Protocol",
	}

	wasActive := n.QuickOn
	quickRemove(n.Kind, n.Number)
	lp := panelPaths()
	newFile := fmt.Sprintf("%s-%d.json", target, number)
	if err := saveNodeDoc(newFile, doc); err != nil {
		return nil, err
	}
	_ = os.Remove(filepath.Join(lp.ConfDir, file))
	_ = os.Remove(nodeURLFile(n))
	_ = os.MkdirAll(lp.QuickDir, 0755)
	_ = writeFileAtomic(filepath.Join(lp.QuickDir, fmt.Sprintf("%s-%d.dedicated", target, number)), nil, 0644)
	_ = writeFileAtomic(filepath.Join(lp.URLDir, fmt.Sprintf("%s-%d.txt", target, number)), nil, 0644)
	_ = rebuildSubFile()
	_ = refreshUserSubs()
	_ = applySingboxReload()

	fresh, _, err := loadNode(newFile)
	if err != nil {
		return nil, err
	}
	if wasActive {
		return quickStart(newFile)
	}
	return fresh, nil
}

// quickInfo 是界面上的临时隧道一行。
type quickInfo struct {
	Unit     string `json:"unit"`
	Kind     string `json:"kind"`
	Number   int    `json:"number"`
	File     string `json:"file"`
	Exists   bool   `json:"exists"`
	Active   bool   `json:"active"`
	Domain   string `json:"domain"`
	Port     int    `json:"port"`
	Watchdog bool   `json:"watchdog"`
}

// listQuickTunnels 列出所有 cf-quick-* 单元。
func listQuickTunnels() []quickInfo {
	lp := panelPaths()
	entries, err := os.ReadDir(lp.QuickUnitDir)
	if err != nil {
		return nil
	}
	watchdog := false
	if out, err := adminExec("systemctl", "is-active", "cf-quick-watchdog.timer"); err == nil {
		watchdog = strings.TrimSpace(out) == "active"
	}
	var out []quickInfo
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "cf-quick-") || !strings.HasSuffix(name, ".service") ||
			strings.Contains(name, "watchdog") {
			continue
		}
		unit := strings.TrimSuffix(name, ".service")
		rest := strings.TrimPrefix(unit, "cf-quick-")
		i := strings.LastIndex(rest, "-")
		if i <= 0 {
			continue
		}
		kind := rest[:i]
		num, err := strconv.Atoi(rest[i+1:])
		if err != nil {
			continue
		}
		domain, active, exists := quickTunnelState(unit)
		qi := quickInfo{Unit: unit, Kind: kind, Number: num,
			File: fmt.Sprintf("%s-%d.json", kind, num), Exists: exists, Active: active,
			Domain: domain, Watchdog: watchdog}
		if n, _, err := loadNode(qi.File); err == nil {
			qi.Port = n.Port
		}
		out = append(out, qi)
	}
	return out
}

// quickTunnelProbe 探测临时隧道域名是否活着（给界面一个「连通」标记）。
func quickTunnelProbe(domain string) bool {
	if domain == "" {
		return false
	}
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Get("https://" + domain + "/")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode != 502 && resp.StatusCode != 530 && resp.StatusCode != 1033
}

// enableCDN 给 WS / XHTTP 类入站开 Cloudflare CDN（脚本 enable_ws_cdn）。
// 返回 CDN 域名与写进链接的节点。
func enableCDN(file, zoneName, prefix string) (string, string, error) {
	n, _, err := loadNode(file)
	if err != nil {
		return "", "", err
	}
	switch n.Kind {
	case "vless-ws", "vmess-ws", "trojan-ws", "vless-xhttp", "xhttp-udp-tls":
	default:
		return "", "", fmt.Errorf("%s 不支持 CDN（只有 WS / XHTTP 类可以）", n.Kind)
	}
	if n.Path == "" {
		return "", "", fmt.Errorf("入站没有 WS/XHTTP 路径")
	}
	if loadCFCreds() == nil {
		return "", "", fmt.Errorf("先配置 Cloudflare 凭证")
	}
	zone, err := cfFindZone(zoneName)
	if err != nil {
		return "", "", err
	}
	domain := strings.Trim(strings.TrimSpace(prefix), ".")
	if domain == "" {
		domain = zone.Name
	} else if domain != zone.Name && !strings.HasSuffix(domain, "."+zone.Name) {
		domain = domain + "." + zone.Name
	}
	if !validDomain(domain) {
		return "", "", fmt.Errorf("CDN 域名无效")
	}
	ip := hostPublicIP()
	if ip == "" {
		return "", "", fmt.Errorf("拿不到本机公网 IP，无法配 DNS")
	}
	var steps []string
	if err := cfUpsertDNS(zone.ID, domain, ip, true); err != nil {
		steps = append(steps, "DNS 失败："+err.Error())
	}
	mode := "flexible"
	if n.TLS {
		mode = "full"
	}
	if err := cfSetSSL(zone.ID, mode); err != nil {
		steps = append(steps, "SSL 失败："+err.Error())
	}
	if err := cfSetOriginPort(zone.ID, domain, n.Port); err != nil {
		steps = append(steps, "回源规则失败："+err.Error())
	}

	uuid, password := "", ""
	if len(n.Users) > 0 {
		uuid, password = n.Users[0].UUID, n.Users[0].Password
	}
	remark := fmt.Sprintf("%s_%s_cdn", nodeRemarkEmoji, n.Kind)
	eq := urlEscape(remark)
	path := n.Path
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	var link string
	switch n.Kind {
	case "vless-ws":
		link = fmt.Sprintf("vless://%s@%s:443?ed=2048&eh=Sec-WebSocket-Protocol&encryption=none&security=tls&sni=%s&type=ws&host=%s&path=%s?ed=2048#%s",
			uuid, cdnPreferredHost, domain, domain, path, eq)
	case "vmess-ws":
		vm := map[string]any{
			"v": "2", "ps": remark, "add": cdnPreferredHost, "port": "443", "id": uuid,
			"aid": "0", "encryption": "auto", "net": "ws", "type": "auto",
			"host": domain, "path": path + "?ed=2048", "tls": "tls", "sni": domain,
			"alpn": "", "fp": "firefox", "allowInsecure": false,
		}
		link = vmessLink(vm)
	case "trojan-ws":
		link = fmt.Sprintf("trojan://%s@%s:443?ed=2048&eh=Sec-WebSocket-Protocol&security=tls&sni=%s&type=ws&host=%s&path=%s?ed=2048#%s",
			password, cdnPreferredHost, domain, domain, path, eq)
	case "vless-xhttp", "xhttp-udp-tls":
		link = fmt.Sprintf("vless://%s@%s:443?encryption=none&security=tls&sni=%s&type=xhttp&host=%s&path=%s#%s",
			uuid, cdnPreferredHost, domain, domain, path, eq)
	}
	urlFile := nodeURLFile(n)
	blob, _ := os.ReadFile(urlFile)
	body := strings.TrimRight(string(blob), "\n")
	// 先删掉同一个域名的旧 _cdn 链接
	var kept []string
	for _, line := range strings.Split(body, "\n") {
		if line == "" {
			continue
		}
		if strings.Contains(line, "_cdn") && strings.Contains(line, "sni="+domain) {
			continue
		}
		kept = append(kept, line)
	}
	kept = append(kept, link)
	if err := writeFileAtomic(urlFile, []byte(strings.Join(kept, "\n")+"\n"), 0644); err != nil {
		return "", "", err
	}
	_ = rebuildSubFile()
	_ = refreshUserSubs()
	if len(steps) > 0 {
		return domain, link, fmt.Errorf("CDN 部分步骤失败：%s（链接已更新，但可能不通）", strings.Join(steps, "；"))
	}
	return domain, link, nil
}

// disableCDN 删掉某个 CDN 域名的回源规则与 DNS，并从链接里去掉。
func disableCDN(file, domain string) error {
	n, _, err := loadNode(file)
	if err != nil {
		return err
	}
	if !validDomain(domain) {
		return fmt.Errorf("域名无效")
	}
	if err := cfDeleteCDN(domain); err != nil {
		return err
	}
	urlFile := nodeURLFile(n)
	blob, _ := os.ReadFile(urlFile)
	var kept []string
	for _, line := range strings.Split(string(blob), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.Contains(line, "_cdn") && strings.Contains(line, "sni="+domain) {
			continue
		}
		kept = append(kept, line)
	}
	if err := writeFileAtomic(urlFile, []byte(strings.Join(kept, "\n")+"\n"), 0644); err != nil {
		return err
	}
	_ = rebuildSubFile()
	_ = refreshUserSubs()
	return nil
}

// enableFixedTunnel 给 WS / XHTTP 类入站加一条固定隧道路由（脚本 enable_ws_argo）。
func enableFixedTunnel(file, zoneName, prefix string) (string, string, error) {
	n, _, err := loadNode(file)
	if err != nil {
		return "", "", err
	}
	switch n.Kind {
	case "vless-ws", "vmess-ws", "trojan-ws", "vless-xhttp", "xhttp-udp-tls":
	default:
		return "", "", fmt.Errorf("%s 不支持固定隧道", n.Kind)
	}
	path := n.Path
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	_, hostname, err := cfSetupTunnel([]tunnelRoute{{Path: path, Port: n.Port, HTTPS: n.TLS}}, zoneName, prefix)
	if err != nil {
		return "", "", err
	}
	_ = applySingboxReload()
	uuid, password := "", ""
	if len(n.Users) > 0 {
		uuid, password = n.Users[0].UUID, n.Users[0].Password
	}
	remark := nodeRemarkEmoji + "Tunnel-" + n.Kind
	eq := urlEscape(remark)
	host := cdnPreferredHost
	var link string
	switch n.Kind {
	case "vless-ws":
		link = fmt.Sprintf("vless://%s@%s:443?ed=2048&eh=Sec-WebSocket-Protocol&encryption=none&security=tls&sni=%s&type=ws&host=%s&path=%s?ed=2048#%s",
			uuid, host, hostname, hostname, path, eq)
	case "vmess-ws":
		vm := map[string]any{
			"v": "2", "ps": remark, "add": host, "port": "443", "id": uuid,
			"aid": "0", "encryption": "auto", "net": "ws", "type": "auto",
			"host": hostname, "path": path + "?ed=2048", "tls": "tls", "sni": hostname,
			"alpn": "", "fp": "firefox", "allowInsecure": false,
		}
		link = vmessLink(vm)
	case "trojan-ws":
		link = fmt.Sprintf("trojan://%s@%s:443?ed=2048&eh=Sec-WebSocket-Protocol&security=tls&sni=%s&type=ws&host=%s&path=%s?ed=2048#%s",
			password, host, hostname, hostname, path, eq)
	case "vless-xhttp", "xhttp-udp-tls":
		link = fmt.Sprintf("vless://%s@%s:443?encryption=none&security=tls&sni=%s&type=xhttp&alpn=h3&host=%s&path=%s#%s",
			uuid, host, hostname, hostname, path, eq)
	}
	urlFile := nodeURLFile(n)
	blob, _ := os.ReadFile(urlFile)
	var kept []string
	for _, line := range strings.Split(string(blob), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.Contains(line, "Tunnel-") {
			continue
		}
		kept = append(kept, line)
	}
	kept = append(kept, link)
	if err := writeFileAtomic(urlFile, []byte(strings.Join(kept, "\n")+"\n"), 0644); err != nil {
		return "", "", err
	}
	_ = rebuildSubFile()
	_ = refreshUserSubs()
	return hostname, link, nil
}
