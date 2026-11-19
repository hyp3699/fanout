package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Cloudflare API。凭证与 sing-box-08.sh 共用：
//
//	Token      → /etc/sing-box/token（脚本本来就存这里，0600）
//	Global Key → /etc/sing-box/cf-global.json（脚本只放环境变量，网页这边落盘，0600）
//
// 网页永远不把完整凭证回传给前端，只给提示（前 4 位 + 长度）。

var (
	cfBaseURL = "https://api.cloudflare.com/client/v4"
	cfHTTP    = &http.Client{Timeout: 40 * time.Second}
)

// cfCreds 是一套 Cloudflare 凭证。
type cfCreds struct {
	Kind  string `json:"kind"` // token / global
	Token string `json:"-"`
	Email string `json:"email,omitempty"`
	Key   string `json:"-"`
}

// ---- 一次性凭证：开 CDN / 开隧道 / 申请证书时可以直接填 Token 或 Global Key，
// 只在这一次操作里用，不落盘，用完清掉（acme.sh 自己存的 SAVED_CF_* 也一并删）。

var (
	cfTempMu sync.Mutex   // 同一时间只跑一个一次性凭证操作
	cfTempRW sync.RWMutex // 保护 cfTemp
	cfTemp   *cfCreds
)

// cfOnce 是请求里带的一次性凭证字段，嵌进各接口的请求体。
type cfOnce struct {
	CFToken string `json:"cf_token"`
	CFEmail string `json:"cf_email"`
	CFKey   string `json:"cf_key"`
}

// creds 把请求里的字段变成凭证；都没填返回 nil（用已保存的）。
func (o cfOnce) creds() (*cfCreds, error) {
	tok := strings.TrimSpace(o.CFToken)
	email := strings.TrimSpace(o.CFEmail)
	key := strings.TrimSpace(o.CFKey)
	switch {
	case tok != "":
		if len(tok) < 20 {
			return nil, fmt.Errorf("Token 太短，看着不像 Cloudflare API Token")
		}
		return &cfCreds{Kind: "token", Token: tok}, nil
	case email != "" || key != "":
		if !strings.Contains(email, "@") || len(key) < 20 {
			return nil, fmt.Errorf("邮箱或 Global API Key 格式不对")
		}
		return &cfCreds{Kind: "global", Email: email, Key: key}, nil
	}
	return nil, nil
}

// withCFOnce 在 fn 执行期间临时使用 o 里的凭证（没填就直接用已保存的），结束后清掉。
func withCFOnce(o cfOnce, fn func() error) error {
	c, err := o.creds()
	if err != nil {
		return err
	}
	if c == nil {
		return fn()
	}
	cfTempMu.Lock()
	defer cfTempMu.Unlock()
	cfTempRW.Lock()
	cfTemp = c
	cfTempRW.Unlock()
	defer func() {
		cfTempRW.Lock()
		cfTemp = nil
		cfTempRW.Unlock()
		scrubAcmeSavedCF()
	}()
	return fn()
}

// scrubAcmeSavedCF 删掉 acme.sh 在 account.conf 里自动保存的 Cloudflare 凭证。
// 用一次性凭证申请的证书因此不能自动续期，到期前再申请一次即可。
func scrubAcmeSavedCF() {
	lp := panelPaths()
	if lp.AcmeSh == "" {
		return
	}
	conf := filepath.Join(filepath.Dir(lp.AcmeSh), "account.conf")
	blob, err := os.ReadFile(conf)
	if err != nil {
		return
	}
	var keep []string
	changed := false
	for _, line := range strings.Split(string(blob), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "SAVED_CF_") {
			changed = true
			continue
		}
		keep = append(keep, line)
	}
	if changed {
		_ = writeFileAtomic(conf, []byte(strings.Join(keep, "\n")), 0600)
	}
}

// loadCFCreds 读出当前凭证（一次性凭证优先）；没有时返回 nil。
func loadCFCreds() *cfCreds {
	cfTempRW.RLock()
	t := cfTemp
	cfTempRW.RUnlock()
	if t != nil {
		cp := *t
		return &cp
	}
	lp := panelPaths()
	if blob, err := os.ReadFile(lp.CFTokenFile); err == nil {
		if tok := strings.TrimSpace(string(blob)); tok != "" {
			return &cfCreds{Kind: "token", Token: tok}
		}
	}
	if blob, err := os.ReadFile(lp.CFGlobalFile); err == nil {
		// 单独用一个结构体读盘：cfCreds 里的 Key/Token 标了 json:"-"，直接解不出来
		var stored struct {
			Email string `json:"email"`
			Key   string `json:"key"`
		}
		if json.Unmarshal(blob, &stored) == nil && stored.Email != "" && stored.Key != "" {
			return &cfCreds{Kind: "global", Email: stored.Email, Key: stored.Key}
		}
	}
	return nil
}

// saveCFCreds 落盘。Token 与 Global Key 互斥：存一种就清掉另一种，
// 否则脚本那边会读到旧 Token，一直用错身份。
func saveCFCreds(c *cfCreds) error {
	lp := panelPaths()
	switch c.Kind {
	case "token":
		tok := strings.TrimSpace(c.Token)
		if len(tok) < 20 {
			return fmt.Errorf("Token 太短，看着不像 Cloudflare API Token")
		}
		if err := writeFileAtomic(lp.CFTokenFile, []byte(tok+"\n"), 0600); err != nil {
			return err
		}
		_ = os.Remove(lp.CFGlobalFile)
	case "global":
		email := strings.TrimSpace(c.Email)
		key := strings.TrimSpace(c.Key)
		if !strings.Contains(email, "@") || len(key) < 20 {
			return fmt.Errorf("邮箱或 Global API Key 格式不对")
		}
		blob, _ := json.MarshalIndent(map[string]string{"email": email, "key": key}, "", "  ")
		if err := writeFileAtomic(lp.CFGlobalFile, append(blob, '\n'), 0600); err != nil {
			return err
		}
		_ = os.Remove(lp.CFTokenFile)
	default:
		return fmt.Errorf("未知的凭证类型")
	}
	return nil
}

func clearCFCreds() {
	lp := panelPaths()
	_ = os.Remove(lp.CFTokenFile)
	_ = os.Remove(lp.CFGlobalFile)
}

// masked 给界面看的凭证提示。
func (c *cfCreds) masked() string {
	if c == nil {
		return ""
	}
	if c.Kind == "token" {
		return "Token " + maskSecret(c.Token)
	}
	return c.Email + " / Global Key " + maskSecret(c.Key)
}

func maskSecret(s string) string {
	if len(s) <= 8 {
		return "****"
	}
	return s[:4] + "…" + s[len(s)-4:]
}

// cfCall 发一次 API 请求，返回解析后的 JSON。Cloudflare 的错误信息原样带出来。
func cfCall(method, path string, payload any) (map[string]any, error) {
	creds := loadCFCreds()
	if creds == nil {
		return nil, fmt.Errorf("还没有配置 Cloudflare 凭证")
	}
	var body io.Reader
	if payload != nil {
		blob, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(blob)
	}
	req, err := http.NewRequest(method, cfBaseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if creds.Kind == "token" {
		req.Header.Set("Authorization", "Bearer "+creds.Token)
	} else {
		req.Header.Set("X-Auth-Email", creds.Email)
		req.Header.Set("X-Auth-Key", creds.Key)
	}
	resp, err := cfHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("连接 Cloudflare 失败: %w", err)
	}
	defer resp.Body.Close()
	blob, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	var out map[string]any
	_ = json.Unmarshal(blob, &out)
	if resp.StatusCode == http.StatusNotFound {
		// 404 有时是「还没有这条规则」的正常状态（回源规则），有时是真的不存在。
		// 两件事都记下来：调用方用 errors.Is 判断，报错时又能带上 Cloudflare 的原话。
		return out, fmt.Errorf("%w: %s", errNotFound, cfErrors(out))
	}
	if out == nil {
		return nil, fmt.Errorf("Cloudflare 返回了无法解析的内容（HTTP %d）", resp.StatusCode)
	}
	if ok, _ := out["success"].(bool); !ok {
		return out, fmt.Errorf("Cloudflare: %s", cfErrors(out))
	}
	return out, nil
}

// errNotFound 用来区分 404（例如还没有回源规则）和真正的失败。
var errNotFound = fmt.Errorf("not found")

func cfErrors(m map[string]any) string {
	errs, _ := m["errors"].([]any)
	var msgs []string
	for _, e := range errs {
		if o := obj(e); o != nil {
			if msg := str(o["message"]); msg != "" {
				msgs = append(msgs, msg)
			}
		}
	}
	if len(msgs) == 0 {
		return "未知错误"
	}
	return strings.Join(msgs, "; ")
}

func cfResult(m map[string]any) any { return m["result"] }

func cfResultList(m map[string]any) []any {
	list, _ := m["result"].([]any)
	return list
}

// cfVerify 验证凭证是否可用。账户令牌不支持 /user/tokens/verify，所以退一步查 zones。
func cfVerify() error {
	creds := loadCFCreds()
	if creds == nil {
		return fmt.Errorf("还没有配置 Cloudflare 凭证")
	}
	if creds.Kind == "token" {
		if _, err := cfCall("GET", "/zones?per_page=1", nil); err == nil {
			return nil
		}
	}
	if _, err := cfCall("GET", "/zones?per_page=1", nil); err != nil {
		return fmt.Errorf("凭证无效: %w", err)
	}
	return nil
}

// cfZone 是 Cloudflare 上的一个域名。
type cfZone struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Account string `json:"account"`
}

func cfZones() ([]cfZone, error) {
	m, err := cfCall("GET", "/zones?per_page=50", nil)
	if err != nil {
		return nil, err
	}
	var out []cfZone
	for _, e := range cfResultList(m) {
		o := obj(e)
		if o == nil {
			continue
		}
		acct := ""
		if a := obj(o["account"]); a != nil {
			acct = str(a["id"])
		}
		out = append(out, cfZone{ID: str(o["id"]), Name: str(o["name"]), Status: str(o["status"]), Account: acct})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// cfFindZone 找出域名所属的 Zone：取最长的匹配后缀，与脚本 cf_find_zone 一致。
func cfFindZone(domain string) (cfZone, error) {
	zones, err := cfZones()
	if err != nil {
		return cfZone{}, err
	}
	best := cfZone{}
	for _, z := range zones {
		if domain == z.Name || strings.HasSuffix(domain, "."+z.Name) {
			if len(z.Name) > len(best.Name) {
				best = z
			}
		}
	}
	if best.ID == "" {
		return cfZone{}, fmt.Errorf("%s 不在当前 Cloudflare 账户下", domain)
	}
	return best, nil
}

// cfUpsertDNS 新建或覆盖 A/AAAA 记录（开代理）。
func cfUpsertDNS(zoneID, domain, ip string, proxied bool) error {
	typ := "A"
	if strings.Contains(ip, ":") {
		typ = "AAAA"
	}
	clean := strings.Trim(ip, "[]")
	existing, err := cfCall("GET", "/zones/"+zoneID+"/dns_records?type="+typ+"&name="+domain, nil)
	var recordID string
	if err == nil {
		if list := cfResultList(existing); len(list) > 0 {
			recordID = str(obj(list[0])["id"])
		}
	}
	payload := map[string]any{"type": typ, "name": domain, "content": clean, "proxied": proxied, "ttl": 1}
	if recordID != "" {
		_, err = cfCall("PUT", "/zones/"+zoneID+"/dns_records/"+recordID, payload)
		return err
	}
	_, err = cfCall("POST", "/zones/"+zoneID+"/dns_records", payload)
	return err
}

// cfDNSRecord 是一条 DNS 记录。
type cfDNSRecord struct {
	ID      string
	Type    string
	Name    string
	Content string
}

func cfDNSRecords(zoneID, name string) ([]cfDNSRecord, error) {
	m, err := cfCall("GET", "/zones/"+zoneID+"/dns_records?name="+name, nil)
	if err != nil {
		return nil, err
	}
	var out []cfDNSRecord
	for _, e := range cfResultList(m) {
		o := obj(e)
		if o == nil {
			continue
		}
		out = append(out, cfDNSRecord{ID: str(o["id"]), Type: str(o["type"]),
			Name: str(o["name"]), Content: str(o["content"])})
	}
	return out, nil
}

// cfDeleteDNS 只删 A / AAAA / CNAME，不碰同名 MX / TXT。
func cfDeleteDNS(zoneID, domain string) error {
	recs, err := cfDNSRecords(zoneID, domain)
	if err != nil {
		return err
	}
	var firstErr error
	for _, r := range recs {
		switch r.Type {
		case "A", "AAAA", "CNAME":
			if _, err := cfCall("DELETE", "/zones/"+zoneID+"/dns_records/"+r.ID, nil); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// cfSetSSL 设置 Zone 的 SSL 模式。
func cfSetSSL(zoneID, mode string) error {
	_, err := cfCall("PATCH", "/zones/"+zoneID+"/settings/ssl", map[string]any{"value": mode})
	return err
}

// cfSSLModes 是界面上可选的值。
var cfSSLModes = []string{"flexible", "full", "strict"}

// cfGetOriginRules 读回源规则；404 表示还没建过，当作空列表（与脚本一致）。
func cfGetOriginRules(zoneID string) ([]any, error) {
	m, err := cfCall("GET", "/zones/"+zoneID+"/rulesets/phases/http_request_origin/entrypoint", nil)
	if errors.Is(err, errNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	result := obj(m["result"])
	if result == nil {
		return nil, nil
	}
	rules, _ := result["rules"].([]any)
	return rules, nil
}

// cfPutOriginRules 整份写回源规则。
// 超过 10 条时先清掉最老的 Auto_Script: 规则（Cloudflare 的上限），
// 与脚本 cf_put_origin_rules 的策略一样。
func cfPutOriginRules(zoneID string, rules []any) error {
	const maxRules = 10
	if len(rules) > maxRules {
		var auto, other []any
		for _, r := range rules {
			o := obj(r)
			if _, ok := o["description"]; ok {
				if strings.HasPrefix(str(o["description"]), "Auto_Script:") {
					auto = append(auto, r)
					continue
				}
			}
			other = append(other, r)
		}
		keep := len(auto) - (len(rules) - maxRules)
		if keep < 0 {
			keep = 0
		}
		rules = append(other, auto[len(auto)-keep:]...)
	}
	_, err := cfCall("PUT", "/zones/"+zoneID+"/rulesets/phases/http_request_origin/entrypoint",
		map[string]any{"rules": rules})
	return err
}

// cfSetOriginPort 给某个域名加一条回源端口规则（CDN 回源用）。
func cfSetOriginPort(zoneID, domain string, port int) error {
	const pfx = "Auto_Script:"
	rules, err := cfGetOriginRules(zoneID)
	if err != nil {
		return err
	}
	var kept []any
	for _, r := range rules {
		o := obj(r)
		desc := str(o["description"])
		expr := strings.ToLower(str(o["expression"]))
		if strings.HasPrefix(desc, pfx) && strings.Contains(expr, `http.host eq "`+strings.ToLower(domain)+`"`) {
			continue // 这条是同一个域名旧的回源规则，替换掉
		}
		kept = append(kept, r)
	}
	kept = append(kept, map[string]any{
		"description": pfx + "VLESS_WSTLS_CDN_" + domain,
		"enabled":     true,
		"expression":  `(http.host eq "` + domain + `")`,
		"action":      "route",
		"action_parameters": map[string]any{
			"origin": map[string]any{"port": port},
		},
	})
	return cfPutOriginRules(zoneID, kept)
}

// cfRemoveOriginPort 删掉某个域名自己的回源规则。
func cfRemoveOriginPort(zoneID, domain string) error {
	rules, err := cfGetOriginRules(zoneID)
	if err != nil {
		return err
	}
	var kept []any
	for _, r := range rules {
		o := obj(r)
		desc := str(o["description"])
		if strings.HasPrefix(desc, "Auto_Script:") && strings.HasSuffix(desc, "_"+domain) {
			continue
		}
		kept = append(kept, r)
	}
	return cfPutOriginRules(zoneID, kept)
}

// cfDeleteCDN 删掉一个 CDN 域名的回源规则与 DNS（脚本 cf_remove_cdn_rules）。
func cfDeleteCDN(domain string) error {
	if !validDomain(domain) || loadCFCreds() == nil {
		return nil
	}
	zone, err := cfFindZone(domain)
	if err != nil {
		return err
	}
	if err := cfRemoveOriginPort(zone.ID, domain); err != nil {
		return err
	}
	return cfDeleteDNS(zone.ID, domain)
}

// ---- Tunnel ----

// cfTunnel 是一条 Cloudflare Tunnel。
type cfTunnel struct {
	ID          string
	Name        string
	Status      string
	Connections []string // 已连上来的出口 IP
}

func cfAccountID() (string, error) {
	zones, err := cfZones()
	if err != nil {
		return "", err
	}
	for _, z := range zones {
		if z.Account != "" {
			return z.Account, nil
		}
	}
	return "", fmt.Errorf("没有可用的 Cloudflare 账户")
}

func cfListTunnels(account string) ([]cfTunnel, error) {
	m, err := cfCall("GET", "/accounts/"+account+"/cfd_tunnel?per_page=100", nil)
	if err != nil {
		return nil, err
	}
	var out []cfTunnel
	for _, e := range cfResultList(m) {
		o := obj(e)
		if o == nil {
			continue
		}
		t := cfTunnel{ID: str(o["id"]), Name: str(o["name"]), Status: str(o["status"])}
		if conns, ok := o["connections"].([]any); ok {
			for _, c := range conns {
				if co := obj(c); co != nil {
					if ip := str(co["origin_ip"]); ip != "" {
						t.Connections = append(t.Connections, ip)
					}
				}
			}
		}
		out = append(out, t)
	}
	return out, nil
}

func cfCreateTunnel(account, name string) (cfTunnel, error) {
	m, err := cfCall("POST", "/accounts/"+account+"/cfd_tunnel",
		map[string]any{"name": name, "config_src": "cloudflare"})
	if err != nil {
		return cfTunnel{}, err
	}
	o := obj(m["result"])
	if o == nil {
		return cfTunnel{}, fmt.Errorf("创建 Tunnel 失败：返回里没有结果")
	}
	return cfTunnel{ID: str(o["id"]), Name: str(o["name"]), Status: str(o["status"])}, nil
}

// cfTunnelToken 取 Tunnel 的连接 Token（写进 cloudflared 入站）。
func cfTunnelToken(account, id string) (string, error) {
	m, err := cfCall("GET", "/accounts/"+account+"/cfd_tunnel/"+id+"/token", nil)
	if err != nil {
		return "", err
	}
	tok := str(m["result"])
	if tok == "" {
		return "", fmt.Errorf("Tunnel Token 为空")
	}
	return tok, nil
}

// cfTunnelIngress 取现有 ingress 规则。
func cfTunnelIngress(account, id string) ([]any, error) {
	m, err := cfCall("GET", "/accounts/"+account+"/cfd_tunnel/"+id+"/configurations", nil)
	if err != nil {
		return nil, err
	}
	cfg := obj(m["result"])
	if cfg == nil {
		return nil, nil
	}
	inner := obj(cfg["config"])
	if inner == nil {
		return nil, nil
	}
	ingress, _ := inner["ingress"].([]any)
	return ingress, nil
}

// cfPutTunnelIngress 覆盖 ingress 规则。
func cfPutTunnelIngress(account, id string, ingress []any) error {
	_, err := cfCall("PUT", "/accounts/"+account+"/cfd_tunnel/"+id+"/configurations",
		map[string]any{"config": map[string]any{"ingress": ingress}})
	return err
}

// cfUpsertCNAME 让域名 CNAME 到 Tunnel。
func cfUpsertCNAME(zoneID, hostname, content string) error {
	recs, err := cfDNSRecords(zoneID, hostname)
	if err != nil {
		return err
	}
	payload := map[string]any{"type": "CNAME", "name": hostname, "content": content, "proxied": true, "ttl": 1}
	for _, r := range recs {
		if r.Type == "CNAME" {
			_, err := cfCall("PUT", "/zones/"+zoneID+"/dns_records/"+r.ID, payload)
			return err
		}
		return fmt.Errorf("%s 已有 %s 记录，无法改成 Tunnel CNAME", hostname, r.Type)
	}
	_, err = cfCall("POST", "/zones/"+zoneID+"/dns_records", payload)
	return err
}

// tunnelRoute 是一条「域名+路径 → 本地端口」的隧道路由。
type tunnelRoute struct {
	Hostname string
	Path     string
	Port     int
	HTTPS    bool // 入站自己带 TLS 时用 https 回源
}

// cloudflaredInbound 写 /etc/sing-box/conf/cloudflared.json（sing-box 的 cloudflared 入站）。
func writeCloudflaredInbound(token string) error {
	lp := panelPaths()
	doc := map[string]any{
		"inbounds": []any{map[string]any{
			"type": "cloudflared", "tag": "cloudflared-in", "token": token,
			"ha_connections": 4, "protocol": "quic", "post_quantum": true,
			"edge_ip_version": 0, "datagram_version": "v3",
		}},
	}
	blob, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(lp.ConfDir, "cloudflared.json")
	return writeVerified(path, append(blob, '\n'), 0600, singboxCheck)
}

// cfSetupTunnel 配好固定隧道：找到本机的 Tunnel（没有就建）→ 写 cloudflared 入站
// → 合并 ingress 路由 → CNAME。返回选的 Tunnel 与写进去的路由。
func cfSetupTunnel(routes []tunnelRoute, zoneName, prefix string) (cfTunnel, string, error) {
	if len(routes) == 0 {
		return cfTunnel{}, "", fmt.Errorf("没有要添加的隧道路由")
	}
	for _, r := range routes {
		if !validPort(r.Port) {
			return cfTunnel{}, "", fmt.Errorf("端口无效：%d", r.Port)
		}
	}
	account, err := cfAccountID()
	if err != nil {
		return cfTunnel{}, "", err
	}
	// 先把域名算清楚：Zone 不对就别往配置目录里写 cloudflared 入站
	zone, err := cfFindZone(zoneName)
	if err != nil {
		return cfTunnel{}, "", err
	}
	hostname := strings.Trim(strings.TrimSpace(prefix), ".")
	switch {
	case hostname == "":
		hostname = zone.Name
	case hostname == zone.Name, strings.HasSuffix(hostname, "."+zone.Name):
	default:
		hostname = hostname + "." + zone.Name
	}
	if !validDomain(hostname) {
		return cfTunnel{}, "", fmt.Errorf("隧道域名无效：%s", hostname)
	}

	tunnels, err := cfListTunnels(account)
	if err != nil {
		return cfTunnel{}, "", err
	}
	ip := hostPublicIP()
	var picked *cfTunnel
	for i := range tunnels {
		for _, c := range tunnels[i].Connections {
			if ip != "" && c == ip {
				picked = &tunnels[i]
				break
			}
		}
		if picked != nil {
			break
		}
	}
	if picked == nil {
		name := "fanout-tunnel"
		if host, _ := os.Hostname(); host != "" {
			name = "sb-" + host
		}
		t, err := cfCreateTunnel(account, name)
		if err != nil {
			return cfTunnel{}, "", err
		}
		picked = &t
	}
	token, err := cfTunnelToken(account, picked.ID)
	if err != nil {
		return cfTunnel{}, "", err
	}
	if err := writeCloudflaredInbound(token); err != nil {
		return cfTunnel{}, "", fmt.Errorf("写 cloudflared 入站失败: %w", err)
	}

	ingress, err := cfTunnelIngress(account, picked.ID)
	if err != nil {
		return cfTunnel{}, "", err
	}
	for _, r := range routes {
		p := r.Path
		if p == "" {
			p = "/"
		}
		if !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
		for _, e := range ingress {
			o := obj(e)
			if o == nil {
				continue
			}
			if str(o["hostname"]) == hostname && str(o["path"]) == p {
				return cfTunnel{}, "", fmt.Errorf("路由已存在：%s%s", hostname, p)
			}
		}
	}

	var merged []any
	for _, e := range ingress {
		o := obj(e)
		if o != nil && str(o["service"]) == "http_status:404" {
			continue
		}
		merged = append(merged, e)
	}
	for _, r := range routes {
		p := r.Path
		if p == "" {
			p = "/"
		}
		if !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
		scheme := "http"
		if r.HTTPS {
			scheme = "https"
		}
		rule := map[string]any{
			"hostname": hostname,
			"path":     p,
			"service":  fmt.Sprintf("%s://127.0.0.1:%d", scheme, r.Port),
		}
		if r.HTTPS {
			rule["originRequest"] = map[string]any{"noTLSVerify": true, "http2Origin": true}
		}
		merged = append(merged, rule)
	}
	merged = append(merged, map[string]any{"service": "http_status:404"})

	if len(merged) > 500 {
		return cfTunnel{}, "", fmt.Errorf("Tunnel 路由太多（%d），先清理一些", len(merged))
	}
	if err := cfPutTunnelIngress(account, picked.ID, merged); err != nil {
		return cfTunnel{}, "", fmt.Errorf("下发 Tunnel 路由失败: %w", err)
	}
	if err := cfUpsertCNAME(zone.ID, hostname, picked.ID+".cfargotunnel.com"); err != nil {
		return cfTunnel{}, "", fmt.Errorf("设置 Tunnel CNAME 失败: %w", err)
	}
	return *picked, hostname, nil
}

// cfOriginCA 申请 Cloudflare Origin CA 证书（15 年），返回证书与私钥 PEM。
func cfOriginCA(domain string) (string, string, error) {
	if !validDomain(domain) {
		return "", "", fmt.Errorf("域名格式无效")
	}
	m, err := cfCall("POST", "/certificates", map[string]any{
		"hostnames":          []string{domain},
		"requested_validity": 5475,
		"request_type":       "origin-ecc",
		"csr":                "",
	})
	if err != nil {
		return "", "", err
	}
	o := obj(m["result"])
	if o == nil {
		return "", "", fmt.Errorf("Cloudflare 没有返回证书")
	}
	cert := str(o["certificate"])
	key := str(o["private_key"])
	if cert == "" || key == "" {
		return "", "", fmt.Errorf("Cloudflare 返回的证书为空")
	}
	return cert, key, nil
}
