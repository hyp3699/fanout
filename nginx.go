package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Nginx 管理：与 sing-box-08.sh 共用 /etc/nginx/conf.d。
//
// 脚本自己有两个文件不能动：singbox_sub.conf（订阅主站）和 singbox_users/ 目录，
// 以及 sing-box.conf（脚本导出的订阅）。这里统一保护起来，页面上只能看不能删。
// 所有写入都先 nginx -t，不过就恢复成原来的内容。

// nginxProtected 是脚本拥有的配置，网页只读。
var nginxProtected = map[string]bool{
	"singbox_sub.conf": true,
	"sing-box.conf":    true,
	"sing-box1.conf":   true,
}

// isProtectedSite 判断是否为脚本的文件或用户订阅目录里的文件。
func isProtectedSite(name string) bool {
	return nginxProtected[name]
}

// nginxStatus 汇总 nginx 的运行状态。
type nginxStatus struct {
	Installed bool   `json:"installed"`
	Version   string `json:"version"`
	Active    bool   `json:"active"`
	Init      string `json:"init"`
	TestOK    bool   `json:"test_ok"`
	TestErr   string `json:"test_err"`
	ConfCount int    `json:"conf_count"`
	PID       string `json:"pid"`
}

func nginxInstalled() bool { return haveCmd("nginx") }

func getNginxStatus() nginxStatus {
	st := nginxStatus{Installed: nginxInstalled()}
	if !st.Installed {
		return st
	}
	if out, err := adminExec("nginx", "-v"); err == nil || out != "" {
		st.Version = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(out), "nginx version:"))
	}
	st.Init = detectService("nginx")
	if st.Init == "systemd" {
		if out, err := adminExec("systemctl", "is-active", "nginx"); err == nil {
			st.Active = strings.TrimSpace(out) == "active"
		}
	} else if st.Init == "openrc" {
		if _, err := adminExec("rc-service", "nginx", "status"); err == nil {
			st.Active = true
		}
	}
	if blob, err := os.ReadFile("/run/nginx.pid"); err == nil {
		st.PID = strings.TrimSpace(string(blob))
	}
	if err := nginxTest(); err != nil {
		st.TestErr = err.Error()
	} else {
		st.TestOK = true
	}
	lp := panelPaths()
	if entries, err := os.ReadDir(lp.NginxConfDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".conf") {
				st.ConfCount++
			}
		}
	}
	return st
}

// nginxTest 跑 nginx -t。
func nginxTest() error {
	if !nginxInstalled() {
		return fmt.Errorf("本机没有安装 nginx")
	}
	out, err := adminExec("nginx", "-t")
	if err != nil {
		return fmt.Errorf("nginx -t 未通过：%s", trimOut(out))
	}
	return nil
}

// nginxApply 让 nginx 用上新配置：在跑就 reload，没跑就 start。
func nginxApply() error {
	if err := nginxTest(); err != nil {
		return err
	}
	switch detectService("nginx") {
	case "systemd":
		if out, err := adminExec("systemctl", "is-active", "nginx"); err == nil && strings.TrimSpace(out) == "active" {
			if out, err := adminExec("systemctl", "reload", "nginx"); err != nil {
				return fmt.Errorf("nginx reload 失败：%s", trimOut(out))
			}
			return nil
		}
		if out, err := adminExec("systemctl", "start", "nginx"); err != nil {
			return fmt.Errorf("nginx 启动失败：%s", trimOut(out))
		}
		return nil
	case "openrc":
		if _, err := adminExec("rc-service", "nginx", "status"); err == nil {
			if out, err := adminExec("rc-service", "nginx", "reload"); err != nil {
				return fmt.Errorf("nginx reload 失败：%s", trimOut(out))
			}
			return nil
		}
		if out, err := adminExec("rc-service", "nginx", "start"); err != nil {
			return fmt.Errorf("nginx 启动失败：%s", trimOut(out))
		}
		return nil
	}
	// 没有服务管理时退回 nginx -s reload
	if out, err := adminExec("nginx", "-s", "reload"); err != nil {
		return fmt.Errorf("nginx 重载失败：%s", trimOut(out))
	}
	return nil
}

// nginxService 执行 start / stop / restart / reload / enable / disable。
func nginxService(action string) error {
	if !nginxInstalled() {
		return fmt.Errorf("本机没有安装 nginx")
	}
	init := detectService("nginx")
	if init == "" {
		if action == "reload" {
			return nginxApply()
		}
		return fmt.Errorf("没有找到 nginx 服务（systemd / OpenRC）")
	}
	var out string
	var err error
	if init == "systemd" {
		out, err = adminExec("systemctl", action, "nginx")
	} else {
		out, err = adminExec("rc-service", "nginx", action)
	}
	if err != nil && action == "reload" {
		// reload 不支持时退一步 restart
		if init == "systemd" {
			out, err = adminExec("systemctl", "restart", "nginx")
		} else {
			out, err = adminExec("rc-service", "nginx", "restart")
		}
	}
	if err != nil {
		return fmt.Errorf("%s 失败：%s", action, trimOut(out))
	}
	return nil
}

// nginxSite 是 conf.d 里的一个站点文件。
type nginxSite struct {
	Name        string   `json:"name"`
	Path        string   `json:"path"`
	Size        int64    `json:"size"`
	Modified    string   `json:"modified"`
	Protected   bool     `json:"protected"`
	UserSite    bool     `json:"user_site"` // singbox_users/ 里的订阅片段
	Listen      []string `json:"listen"`
	ServerNames []string `json:"server_names"`
	ProxyPass   []string `json:"proxy_pass"`
}

// listNginxSites 列出 conf.d 的站点与订阅用户目录里的片段。
func listNginxSites() []nginxSite {
	lp := panelPaths()
	out := []nginxSite{}
	read := func(dir string, userSite bool) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".conf") {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			site := nginxSite{
				Name:      e.Name(),
				Path:      filepath.Join(dir, e.Name()),
				Size:      info.Size(),
				Modified:  info.ModTime().Format("2006-01-02 15:04"),
				Protected: isProtectedSite(e.Name()),
				UserSite:  userSite,
			}
			if blob, err := os.ReadFile(site.Path); err == nil {
				site.Listen = matchAll(string(blob), `(?m)^\s*listen\s+([^;]+);`)
				site.ServerNames = matchAll(string(blob), `(?m)^\s*server_name\s+([^;]+);`)
				site.ProxyPass = matchAll(string(blob), `proxy_pass\s+([^;]+);`)
			}
			out = append(out, site)
		}
	}
	read(lp.NginxConfDir, false)
	read(lp.NginxUserDir, true)
	sort.Slice(out, func(i, j int) bool {
		if out[i].UserSite != out[j].UserSite {
			return !out[i].UserSite
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func matchAll(text, pattern string) []string {
	re := regexp.MustCompile(pattern)
	var out []string
	for _, m := range re.FindAllStringSubmatch(text, -1) {
		if len(m) > 1 {
			if v := strings.TrimSpace(m[1]); v != "" {
				out = append(out, v)
			}
		}
	}
	return out
}

// readNginxSite 读一个站点文件的内容（最多 256KB）。
func readNginxSite(name string, userSite bool) (string, error) {
	path, err := nginxSitePath(name, userSite)
	if err != nil {
		return "", err
	}
	blob, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if len(blob) > 256<<10 {
		return "", fmt.Errorf("文件太大（%d 字节），请在命令行里改", len(blob))
	}
	return string(blob), nil
}

// nginxSitePath 拼出站点文件路径，顺手挡住路径穿越。
func nginxSitePath(name string, userSite bool) (string, error) {
	if !validConfName(name) {
		return "", fmt.Errorf("文件名只能用字母、数字、- _ 和 .conf")
	}
	lp := panelPaths()
	base := lp.NginxConfDir
	if userSite {
		base = lp.NginxUserDir
	}
	return safeJoin(base, name)
}

// saveNginxSite 保存站点：先 nginx -t，再 reload；任何一步失败都恢复原文件。
func saveNginxSite(name, content string, userSite bool) error {
	path, err := nginxSitePath(name, userSite)
	if err != nil {
		return err
	}
	if isProtectedSite(name) {
		return fmt.Errorf("%s 由 sing-box-08.sh 维护，网页只读", name)
	}
	content = strings.TrimRight(content, "\n") + "\n"
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("内容不能为空")
	}
	return writeNginxVerified(path, []byte(content), false)
}

// writeNginxVerified 写文件 → nginx -t → 生效，任何一步不过都恢复原文件。
// saveNginxSite 与 deleteNginxSite 共用，避免只回滚一半。
func writeNginxVerified(path string, data []byte, remove bool) error {
	var old []byte
	had := false
	if blob, err := os.ReadFile(path); err == nil {
		old, had = blob, true
	}
	restore := func() {
		if had {
			_ = writeFileAtomic(path, old, 0644)
		} else {
			_ = os.Remove(path)
		}
	}
	if remove {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	} else if err := writeFileAtomic(path, data, 0644); err != nil {
		return err
	}
	if err := nginxTest(); err != nil {
		restore()
		return err
	}
	if err := nginxApply(); err != nil {
		restore()
		// 恢复后再让 nginx 读回旧配置，别把它留在坏状态
		_ = nginxApply()
		return err
	}
	return nil
}

// deleteNginxSite 删站点：挪进回收目录，nginx -t 不过就挪回来。
func deleteNginxSite(name string, userSite bool) error {
	if isProtectedSite(name) {
		return fmt.Errorf("%s 由 sing-box-08.sh 维护，不能删", name)
	}
	path, err := nginxSitePath(name, userSite)
	if err != nil {
		return err
	}
	if !pathExists(path) {
		return fmt.Errorf("站点文件 %s 不存在", name)
	}
	lp := panelPaths()
	// 写一份到回收目录留底，然后在原位走「删 → 校验 → 生效 → 失败还原」
	blob, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if _, err := moveToTrash(lp.NginxTrashDir, path); err != nil {
		return err
	}
	if err := writeNginxVerified(path, blob, true); err != nil {
		return err
	}
	return nil
}

// proxySpec 是「新增反向代理站点」的表单。
type proxySpec struct {
	Name       string `json:"name"`        // 文件名，不带 .conf
	Domain     string `json:"domain"`      // server_name
	ListenPort int    `json:"listen_port"` // 80 / 443 / 其他
	Target     string `json:"target"`      // 127.0.0.1:9393
	TLS        bool   `json:"tls"`
	WebSocket  bool   `json:"websocket"`
	ExtraConf  string `json:"extra"` // 追加到 location 里
}

// buildProxyConf 生成一个反向代理 server 块。
func buildProxyConf(spec proxySpec) (string, error) {
	domain := strings.TrimSpace(spec.Domain)
	isIP := net.ParseIP(domain) != nil
	if !isIP && !validDomain(strings.TrimPrefix(domain, "*.")) {
		return "", fmt.Errorf("域名或 IP 格式无效")
	}
	if !validPort(spec.ListenPort) {
		return "", fmt.Errorf("监听端口无效")
	}
	target, err := normalizeTarget(spec.Target)
	if err != nil {
		return "", err
	}
	scheme := "http"
	origin := target
	if strings.HasPrefix(target, "https://") {
		scheme = "https"
		origin = strings.TrimPrefix(target, "https://")
	}
	if !strings.Contains(origin, ":") {
		if scheme == "https" {
			origin += ":443"
		} else {
			origin += ":80"
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# fanout 生成的反向代理：%s → %s\n", domain, target)
	ssl := ""
	if spec.TLS && spec.ListenPort != 80 {
		ssl = " ssl"
	}
	serverName := domain
	if isIP {
		// 用 IP 访问时 Host 头就是 IP，server_name 写 IP 也能匹配；IPv6 要加方括号
		if strings.Contains(domain, ":") {
			serverName = "[" + domain + "]"
		}
	}
	fmt.Fprintf(&b, "server {\n    listen %d%s;\n    listen [::]:%d%s;\n    server_name %s;\n",
		spec.ListenPort, ssl, spec.ListenPort, ssl, serverName)
	if spec.TLS {
		cert, key := certPathsFor(domain)
		if !pathExists(cert) || !pathExists(key) {
			return "", fmt.Errorf("没有找到 %s 的证书（%s），先在「证书」里申请，或把域名换成已有证书的域名", domain, cert)
		}
		fmt.Fprintf(&b, "\n    ssl_certificate %s;\n    ssl_certificate_key %s;\n", cert, key)
		b.WriteString("    ssl_protocols TLSv1.2 TLSv1.3;\n    ssl_ciphers HIGH:!aNULL:!MD5;\n")
		fmt.Fprintf(&b, "    add_header Strict-Transport-Security \"max-age=31536000\" always;\n")
		if spec.ListenPort == 80 {
			fmt.Fprintf(&b, "    return 301 https://$host$request_uri;\n")
			b.WriteString("}\n")
			return b.String(), nil
		}
	}
	b.WriteString("\n    client_max_body_size 50m;\n")
	b.WriteString("    location / {\n")
	fmt.Fprintf(&b, "        proxy_pass %s://%s;\n", scheme, origin)
	b.WriteString("        proxy_http_version 1.1;\n")
	b.WriteString("        proxy_set_header Host $host;\n")
	b.WriteString("        proxy_set_header X-Real-IP $remote_addr;\n")
	b.WriteString("        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;\n")
	b.WriteString("        proxy_set_header X-Forwarded-Proto $scheme;\n")
	if spec.WebSocket {
		b.WriteString("        proxy_set_header Upgrade $http_upgrade;\n")
		b.WriteString("        proxy_set_header Connection \"upgrade\";\n")
		b.WriteString("        proxy_read_timeout 3600s;\n")
		b.WriteString("        proxy_send_timeout 3600s;\n")
	}
	if extra := strings.TrimSpace(spec.ExtraConf); extra != "" {
		for _, line := range strings.Split(extra, "\n") {
			if s := strings.TrimSpace(line); s != "" {
				b.WriteString("        " + s + "\n")
			}
		}
	}
	b.WriteString("    }\n}\n")
	return b.String(), nil
}

// normalizeTarget 校验反代目标，只允许 host:port 或 http(s)://host:port。
func normalizeTarget(raw string) (string, error) {
	t := strings.TrimSpace(raw)
	if t == "" {
		return "", fmt.Errorf("反代目标不能为空，例如 127.0.0.1:9393")
	}
	if strings.ContainsAny(t, " \t\n;{}()") {
		return "", fmt.Errorf("反代目标里有非法字符")
	}
	base := t
	if strings.Contains(base, "://") {
		parts := strings.SplitN(base, "://", 2)
		if parts[0] != "http" && parts[0] != "https" {
			return "", fmt.Errorf("只支持 http:// 或 https://")
		}
		base = parts[1]
	}
	host := base
	if strings.HasPrefix(base, "[") {
		if i := strings.Index(base, "]"); i > 0 {
			host = base[1:i]
		}
	} else if i := strings.LastIndex(base, ":"); i > 0 {
		host = base[:i]
	}
	if host == "" || strings.ContainsAny(host, "/?#") {
		return "", fmt.Errorf("反代目标格式不对")
	}
	return t, nil
}

// nginxSubDomain 从 singbox_sub.conf 里取订阅主站的域名与端口。
func nginxSubDomain() (string, string) {
	lp := panelPaths()
	blob, err := os.ReadFile(lp.NginxSubConf)
	if err != nil {
		return "", ""
	}
	text := string(blob)
	domain := ""
	if v := matchAll(text, `(?m)^\s*server_name\s+([^;]+);`); len(v) > 0 {
		domain = strings.TrimSpace(v[0])
	}
	port := ""
	if v := matchAll(text, `(?m)^\s*listen\s+([^;]+);`); len(v) > 0 {
		if p := regexp.MustCompile(`\d+`).FindString(v[0]); p != "" {
			port = p
		}
	}
	if domain == "_" {
		domain = ""
	}
	return domain, port
}

// nginxLogs 返回最近的访问/错误日志（取存在的那个）。
func nginxLogs(lines int) string {
	if lines <= 0 || lines > 500 {
		lines = 120
	}
	candidates := []string{"/var/log/nginx/error.log", "/var/log/nginx/access.log"}
	var b strings.Builder
	for _, path := range candidates {
		blob, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		text := string(blob)
		parts := strings.Split(text, "\n")
		if len(parts) > lines {
			parts = parts[len(parts)-lines:]
		}
		fmt.Fprintf(&b, "== %s ==\n%s\n", path, strings.Join(parts, "\n"))
	}
	if b.Len() == 0 {
		return "没有找到 nginx 日志（/var/log/nginx/）"
	}
	return trimOut(b.String())
}

// nginxMTime 判断配置目录最近有没有变化（界面刷新用）。
func nginxMTime() time.Time {
	lp := panelPaths()
	st, err := os.Stat(lp.NginxConfDir)
	if err != nil {
		return time.Time{}
	}
	return st.ModTime()
}

// portFromTarget 从 "127.0.0.1:9393" 里取端口，测试用。
func portFromTarget(t string) int {
	i := strings.LastIndex(t, ":")
	if i < 0 {
		return 0
	}
	p, _ := strconv.Atoi(strings.TrimRight(t[i+1:], "/"))
	return p
}
