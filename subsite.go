package main

import (
	"bytes"
	_ "embed"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// 订阅服务与订阅主站（脚本 add_user_menu 第一次添加用户时做的那部分）。

//go:embed sub_service.py
var subServicePy []byte

const (
	subServicePath = "/usr/local/bin/sing-box-subscription.py"
	subServiceUnit = "/etc/systemd/system/sing-box-subscription.service"
	subServiceName = "sing-box-subscription"
)

const subServiceUnitText = `[Unit]
Description=Sing-box Dynamic Subscription Service
After=network.target

[Service]
Type=simple
ExecStart=/usr/bin/python3 /usr/local/bin/sing-box-subscription.py
Restart=always
RestartSec=2
User=root

[Install]
WantedBy=multi-user.target
`

// subMu 串行化添加 / 删除 / 重建用户，防止连点建出好几个。
var subMu sync.Mutex

// subPorts 是订阅主站可选端口（Cloudflare 代理支持的 HTTPS 端口）。
var subPorts = map[int]bool{443: true, 2053: true, 2083: true, 2087: true, 2096: true, 8443: true}

// ensureSubService 确保订阅服务装好并在跑。
func ensureSubService() error {
	if layoutOverride != nil || !hasCmd("systemctl") || !dirExists("/run/systemd/system") {
		return nil
	}
	changed := false
	if old, err := os.ReadFile(subServicePath); err != nil || len(bytes.TrimSpace(old)) == 0 {
		if err := writeFileAtomic(subServicePath, subServicePy, 0755); err != nil {
			return fmt.Errorf("写订阅服务失败: %w", err)
		}
		changed = true
	}
	if !pathExists(subServiceUnit) {
		if err := writeFileAtomic(subServiceUnit, []byte(subServiceUnitText), 0644); err != nil {
			return fmt.Errorf("写订阅服务单元失败: %w", err)
		}
		_ = exec.Command("systemctl", "daemon-reload").Run()
		changed = true
	}
	if changed {
		_ = exec.Command("systemctl", "enable", "--now", subServiceName).Run()
		_ = exec.Command("systemctl", "restart", subServiceName).Run()
		return nil
	}
	if exec.Command("systemctl", "is-active", "--quiet", subServiceName).Run() != nil {
		if out, err := exec.Command("systemctl", "enable", "--now", subServiceName).CombinedOutput(); err != nil {
			return fmt.Errorf("订阅服务启动失败: %s", strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// subSiteReady 订阅主站（singbox_sub.conf）是否已有域名。
func subSiteReady() bool {
	d, _ := nginxSubDomain()
	return d != ""
}

// subSiteHost 把选中的证书变成订阅域名：通配证书拼上前缀。
func subSiteHost(cert, prefix string) (string, string, error) {
	cert = strings.TrimSpace(cert)
	prefix = strings.Trim(strings.TrimSpace(prefix), ".")
	if cert == "" {
		return "", "", fmt.Errorf("选择订阅域名")
	}
	zone := strings.TrimPrefix(cert, "*.")
	host := cert
	if strings.HasPrefix(cert, "*.") {
		if prefix == "" {
			return "", "", fmt.Errorf("通配证书要填前缀")
		}
		host = prefix + "." + zone
	} else if prefix != "" && certIsWildcard(cert) {
		host = prefix + "." + zone
	}
	if net.ParseIP(host) == nil && !validDomain(host) {
		return "", "", fmt.Errorf("域名无效: %s", host)
	}
	return host, cert, nil
}

// certIsWildcard 证书是否带 *.域名。
func certIsWildcard(cert string) bool {
	if strings.HasPrefix(cert, "*.") {
		return true
	}
	for _, c := range listCerts() {
		if c.Domain != cert {
			continue
		}
		for _, n := range c.DNSNames {
			if n == "*."+cert {
				return true
			}
		}
	}
	return false
}

// writeSubSite 生成订阅主站配置（与脚本 add_user_menu 写的 singbox_sub.conf 一致）。
func writeSubSite(cert, prefix string, port int) error {
	if port == 0 {
		port = 443
	}
	if !subPorts[port] {
		return fmt.Errorf("订阅端口只能 443/2053/2083/2087/2096/8443")
	}
	host, certName, err := subSiteHost(cert, prefix)
	if err != nil {
		return err
	}
	cp, kp := certPathsFor(certName)
	if !pathExists(cp) || !pathExists(kp) {
		return fmt.Errorf("没有找到 %s 的证书", certName)
	}
	lp := panelPaths()
	if err := os.MkdirAll(lp.NginxUserDir, 0755); err != nil {
		return err
	}
	serverName := host
	if strings.Contains(host, ":") {
		serverName = "[" + host + "]"
	}
	conf := fmt.Sprintf(`server {
    listen %d ssl http2;
    listen [::]:%d ssl http2;
    server_name %s;

    ssl_certificate %s;
    ssl_certificate_key %s;
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_ciphers HIGH:!aNULL:!MD5;

    include %s/*.conf;

    location / {
        return 404;
    }
    location ~ /\. {
        deny all;
        access_log off;
        log_not_found off;
    }
}
`, port, port, serverName, cp, kp, lp.NginxUserDir)
	if err := writeVerified(lp.NginxSubConf, []byte(conf), 0644, nginxTest); err != nil {
		return fmt.Errorf("订阅主站配置未通过 nginx -t: %w", err)
	}
	_ = allowPort(port, "tcp")
	return nil
}

// ensureSubIncludes：订阅域名+端口和别的站点（比如面板反代）是同一个时，nginx 只会用先加载的那个 server，
// 订阅路径就落到别的站点上（面板回 404）。把用户订阅片段也 include 进那些 server 里。
func ensureSubIncludes() bool {
	host, port := nginxSubDomain()
	if host == "" {
		return false
	}
	if port == "" {
		port = "443"
	}
	lp := panelPaths()
	inc := lp.NginxUserDir + "/*.conf"
	entries, err := os.ReadDir(lp.NginxConfDir)
	if err != nil {
		return false
	}
	any := false
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".conf") {
			continue
		}
		path := lp.NginxConfDir + "/" + e.Name()
		if path == lp.NginxSubConf {
			continue
		}
		blob, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		out, changed := addSubInclude(string(blob), host, port, inc)
		if changed && writeVerified(path, []byte(out), 0644, nginxTest) == nil {
			any = true
		}
	}
	return any
}

// addSubInclude 在 server_name 含 host、listen 含 port、还没有 include 的 server 块里补一行 include。
func addSubInclude(text, host, port, inc string) (string, bool) {
	lines := strings.Split(text, "\n")
	depth := 0
	start := -1
	changed := false
	var out []string
	var block []string
	flush := func() {
		hasName, hasPort, hasInc, nameIdx := false, false, false, -1
		for i, l := range block {
			t := strings.TrimSpace(l)
			if strings.HasPrefix(t, "server_name") {
				for _, f := range strings.Fields(strings.TrimSuffix(strings.TrimPrefix(t, "server_name"), ";")) {
					if strings.Trim(f, "[];") == host {
						hasName, nameIdx = true, i
					}
				}
			}
			if strings.HasPrefix(t, "listen") {
				for _, f := range strings.Fields(strings.TrimSuffix(t, ";")) {
					f = strings.TrimSuffix(f, ";")
					if f == port || strings.HasSuffix(f, ":"+port) {
						hasPort = true
					}
				}
			}
			if strings.HasPrefix(t, "include") && strings.Contains(t, inc) {
				hasInc = true
			}
		}
		if hasName && hasPort && !hasInc {
			b := append([]string{}, block[:nameIdx+1]...)
			b = append(b, "    include "+inc+";")
			block = append(b, block[nameIdx+1:]...)
			changed = true
		}
		out = append(out, block...)
		block = nil
	}
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if depth == 0 && start < 0 && strings.HasPrefix(t, "server") && strings.Contains(t, "{") {
			start = len(out)
		}
		if start >= 0 {
			block = append(block, l)
		} else {
			out = append(out, l)
		}
		if !strings.HasPrefix(t, "#") {
			depth += strings.Count(l, "{") - strings.Count(l, "}")
		}
		if start >= 0 && depth == 0 {
			flush()
			start = -1
		}
	}
	if block != nil {
		out = append(out, block...)
	}
	return strings.Join(out, "\n"), changed
}
