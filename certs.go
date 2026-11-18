package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// 证书管理：与 sing-box-08.sh 同一套目录（/root/cert/<域名>/fullchain.pem + privkey.pem），
// 申请走 acme.sh --dns dns_cf（Cloudflare DNS-01，不用占 80 端口），
// 也可以直接调 Cloudflare 申请 15 年 Origin CA 证书。

// certItem 是界面上的一个证书。
type certItem struct {
	Domain   string   `json:"domain"`
	Dir      string   `json:"dir"`
	CertPath string   `json:"cert_path"`
	KeyPath  string   `json:"key_path"`
	NotAfter string   `json:"not_after"` // yyyy-mm-dd hh:mm
	Days     int      `json:"days"`      // 还剩多少天
	SelfSign bool     `json:"self_signed"`
	DNSNames []string `json:"dns_names"`
	Issuer   string   `json:"issuer"`
	UsedBy   []string `json:"used_by"` // 哪些入站 / Nginx 站点在用
	HasKey   bool     `json:"has_key"`
}

// ensureSelfSignedCert 保证 /etc/sing-box/cert.pem 与 private.key 在。
// 脚本安装时已经生成过；没有就现造一张（10 年自签，SAN 带公网 IP），
// 免得网页建的 Hysteria2 / TUIC 因为没有证书直接失败。
func ensureSelfSignedCert() (string, string, error) {
	lp := panelPaths()
	if pathExists(lp.SelfCert) && pathExists(lp.SelfKey) {
		return lp.SelfCert, lp.SelfKey, nil
	}
	certPEM, keyPEM, err := generateSelfSignedPEM()
	if err != nil {
		return "", "", err
	}
	if err := writeFileAtomic(lp.SelfCert, certPEM, 0644); err != nil {
		return "", "", err
	}
	if err := writeFileAtomic(lp.SelfKey, keyPEM, 0600); err != nil {
		return "", "", err
	}
	return lp.SelfCert, lp.SelfKey, nil
}

// generateSelfSignedPEM 造一张 10 年自签证书（EC P-256，SAN 带公网 IP）。
func generateSelfSignedPEM() ([]byte, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	tpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "sing-box"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost", "www.bing.com"},
	}
	if ip := hostPublicIP(); ip != "" {
		if parsed := net.ParseIP(ip); parsed != nil {
			tpl.IPAddresses = append(tpl.IPAddresses, parsed)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

// listCerts 列出所有证书目录里的证书，并标出被谁用着。
func listCerts() []certItem {
	lp := panelPaths()
	seen := map[string]bool{}
	var out []certItem
	for _, base := range lp.CertDirs {
		entries, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			domain := e.Name()
			if !validDomain(strings.TrimPrefix(domain, "*.")) && !validDomain(domain) {
				continue
			}
			dir := filepath.Join(base, domain)
			certPath := filepath.Join(dir, "fullchain.pem")
			keyPath := filepath.Join(dir, "privkey.pem")
			if !pathExists(certPath) {
				continue
			}
			item := certItem{
				Domain:   domain,
				Dir:      dir,
				CertPath: certPath,
				KeyPath:  keyPath,
				HasKey:   pathExists(keyPath),
			}
			if info := readCertInfo(certPath); info != nil {
				item.NotAfter = info.notAfter.Format("2006-01-02 15:04")
				item.Days = int(time.Until(info.notAfter).Hours() / 24)
				item.SelfSign = info.selfSigned
				item.DNSNames = info.dnsNames
				item.Issuer = info.issuer
			}
			item.UsedBy = certUsedBy(domain, certPath)
			key := base + "/" + domain
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, item)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Domain < out[j].Domain })
	return out
}

type certDetails struct {
	notAfter   time.Time
	selfSigned bool
	dnsNames   []string
	issuer     string
}

// readCertInfo 解析 PEM 证书。读不出来返回 nil（界面显示读取失败）。
func readCertInfo(path string) *certDetails {
	blob, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	block, _ := pem.Decode(blob)
	if block == nil {
		return nil
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil
	}
	return &certDetails{
		notAfter:   cert.NotAfter,
		selfSigned: cert.RawIssuer != nil && string(cert.RawIssuer) == string(cert.RawSubject),
		dnsNames:   cert.DNSNames,
		issuer:     cert.Issuer.CommonName,
	}
}

// certUsedBy 扫 sing-box 配置与 Nginx 配置，找出引用这个证书文件的地方。
func certUsedBy(domain, certPath string) []string {
	lp := panelPaths()
	needle := "/" + domain + "/"
	var out []string

	writeMeta := func(dir, label string, files []string) {
		for _, f := range files {
			blob, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			text := string(blob)
			if !strings.Contains(text, needle) && !strings.Contains(text, certPath) {
				continue
			}
			out = append(out, label+filepath.Base(f))
		}
	}

	if entries, err := os.ReadDir(lp.ConfDir); err == nil {
		var files []string
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
				files = append(files, filepath.Join(lp.ConfDir, e.Name()))
			}
		}
		writeMeta(lp.ConfDir, "入站 ", files)
	}
	if entries, err := os.ReadDir(lp.NginxConfDir); err == nil {
		var files []string
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".conf") {
				files = append(files, filepath.Join(lp.NginxConfDir, e.Name()))
			}
		}
		if list, err := os.ReadDir(lp.NginxUserDir); err == nil {
			for _, e := range list {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".conf") {
					files = append(files, filepath.Join(lp.NginxUserDir, e.Name()))
				}
			}
		}
		writeMeta(lp.NginxConfDir, "Nginx ", files)
	}
	return out
}

// certByDomain 找一个证书。
func certByDomain(domain string) *certItem {
	for _, c := range listCerts() {
		if c.Domain == domain {
			item := c
			return &item
		}
	}
	return nil
}

// deleteCert 删证书。被入站/Nginx 用着的拒绝删（脚本同样的保护），
// 删除是挪进回收目录而不是 rm -rf。
func deleteCert(domain string, deleteDNS bool) (string, error) {
	if !validDomain(strings.TrimPrefix(domain, "*.")) {
		return "", fmt.Errorf("域名格式无效")
	}
	lp := panelPaths()
	var moved []string
	found := false
	for _, base := range lp.CertDirs {
		dir := filepath.Join(base, domain)
		if !pathExists(filepath.Join(dir, "fullchain.pem")) {
			continue
		}
		if used := certUsedBy(domain, filepath.Join(dir, "fullchain.pem")); len(used) > 0 {
			return "", fmt.Errorf("证书 %s 正被使用（%s），先删掉或改掉相关入站/站点再删", domain, strings.Join(used, "、"))
		}
		dst, err := moveToTrash(lp.CertTrash, dir)
		if err != nil {
			return "", err
		}
		moved = append(moved, dst)
		found = true
	}
	if !found {
		return "", fmt.Errorf("没有找到 %s 的证书", domain)
	}
	// 取消 acme.sh 的续签任务
	if pathExists(lp.AcmeSh) {
		_, _ = adminExec(lp.AcmeSh, "--remove", "-d", domain)
	}
	dnsNote := ""
	if deleteDNS {
		if err := cfDeleteCDN(domain); err != nil {
			dnsNote = "；但 Cloudflare DNS 没删掉：" + err.Error()
		} else {
			dnsNote = "；Cloudflare DNS 已删除"
		}
	}
	return "证书已移到回收目录：\n" + strings.Join(moved, "\n") + dnsNote, nil
}

// certIssueSpec 是「申请证书」的表单。
type certIssueSpec struct {
	Zone     string `json:"zone"`     // Cloudflare 上的根域名
	Mode     string `json:"mode"`     // root / sub / wildcard / origin_ca
	Prefix   string `json:"prefix"`   // sub 模式的前缀
	Wildcard bool   `json:"wildcard"` // Origin CA 也可以带通配
}

// certDomainFor 算出要签发的域名。
func certDomainFor(spec certIssueSpec) (string, error) {
	zone := strings.TrimSpace(spec.Zone)
	if !validDomain(zone) {
		return "", fmt.Errorf("请选择一个 Cloudflare 域名")
	}
	switch spec.Mode {
	case "", "root":
		return zone, nil
	case "sub":
		prefix := strings.Trim(strings.TrimSpace(spec.Prefix), ".")
		if prefix == "" || strings.Contains(prefix, ".") {
			return "", fmt.Errorf("前缀只能是单段，不能带点")
		}
		for _, c := range prefix {
			ok := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-'
			if !ok {
				return "", fmt.Errorf("前缀只能用字母、数字和 -")
			}
		}
		d := prefix + "." + zone
		if !validDomain(d) {
			return "", fmt.Errorf("拼出来的域名不合法：%s", d)
		}
		return d, nil
	case "wildcard":
		return "*." + zone, nil
	case "origin_ca":
		if spec.Wildcard {
			return "*." + zone, nil
		}
		if p := strings.Trim(strings.TrimSpace(spec.Prefix), "."); p != "" {
			d := p + "." + zone
			if !validDomain(d) {
				return "", fmt.Errorf("域名不合法：%s", d)
			}
			return d, nil
		}
		return zone, nil
	}
	return "", fmt.Errorf("未知的证书模式")
}

// runWithTimeout 跑一个可能很久的外部命令（acme.sh）。
func runWithTimeout(env []string, timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return string(out), fmt.Errorf("执行超时：%s %s", name, strings.Join(args, " "))
	}
	return string(out), err
}

// issueCert 申请证书。acme.sh + DNS-01，或者 Cloudflare Origin CA。
func issueCert(spec certIssueSpec) (*certItem, string, error) {
	lp := panelPaths()
	creds := loadCFCreds()
	if creds == nil {
		return nil, "", fmt.Errorf("先在这里或脚本的 token 管理里填好 Cloudflare 凭证")
	}
	domain, err := certDomainFor(spec)
	if err != nil {
		return nil, "", err
	}
	zone, err := cfFindZone(strings.TrimPrefix(domain, "*."))
	if err != nil {
		return nil, "", err
	}
	saveDir := filepath.Join(lp.CertDirs[0], domain)
	if err := os.MkdirAll(saveDir, 0755); err != nil {
		return nil, "", err
	}
	var log strings.Builder

	if spec.Mode == "origin_ca" {
		certPEM, keyPEM, err := cfOriginCA(domain)
		if err != nil {
			return nil, "", fmt.Errorf("Cloudflare Origin CA 申请失败: %w", err)
		}
		if err := writeFileAtomic(filepath.Join(saveDir, "fullchain.pem"), []byte(certPEM), 0644); err != nil {
			return nil, "", err
		}
		if err := writeFileAtomic(filepath.Join(saveDir, "privkey.pem"), []byte(keyPEM), 0600); err != nil {
			return nil, "", err
		}
		log.WriteString("已签发 Cloudflare Origin CA 证书（15 年）\n")
	} else {
		acme, err := ensureAcmeSh(&log)
		if err != nil {
			return nil, log.String(), err
		}
		env := []string{}
		if creds.Kind == "token" {
			env = append(env, "CF_Token="+creds.Token)
		} else {
			env = append(env, "CF_Email="+creds.Email, "CF_Key="+creds.Key)
		}
		_, _ = runWithTimeout(env, 60*time.Second, acme, "--set-default-ca", "--server", "letsencrypt")
		out, err := runWithTimeout(env, 300*time.Second, acme,
			"--issue", "--dns", "dns_cf", "-d", domain, "--keylength", "ec-256", "--force")
		log.WriteString(trimOut(out))
		if err != nil {
			return nil, log.String(), fmt.Errorf("acme.sh 申请失败（检查 Token 是否有 DNS 编辑权限）")
		}
		out, err = runWithTimeout(env, 120*time.Second, acme,
			"--installcert", "-d", domain, "--ecc",
			"--key-file", filepath.Join(saveDir, "privkey.pem"),
			"--fullchain-file", filepath.Join(saveDir, "fullchain.pem"),
			"--reloadcmd", "systemctl reload nginx >/dev/null 2>&1 || rc-service nginx reload >/dev/null 2>&1; systemctl reload sing-box >/dev/null 2>&1 || rc-service sing-box reload >/dev/null 2>&1; true")
		log.WriteString(trimOut(out))
		if err != nil {
			return nil, log.String(), fmt.Errorf("证书安装失败")
		}
		_, _ = runWithTimeout(env, 60*time.Second, acme, "--upgrade", "--auto-upgrade")
	}
	if !pathExists(filepath.Join(saveDir, "fullchain.pem")) || !pathExists(filepath.Join(saveDir, "privkey.pem")) {
		return nil, log.String(), fmt.Errorf("证书文件没有生成")
	}
	_ = os.Chmod(filepath.Join(saveDir, "privkey.pem"), 0600)

	// 该域名没有任何 DNS 记录时补一条 A 记录（脚本同样的克制：不覆盖已有解析）
	if !strings.HasPrefix(domain, "*.") {
		if ip := hostPublicIP(); ip != "" {
			recs, err := cfDNSRecords(zone.ID, domain)
			if err == nil && len(recs) == 0 {
				_ = cfUpsertDNS(zone.ID, domain, ip, true)
				log.WriteString("\n已添加 DNS A 记录 → " + ip)
			}
		}
	}
	item := certByDomain(domain)
	if item == nil {
		item = &certItem{Domain: domain, Dir: saveDir,
			CertPath: filepath.Join(saveDir, "fullchain.pem"),
			KeyPath:  filepath.Join(saveDir, "privkey.pem"), HasKey: true}
	}
	return item, log.String(), nil
}

// ensureAcmeSh 保证 acme.sh 可用，返回路径。
func ensureAcmeSh(log *strings.Builder) (string, error) {
	lp := panelPaths()
	if pathExists(lp.AcmeSh) {
		return lp.AcmeSh, nil
	}
	if !haveCmd("curl") {
		return "", fmt.Errorf("本机没有 curl，装不了 acme.sh")
	}
	tmp := "/tmp/acme_install.sh"
	if out, err := adminExec("curl", "-fsSL", "--max-time", "120", "-o", tmp, "https://get.acme.sh"); err != nil {
		return "", fmt.Errorf("下载 acme.sh 失败：%s", trimOut(out))
	}
	blob, err := os.ReadFile(tmp)
	if err != nil || !strings.Contains(string(blob), "acme.sh") {
		return "", fmt.Errorf("下载到的 acme.sh 安装脚本不可信")
	}
	if out, err := adminExec("bash", tmp); err != nil {
		return "", fmt.Errorf("安装 acme.sh 失败：%s", trimOut(out))
	}
	_ = os.Remove(tmp)
	if !pathExists(lp.AcmeSh) {
		return "", fmt.Errorf("acme.sh 安装后找不到 %s", lp.AcmeSh)
	}
	if log != nil {
		log.WriteString("已安装 acme.sh\n")
	}
	return lp.AcmeSh, nil
}

// certPathsFor 返回某个域名证书在磁盘上的位置（按 CertDirs 顺序找第一个存在的）。
func certPathsFor(domain string) (string, string) {
	lp := panelPaths()
	for _, base := range lp.CertDirs {
		c := filepath.Join(base, domain, "fullchain.pem")
		k := filepath.Join(base, domain, "privkey.pem")
		if pathExists(c) && pathExists(k) {
			return c, k
		}
	}
	base := lp.CertDirs[0]
	return filepath.Join(base, domain, "fullchain.pem"), filepath.Join(base, domain, "privkey.pem")
}

// selfSignedCertPath 是网页建 TLS 入站时的默认证书。
func selfSignedCertPath() (string, string, error) { return ensureSelfSignedCert() }
