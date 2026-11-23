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
