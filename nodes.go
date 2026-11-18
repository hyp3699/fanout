package main

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// 这里处理 sing-box-08.sh 建的入站：读写 /etc/sing-box/conf/{类型}-{编号}.json。
//
// 与脚本的分工保持不变：fanout 自己的 fanout-outbounds.json / fanout-route.json 不碰，
// 别的文件按脚本的约定原样读写。每次写盘都先 `sing-box check -C`，不过就回滚。

// nodeUser 是入站里的一个用户，字段跟 sing-box 的 users[] 对齐。
type nodeUser struct {
	Name     string `json:"name"`
	UUID     string `json:"uuid,omitempty"`
	Password string `json:"password,omitempty"`
	Username string `json:"username,omitempty"`
	Flow     string `json:"flow,omitempty"`
}

// node 是配置目录里的一个入站（脚本一个文件一条入站）。
type node struct {
	File     string     `json:"file"`     // vless-reality-1.json
	Kind     string     `json:"kind"`     // 文件名里的类型，如 vless-reality
	Number   int        `json:"number"`   // 文件名里的编号
	Tag      string     `json:"tag"`      // 入站 tag
	Protocol string     `json:"protocol"` // sing-box type：vless / hysteria2 / ...
	Port     int        `json:"port"`
	Listen   string     `json:"listen"`
	Users    []nodeUser `json:"users"`

	Transport string `json:"transport,omitempty"` // ws / http / grpc / xhttp
	Path      string `json:"path,omitempty"`
	Host      string `json:"host,omitempty"`
	TLS       bool   `json:"tls"`
	Reality   bool   `json:"reality"`
	SNI       string `json:"sni,omitempty"`
	Handshake string `json:"handshake,omitempty"`
	CertPath  string `json:"cert_path,omitempty"`
	KeyPath   string `json:"key_path,omitempty"`
	Method    string `json:"method,omitempty"`

	// 运行态，读取时现查
	Quick       bool            `json:"quick"`        // 临时隧道专用入站
	QuickUnit   string          `json:"quick_unit"`   // cf-quick-… 服务名
	QuickOn     bool            `json:"quick_on"`     // 隧道进程是否在跑
	QuickDomain string          `json:"quick_domain"` // 当前 trycloudflare 域名
	CDNDomains  []string        `json:"cdn_domains"`  // 链接里带 _cdn 的域名
	PortOK      map[string]bool `json:"port_ok"`      // tcp/udp 是否已放行
	HopOn       bool            `json:"hop_on"`
	ObfsOn      bool            `json:"obfs_on"`
	Remark      string          `json:"remark,omitempty"`
	Links       []string        `json:"links,omitempty"`
}

// nodeRemarkEmoji 是链接备注前的国旗。取不到国家时用地球，
// 脚本 generate_vars 的做法一样。单测里可以固定成空串。
var nodeRemarkEmoji = "🌐"

// nodeKinds 是脚本支持的 14 种普通入站，顺序与选项 9 的菜单一致。
var nodeKinds = []string{
	"vless-reality", "hysteria2", "tuic", "http-reality", "grpc-reality",
	"anytls", "socks5", "xhttp-reality", "vless-xhttp", "xhttp-udp-tls",
	"vless-tcp-tls", "naiveproxy", "vmess-ws", "vless-ws",
}

// nodeKindInfo 描述一种入站：文件前缀、tag 前缀、协议、默认自签证书。
type nodeKindInfo struct {
	Kind     string // 菜单里的类型（也是文件名前缀）
	TagBase  string // tag 前缀
	UserBase string // 默认用户名前缀
	Proto    string // sing-box type
	Net      string // tcp / udp
	TLS      string // none / self / domain / optional
}

var nodeKindInfos = map[string]nodeKindInfo{
	"vless-reality": {"vless-reality", "vless-reality", "vless-reality-user", "vless", "tcp", "reality"},
	"hysteria2":     {"hysteria2", "hysteria2", "hysteria2-user", "hysteria2", "udp", "self"},
	"tuic":          {"tuic", "tuic", "tuic-user", "tuic", "udp", "self"},
	"http-reality":  {"http-reality", "http-reality", "http-reality-user", "vless", "tcp", "reality"},
	"grpc-reality":  {"grpc-reality", "grpc-reality", "grpc-reality-user", "vless", "tcp", "reality"},
	"anytls":        {"anytls", "anytls", "anytls-user", "anytls", "tcp", "self"},
	"socks5":        {"socks5", "socks", "socks", "socks", "tcp", "none"},
	"xhttp-reality": {"xhttp-reality", "xhttp-reality", "xhttp-reality-user", "vless", "tcp", "reality"},
	"vless-xhttp":   {"vless-xhttp", "vless-xhttp", "vless-xhttp-user", "vless", "tcp", "optional"},
	"xhttp-udp-tls": {"xhttp-udp-tls", "vless-xhttp-udptls", "vless-xhttp-udptls-user", "vless", "tcp", "domain"},
	"vless-tcp-tls": {"vless-tcp-tls", "vless-tcp-tls", "vless-tcp-tls-user", "vless", "tcp", "domain"},
	"naiveproxy":    {"naiveproxy", "naive", "naive-user", "naive", "tcp", "domain"},
	"vmess-ws":      {"vmess-ws", "vmess-ws", "vmess-ws-user", "vmess", "tcp", "optional"},
	"vless-ws":      {"vless-ws", "vless-ws", "vless-ws-user", "vless", "tcp", "optional"},
}

// knownNodeFile 判断文件名是不是脚本会建的入站文件（config.json 等不算）。
func knownNodeFile(name string) bool {
	if !validNodeFile(name) {
		return false
	}
	kind, _, ok := splitNodeFile(name)
	if !ok {
		return false
	}
	if _, ok := nodeKindInfos[kind]; ok {
		return true
	}
	// trojan-ws 之类历史类型也允许管理，只要不是 fanout / 脚本的公共文件
	switch kind {
	case "trojan-ws", "ss", "shadowsocks", "vmess", "vless":
		return true
	}
	return false
}

// listNodes 列出配置目录里脚本建的入站，附上端口放行、临时隧道、CDN 等运行态。
func listNodes() ([]*node, error) {
	lp := panelPaths()
	entries, err := os.ReadDir(lp.ConfDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []*node{}, nil
		}
		return nil, fmt.Errorf("读取配置目录失败: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || isManagedFile(e.Name()) {
			continue
		}
		if !knownNodeFile(e.Name()) {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	out := make([]*node, 0, len(names))
	for _, name := range names {
		doc, err := readNodeDoc(name)
		if err != nil {
			continue
		}
		n := describeNode(name, doc)
		if n == nil {
			continue
		}
		out = append(out, n)
	}
	fillNodeRuntime(out)
	return out, nil
}

// readNodeDoc 读一个入站文件；文件名先过校验，挡住 ../。
func readNodeDoc(file string) (map[string]any, error) {
	if !validNodeFile(file) {
		return nil, fmt.Errorf("非法配置文件名 %q", file)
	}
	lp := panelPaths()
	blob, err := os.ReadFile(filepath.Join(lp.ConfDir, file))
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := json.Unmarshal(blob, &doc); err != nil {
		return nil, fmt.Errorf("%s 解析失败: %w", file, err)
	}
	return doc, nil
}

// describeNode 把入站 JSON 摊平成界面用的结构。
func describeNode(file string, doc map[string]any) *node {
	ib := firstInbound(doc)
	if ib == nil {
		return nil
	}
	kind, number, _ := splitNodeFile(file)
	n := &node{
		File:     file,
		Kind:     kind,
		Number:   number,
		Tag:      str(ib["tag"]),
		Protocol: str(ib["type"]),
		Port:     num(ib["listen_port"]),
		Listen:   str(ib["listen"]),
		Method:   str(ib["method"]),
		PortOK:   map[string]bool{},
	}
	for _, u := range inboundUserMaps(ib) {
		nu := nodeUser{
			Name:     str(u["name"]),
			UUID:     str(u["uuid"]),
			Password: str(u["password"]),
			Username: str(u["username"]),
			Flow:     str(u["flow"]),
		}
		if nu.Name == "" && nu.UUID == "" && nu.Password == "" {
			continue
		}
		n.Users = append(n.Users, nu)
	}
	if tr := obj(ib["transport"]); tr != nil {
		n.Transport = str(tr["type"])
		n.Path = str(tr["path"])
		n.Host = str(tr["host"])
		if n.Host == "" {
			if h := obj(tr["headers"]); h != nil {
				if v := strList(h["Host"]); len(v) > 0 {
					n.Host = v[0]
				}
			}
		}
	}
	if tls := obj(ib["tls"]); tls != nil && tls["enabled"] == true {
		n.TLS = true
		n.SNI = str(tls["server_name"])
		n.CertPath = str(tls["certificate_path"])
		n.KeyPath = str(tls["key_path"])
		if r := obj(tls["reality"]); r != nil && r["enabled"] == true {
			n.Reality = true
			if hs := obj(r["handshake"]); hs != nil {
				n.Handshake = str(hs["server"])
			}
			if n.SNI == "" {
				n.SNI = n.Handshake
			}
		}
	}
	n.Remark = nodeRemark(n)
	return n
}

func nodeRemark(n *node) string {
	switch {
	case n.Reality:
		return n.SNI
	case n.TLS && n.SNI != "":
		return n.SNI
	}
	return ""
}

// fillNodeRuntime 补上运行态：端口放行、临时隧道、CDN、hy2 跳跃/混淆。
func fillNodeRuntime(nodes []*node) {
	lp := panelPaths()
	chain := nftChain("inet filter script_input")
	for _, n := range nodes {
		if n.Port > 0 {
			n.PortOK["tcp"] = nftPortOpen(chain, n.Port, "tcp")
			n.PortOK["udp"] = nftPortOpen(chain, n.Port, "udp")
		}
		unit := quickUnitName(n.Kind, n.Number)
		n.QuickUnit = unit
		dom, on, exists := quickTunnelState(unit)
		n.QuickOn = on
		n.QuickDomain = dom
		if exists && (n.Listen == "127.0.0.1" || pathExists(filepath.Join(lp.QuickDir, fmt.Sprintf("%s-%d.dedicated", n.Kind, n.Number)))) {
			n.Quick = true
		}
		n.CDNDomains = nodeCDNDomains(n)
		if n.Kind == "hysteria2" {
			n.HopOn = hy2HopEnabled(n.Number)
			n.ObfsOn = hy2ObfsEnabled(n)
		}
		n.Links = readNodeLinks(n)
	}
}

// ---- JSON 小工具 ----

// firstInbound 取文件里的第一条入站。脚本一个文件只写一条。
func firstInbound(doc map[string]any) map[string]any {
	inbounds, _ := doc["inbounds"].([]any)
	if len(inbounds) == 0 {
		return nil
	}
	return obj(inbounds[0])
}

func inboundUserMaps(ib map[string]any) []map[string]any {
	users, _ := ib["users"].([]any)
	out := make([]map[string]any, 0, len(users))
	for _, u := range users {
		if m := obj(u); m != nil {
			out = append(out, m)
		}
	}
	return out
}

// nodeExists 判断文件在不在。
func nodeExists(file string) bool {
	if !validNodeFile(file) {
		return false
	}
	return pathExists(filepath.Join(panelPaths().ConfDir, file))
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// nextNodeNumber 找该类型下一个没占用的编号（与脚本 get_next_inbound_number 一致）。
func nextNodeNumber(kind string) int {
	lp := panelPaths()
	for i := 1; i < 1000; i++ {
		if !pathExists(filepath.Join(lp.ConfDir, fmt.Sprintf("%s-%d.json", kind, i))) {
			return i
		}
	}
	return 0
}

// saveNodeDoc 写回入站文件：先 check 再落盘，失败自动回滚。
func saveNodeDoc(file string, doc map[string]any) error {
	lp := panelPaths()
	blob, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	blob = append(blob, '\n')
	return writeVerified(filepath.Join(lp.ConfDir, file), blob, 0600, singboxCheck)
}

// singboxCheck 用 sing-box 自己的校验器检查整个配置目录。
func singboxCheck() error {
	lp := panelPaths()
	if !haveCmd(lp.Bin) {
		return fmt.Errorf("找不到 sing-box 可执行文件 %s", lp.Bin)
	}
	out, err := adminExec(lp.Bin, "check", "-C", lp.ConfDir)
	if err != nil {
		return fmt.Errorf("sing-box 配置校验失败：%s", trimOut(out))
	}
	return nil
}

// applySingboxReload 让 sing-box 用上新配置。托管模式（没有服务）时提示重启 fanout。
func applySingboxReload() error {
	lp := panelPaths()
	switch detectService(singbox.Service) {
	case "systemd":
		if out, err := adminExec("systemctl", "reload", singbox.Service); err == nil {
			return nil
		} else if out2, err2 := adminExec("systemctl", "restart", singbox.Service); err2 == nil {
			return nil
		} else {
			return fmt.Errorf("重载 sing-box 失败：%s%s", trimOut(out), trimOut(out2))
		}
	case "openrc":
		if out, err := adminExec("rc-service", singbox.Service, "reload"); err == nil {
			return nil
		} else if _, err2 := adminExec("rc-service", singbox.Service, "restart"); err2 == nil {
			return nil
		} else {
			return fmt.Errorf("重载 sing-box 失败：%s", trimOut(out))
		}
	}
	// fanout 自己托管 sing-box 进程时，入站变化要重启一次让它重新读目录
	if lp.Bin != "" {
		if p, err := os.FindProcess(0); err == nil {
			_ = p
		}
	}
	// 没有服务时是 fanout 自己拉着 sing-box：配置已经写好并通过 check，
	// 进程要等下次 fanout 重启才重新读目录，这里不算失败。
	log.Printf("本机没有 sing-box 服务；配置已写入并通过校验，重启 fanout 后生效")
	return nil
}

// ---- 创建入站 ----

// nodeCreateSpec 是网页上「添加入站」的表单。
type nodeCreateSpec struct {
	Kind     string `json:"kind"`
	Port     int    `json:"port"`      // 0 = 自动挑一个空闲端口
	CertMode string `json:"cert_mode"` // self / domain（TLS 类入站）
	Domain   string `json:"domain"`    // CertMode=domain 时用它的证书
	AddTLS   bool   `json:"add_tls"`   // ws / xhttp 类可选加证书
	SNI      string `json:"sni"`       // Reality 伪装域名
}

// realitySites 是脚本内置的伪装域名，第 1 个是默认值。
var realitySites = []string{
	"www.joom.com", "www.stengg.com", "www.wedgehr.com", "www.cerebrium.ai",
	"www.nazhumi.com", "addons.mozilla.org", "www.iij.ad.jp",
}

// defaultRealitySNI 返回脚本默认伪装域名。
func defaultRealitySNI() string { return realitySites[6] }

// buildInboundDoc 按脚本的模板生成一份入站配置。
// 返回的 user 是主用户（脚本里第一个用户），链接就用它。
func buildInboundDoc(spec nodeCreateSpec, number, port int) (map[string]any, nodeUser, error) {
	info, ok := nodeKindInfos[spec.Kind]
	if !ok {
		return nil, nodeUser{}, fmt.Errorf("不支持的入站类型 %q", spec.Kind)
	}
	uuid := newUUID()
	password := randomAPIKey(24)
	shortID := randomHex(6)
	userName := fmt.Sprintf("%s%d", info.UserBase, number)
	tag := fmt.Sprintf("%s-%d", info.TagBase, number)

	ib := map[string]any{
		"type":        info.Proto,
		"tag":         tag,
		"listen":      "::",
		"listen_port": port,
	}
	user := nodeUser{Name: userName, UUID: uuid, Password: password}

	var certPath, keyPath, sni string
	useTLS := false
	switch info.TLS {
	case "self":
		c, k, err := ensureSelfSignedCert()
		if err != nil {
			return nil, user, err
		}
		certPath, keyPath, useTLS = c, k, true
	case "domain":
		if !validDomain(spec.Domain) {
			return nil, user, fmt.Errorf("这类入站必须有域名证书，请先在「证书」里申请，或选择一个已有证书的域名")
		}
		c, k := certPathsFor(spec.Domain)
		if !pathExists(c) || !pathExists(k) {
			return nil, user, fmt.Errorf("没有找到 %s 的证书（%s），请先在「证书」里申请", spec.Domain, c)
		}
		certPath, keyPath, sni, useTLS = c, k, spec.Domain, true
	case "reality":
		sni = strings.TrimSpace(spec.SNI)
		if sni == "" {
			sni = defaultRealitySNI()
		}
		if !validDomain(sni) {
			return nil, user, fmt.Errorf("Reality 伪装域名格式无效")
		}
	case "optional":
		if spec.AddTLS {
			if !validDomain(spec.Domain) {
				return nil, user, fmt.Errorf("勾选了添加 TLS，就要选择一个证书域名")
			}
			c, k := certPathsFor(spec.Domain)
			if !pathExists(c) || !pathExists(k) {
				return nil, user, fmt.Errorf("没有找到 %s 的证书，请先在「证书」里申请", spec.Domain)
			}
			certPath, keyPath, sni, useTLS = c, k, spec.Domain, true
		}
	}

	switch spec.Kind {
	case "vless-reality", "http-reality", "grpc-reality", "xhttp-reality":
		priv, _, err := newRealityKeyPair()
		if err != nil {
			return nil, user, err
		}
		user.Flow = ""
		if spec.Kind == "vless-reality" {
			user.Flow = "xtls-rprx-vision"
		}
		ib["users"] = []any{userMap(user)}
		ib["tls"] = map[string]any{
			"enabled":     true,
			"server_name": sni,
			"reality": map[string]any{
				"enabled": true,
				"handshake": map[string]any{
					"server":      sni,
					"server_port": 443,
				},
				"private_key": priv,
				"short_id":    []any{shortID},
			},
		}
		switch spec.Kind {
		case "http-reality":
			ib["transport"] = map[string]any{"type": "http"}
			ib["multiplex"] = map[string]any{
				"enabled": true, "padding": true,
				"brutal": map[string]any{"enabled": true, "up_mbps": 1000, "down_mbps": 1000},
			}
		case "grpc-reality":
			ib["transport"] = map[string]any{"type": "grpc", "service_name": "grpc"}
			ib["multiplex"] = map[string]any{
				"enabled": true, "padding": true,
				"brutal": map[string]any{"enabled": true, "up_mbps": 200, "down_mbps": 200},
			}
		case "xhttp-reality":
			ib["transport"] = map[string]any{"type": "xhttp", "mode": "auto", "path": "/sssisuiu-xhttp"}
		}
	case "hysteria2":
		ib["bbr_profile"] = "standard"
		ib["ignore_client_bandwidth"] = false
		ib["masquerade"] = "https://bing.com"
		ib["users"] = []any{map[string]any{"name": user.Name, "password": password}}
		ib["tls"] = map[string]any{
			"enabled": true, "alpn": []any{"h3"},
			"min_version": "1.3", "max_version": "1.3",
			"certificate_path": certPath, "key_path": keyPath,
		}
	case "tuic":
		ib["users"] = []any{map[string]any{"name": user.Name, "uuid": uuid, "password": password}}
		ib["congestion_control"] = "bbr"
		ib["tls"] = map[string]any{
			"enabled": true, "alpn": []any{"h3"},
			"certificate_path": certPath, "key_path": keyPath,
		}
	case "anytls":
		ib["users"] = []any{map[string]any{"name": user.Name, "password": password}}
		ib["padding_scheme"] = []any{
			"stop=6", "0=30-50", "1=80-400",
			"2=400-500,c,500-1000,c,500-1000", "3=9-9,500-1000",
			"4=500-1000", "5=500-1000",
		}
		ib["tls"] = map[string]any{"enabled": true, "certificate_path": certPath, "key_path": keyPath}
	case "socks5":
		su := randomAPIKey(15)
		user = nodeUser{Name: userName, Username: su, Password: password}
		ib["users"] = []any{map[string]any{"name": userName, "username": su, "password": password}}
	case "vless-xhttp":
		path := "/" + randomHex(6) + "-xhttp"
		ib["users"] = []any{userMap(user)}
		ib["transport"] = map[string]any{"type": "xhttp", "path": path}
		if useTLS {
			ib["tls"] = map[string]any{
				"enabled": true, "server_name": sni,
				"certificate_path": certPath, "key_path": keyPath,
			}
		}
	case "xhttp-udp-tls":
		path := "/" + randomHex(6) + "-xhttp"
		ib["users"] = []any{userMap(user)}
		ib["tls"] = map[string]any{
			"enabled": true, "certificate_path": certPath, "key_path": keyPath,
			"alpn": []any{"h3"},
		}
		ib["transport"] = map[string]any{"type": "xhttp", "path": path}
	case "vless-tcp-tls":
		ib["users"] = []any{userMap(user)}
		ib["tls"] = map[string]any{
			"enabled": true, "server_name": sni,
			"certificate_path": certPath, "key_path": keyPath,
		}
	case "naiveproxy":
		nuser := randomAPIKey(16)
		user = nodeUser{Name: userName, Username: nuser, Password: nuser}
		ib["users"] = []any{map[string]any{"name": userName, "username": nuser, "password": nuser}}
		ib["tls"] = map[string]any{"enabled": true, "certificate_path": certPath, "key_path": keyPath}
	case "vmess-ws", "vless-ws":
		path := "/" + randomHex(6) + "-ws"
		ib["users"] = []any{userMap(user)}
		ib["transport"] = map[string]any{
			"type": "ws", "path": path,
			"max_early_data": 2048, "early_data_header_name": "Sec-WebSocket-Protocol",
		}
		if useTLS {
			ib["tls"] = map[string]any{
				"enabled": true, "server_name": sni,
				"certificate_path": certPath, "key_path": keyPath,
			}
		}
	default:
		return nil, user, fmt.Errorf("这个入站类型网页暂不支持：%s", spec.Kind)
	}
	return map[string]any{"inbounds": []any{ib}}, user, nil
}

func userMap(u nodeUser) map[string]any {
	m := map[string]any{"name": u.Name}
	if u.UUID != "" {
		m["uuid"] = u.UUID
	}
	if u.Flow != "" {
		m["flow"] = u.Flow
	}
	if u.Password != "" && u.UUID == "" {
		m["password"] = u.Password
	}
	if u.Username != "" {
		m["username"] = u.Username
	}
	return m
}

// createNode 建入站：写配置 → 放行端口 → 写链接 → 重建订阅 → 重载。
func createNode(spec nodeCreateSpec) (*node, error) {
	info, ok := nodeKindInfos[spec.Kind]
	if !ok {
		return nil, fmt.Errorf("不支持的入站类型 %q", spec.Kind)
	}
	number := nextNodeNumber(info.Kind)
	if number == 0 {
		return nil, fmt.Errorf("编号用完了，先清理一些旧入站")
	}
	port := spec.Port
	if port == 0 {
		p, err := pickFreePort(info.Net)
		if err != nil {
			return nil, err
		}
		port = p
	}
	if !validPort(port) {
		return nil, fmt.Errorf("端口必须在 1-65535 之间")
	}
	if portInUse(port, info.Net) {
		return nil, fmt.Errorf("端口 %d 已被占用", port)
	}

	doc, user, err := buildInboundDoc(spec, number, port)
	if err != nil {
		return nil, err
	}
	file := fmt.Sprintf("%s-%d.json", info.Kind, number)
	lp := panelPaths()
	if pathExists(filepath.Join(lp.ConfDir, file)) {
		return nil, fmt.Errorf("配置文件 %s 已存在", file)
	}
	if err := saveNodeDoc(file, doc); err != nil {
		return nil, err
	}
	cleanup := func() {
		_ = os.Remove(filepath.Join(lp.ConfDir, file))
	}

	// 与脚本一致：TCP 一律放行，UDP 类协议放 UDP；xhttp-udp-tls / naive 两个都放
	protos := []string{"tcp"}
	switch spec.Kind {
	case "hysteria2", "tuic":
		protos = []string{"udp"}
	case "xhttp-udp-tls", "naiveproxy":
		protos = []string{"tcp", "udp"}
	}
	for _, proto := range protos {
		if err := allowPort(port, proto); err != nil {
			cleanup()
			return nil, fmt.Errorf("放行端口失败: %w", err)
		}
	}

	n := describeNode(file, doc)
	n.PortOK["tcp"] = contains(protos, "tcp")
	n.PortOK["udp"] = contains(protos, "udp")

	links := buildNodeLinks(n, user, nil)
	if err := writeNodeLinks(n, links); err != nil {
		cleanup()
		return nil, err
	}
	_ = addV2RayUser(user.Name)
	_ = rebuildSubFile()
	_ = refreshUserSubs()
	if err := applySingboxReload(); err != nil {
		// 配置已经写好、check 通过，重载失败不算致命：把原因带回去让界面显示
		n.Remark = err.Error()
	}
	n.Links = links
	return n, nil
}

// ---- 生成链接 ----

// buildNodeLinks 生成一个入站的分享链接（脚本的格式，含自签证书指纹）。
// shadow 用于给用户生成链接时借用原链接的 Host/SNI（比如 CDN、隧道链接）。
func buildNodeLinks(n *node, user nodeUser, shadow []string) []string {
	if len(shadow) > 0 {
		return shadow
	}
	host := hostPublicIP()
	if host == "" {
		host = "<本机IP>"
	}
	port := n.Port
	path := n.Path
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	// 自签证书要带指纹，客户端才肯连
	fingerprint, insecure := "", false
	if n.TLS && !n.Reality && n.CertPath != "" {
		if self, sha, _, err := certInfo(n.CertPath); err == nil {
			insecure = self
			if self && sha != "" {
				fingerprint = strings.ReplaceAll(sha, ":", "%3A")
			}
		}
	}
	remark := nodeLinkRemark(n)
	eq := urlEscape(remark)

	switch n.Kind {
	case "vless-reality", "http-reality", "grpc-reality", "xhttp-reality":
		priv := realityPrivateKeyOf(n)
		pub, _ := realityPublicKey(priv)
		sid := realityShortIDOf(n)
		sni := n.SNI
		if sni == "" {
			sni = n.Handshake
			if sni == "" {
				sni = defaultRealitySNI()
			}
		}
		q := "encryption=none&security=reality&sni=" + sni + "&fp=firefox&pbk=" + pub + "&sid=" + sid
		switch n.Kind {
		case "vless-reality":
			q = "encryption=none&flow=xtls-rprx-vision&security=reality&sni=" + sni + "&fp=firefox&pbk=" + pub + "&sid=" + sid + "&type=tcp&headerType=none"
		case "http-reality":
			q += "&type=http"
		case "grpc-reality":
			q += "&type=grpc&serviceName=grpc"
		case "xhttp-reality":
			q += "&type=xhttp&path=" + path + "&mode=auto"
		}
		return []string{fmt.Sprintf("vless://%s@%s:%d?%s#%s", user.UUID, bracketIPv6(host), port, q, eq)}
	case "vless-tcp-tls":
		sni := n.SNI
		if sni == "" {
			sni = host
		}
		q := fmt.Sprintf("encryption=none&security=tls&sni=%s&type=tcp", sni)
		return []string{fmt.Sprintf("vless://%s@%s:%d?%s#%s", user.UUID, bracketIPv6(host), port, q, eq)}
	case "hysteria2":
		q := "alpn=h3"
		if insecure {
			q = "insecure=1&sni=www.bing.com&pinSHA256=" + fingerprint + "&alpn=h3"
		} else if n.SNI != "" {
			q = "sni=" + n.SNI + "&alpn=h3"
		}
		return []string{fmt.Sprintf("hysteria2://%s@%s:%d/?%s#%s", user.Password, bracketIPv6(host), port, q, eq)}
	case "tuic":
		q := "congestion_control=bbr&udp_relay_mode=native&alpn=h3"
		if insecure {
			q = "insecure=1&sni=www.bing.com&congestion_control=bbr&udp_relay_mode=native&alpn=h3"
		} else if n.SNI != "" {
			q = "sni=" + n.SNI + "&congestion_control=bbr&udp_relay_mode=native&alpn=h3"
		}
		return []string{fmt.Sprintf("tuic://%s:%s@%s:%d/?%s#%s", user.UUID, user.Password, bracketIPv6(host), port, q, eq)}
	case "anytls":
		q := ""
		if insecure {
			q = "insecure=1&sni=www.bing.com&pinSHA256=" + fingerprint
		} else if n.SNI != "" {
			q = "sni=" + n.SNI
		}
		return []string{fmt.Sprintf("anytls://%s@%s:%d?%s#%s", user.Password, bracketIPv6(host), port, q, eq)}
	case "socks5":
		return []string{fmt.Sprintf("socks://%s:%s@%s:%d#%s", user.Username, user.Password, bracketIPv6(host), port, eq)}
	case "naiveproxy":
		sni := n.SNI
		if sni == "" {
			sni = host
		}
		h2 := fmt.Sprintf("naive+https://%s:%s@%s:%d?security=tls&sni=%s&insecure=0#%s", user.Username, user.Password, sni, port, sni, eq)
		h3 := fmt.Sprintf("naive+quic://%s:%s@%s:%d?congestion_control=bbr&security=tls&sni=%s&insecure=0#%s", user.Username, user.Password, sni, port, sni, eq)
		return []string{h2, h3}
	case "vless-xhttp":
		sec := "none"
		q := ""
		if n.TLS {
			sec = "tls"
			q = "&sni=" + n.SNI
		}
		return []string{fmt.Sprintf("vless://%s@%s:%d?encryption=none&security=%s%s&type=xhttp&path=%s#%s", user.UUID, bracketIPv6(host), port, sec, q, path, eq)}
	case "xhttp-udp-tls":
		sni := n.SNI
		if sni == "" {
			sni = host
		}
		return []string{fmt.Sprintf("vless://%s@%s:%d?encryption=none&security=tls&sni=%s&type=xhttp&alpn=h3&path=%s#%s", user.UUID, bracketIPv6(host), port, sni, path, eq)}
	case "vmess-ws", "vless-ws":
		tls := "none"
		if n.TLS {
			tls = "tls"
		}
		if n.Kind == "vmess-ws" {
			vm := map[string]any{
				"v": "2", "ps": remark, "add": host, "port": strconv.Itoa(port),
				"id": user.UUID, "aid": "0", "encryption": "auto",
				"net": "ws", "type": "auto", "path": path + "?ed=2048",
			}
			if n.TLS {
				vm["host"] = n.SNI
				vm["tls"] = "tls"
				vm["sni"] = n.SNI
				vm["alpn"] = ""
				vm["fp"] = "firefox"
				vm["allowInsecure"] = false
			}
			return []string{vmessLink(vm)}
		}
		q := "ed=2048&eh=Sec-WebSocket-Protocol&encryption=none&security=" + tls
		if n.TLS {
			q += "&sni=" + n.SNI
		}
		q += "&type=ws&path=" + path + "?ed=2048"
		return []string{fmt.Sprintf("vless://%s@%s:%d?%s#%s", user.UUID, bracketIPv6(host), port, q, eq)}
	}
	if len(n.Links) > 0 {
		return n.Links
	}
	return nil
}

// nodeLinkRemark 跟脚本一样带国旗：取不到国家时退回国旗以外的固定文案。
func nodeLinkRemark(n *node) string {
	emoji := nodeRemarkEmoji
	switch n.Kind {
	case "vless-reality":
		return emoji + "vless_tcp_reality"
	case "hysteria2":
		return emoji + "hysteria2"
	case "tuic":
		return emoji + "tuic"
	case "http-reality":
		return emoji + "http_reality"
	case "grpc-reality":
		return emoji + "grpc_reality"
	case "anytls":
		return emoji + "anytls"
	case "socks5":
		return emoji + "socks_port"
	case "xhttp-reality":
		return emoji + "vless_xhttp_reality"
	case "vless-xhttp":
		return emoji + "xhttp"
	case "xhttp-udp-tls":
		return emoji + "xhttp-udptsl"
	case "vless-tcp-tls":
		return emoji + "vless_tcp_tls"
	case "naiveproxy":
		return emoji + "naive_h2"
	case "vmess-ws":
		return emoji + "vmess_ws"
	case "vless-ws":
		return emoji + "vless_ws"
	}
	return emoji + n.Kind
}

func vmessLink(vm map[string]any) string {
	blob, _ := json.Marshal(vm)
	return "vmess://" + base64.StdEncoding.EncodeToString(blob)
}

// writeNodeLinks 写 /etc/sing-box/url/{类型}-{编号}.txt。
func writeNodeLinks(n *node, links []string) error {
	lp := panelPaths()
	if len(links) == 0 {
		return nil
	}
	path := filepath.Join(lp.URLDir, n.File[:len(n.File)-len(".json")]+".txt")
	body := strings.Join(links, "\n") + "\n"
	return writeFileAtomic(path, []byte(body), 0644)
}

// readNodeLinks 读一个入站自己的链接文件。
func readNodeLinks(n *node) []string {
	lp := panelPaths()
	path := filepath.Join(lp.URLDir, fmt.Sprintf("%s-%d.txt", n.Kind, n.Number))
	blob, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(blob), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// nodeCDNDomains 找链接里带 _cdn 备注的域名（与脚本 get_inbound_cdn_domain 一致）。
func nodeCDNDomains(n *node) []string {
	var out []string
	seen := map[string]bool{}
	for _, line := range n.Links {
		if !strings.Contains(line, "_cdn") {
			continue
		}
		var sni string
		if strings.HasPrefix(line, "vmess://") {
			if vm, ok := decodeVmess(line); ok {
				if ps, _ := vm["ps"].(string); strings.HasSuffix(ps, "_cdn") {
					sni, _ = vm["sni"].(string)
				}
			}
		} else {
			if frag := fragmentOf(line); strings.HasSuffix(frag, "_cdn") {
				sni = queryValue(line, "sni")
			}
		}
		if sni != "" && !seen[sni] {
			seen[sni] = true
			out = append(out, sni)
		}
	}
	return out
}

// realityPrivateKeyOf 从入站配置里取 Reality 私钥。
func realityPrivateKeyOf(n *node) string {
	doc, err := readNodeDoc(n.File)
	if err != nil {
		return ""
	}
	ib := firstInbound(doc)
	if ib == nil {
		return ""
	}
	if tls := obj(ib["tls"]); tls != nil {
		if r := obj(tls["reality"]); r != nil {
			return str(r["private_key"])
		}
	}
	return ""
}

func realityShortIDOf(n *node) string {
	doc, err := readNodeDoc(n.File)
	if err != nil {
		return ""
	}
	ib := firstInbound(doc)
	if ib == nil {
		return ""
	}
	if tls := obj(ib["tls"]); tls != nil {
		if r := obj(tls["reality"]); r != nil {
			if ids := strList(r["short_id"]); len(ids) > 0 {
				return ids[0]
			}
		}
	}
	return ""
}

// newRealityKeyPair 生成 X25519 密钥对（base64url，无填充，与 sing-box 一致）。
func newRealityKeyPair() (priv string, pub string, err error) {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	priv = base64.RawURLEncoding.EncodeToString(k.Bytes())
	pub = base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes())
	return priv, pub, nil
}

// newUUID 生成 v4 UUID。
func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// randomHex 生成随机十六进制串（WS 路径、Reality short_id）。
func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// randomAPIKey 生成 [A-Za-z0-9] 口令，长度与脚本一致（15/16/24）。
func randomAPIKey(n int) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	raw := make([]byte, n)
	_, _ = rand.Read(raw)
	for i := range b {
		b[i] = alphabet[int(raw[i])%len(alphabet)]
	}
	return string(b)
}

// freePort 让内核给一个空闲端口。
func pickFreePort(proto string) (int, error) {
	switch proto {
	case "udp":
		pc, err := net.ListenPacket("udp", ":0")
		if err != nil {
			return 0, fmt.Errorf("找不到空闲 UDP 端口: %w", err)
		}
		defer pc.Close()
		return pc.LocalAddr().(*net.UDPAddr).Port, nil
	default:
		l, err := net.Listen("tcp", ":0")
		if err != nil {
			return 0, fmt.Errorf("找不到空闲 TCP 端口: %w", err)
		}
		defer l.Close()
		return l.Addr().(*net.TCPAddr).Port, nil
	}
}

// portInUse 检查端口有没有被别的东西占着。
func portInUse(port int, proto string) bool {
	if proto == "udp" {
		pc, err := net.ListenPacket("udp", fmt.Sprintf(":%d", port))
		if err != nil {
			return true
		}
		_ = pc.Close()
		return false
	}
	l, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return true
	}
	_ = l.Close()
	return false
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// urlEscape 按 @uri 的规则转义备注（空格→%20，中文保留）。
func urlEscape(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~', c >= 0x80:
			b.WriteByte(c)
		default:
			b.WriteString(fmt.Sprintf("%%%02X", c))
		}
	}
	return b.String()
}

func queryValue(link, key string) string {
	i := strings.Index(link, "?")
	if i < 0 {
		return ""
	}
	rest := link[i+1:]
	if j := strings.Index(rest, "#"); j >= 0 {
		rest = rest[:j]
	}
	pat := regexp.MustCompile(`(^|&)` + regexp.QuoteMeta(key) + `=([^&]*)`)
	if m := pat.FindStringSubmatch(rest); m != nil {
		return m[2]
	}
	return ""
}

func fragmentOf(link string) string {
	i := strings.LastIndex(link, "#")
	if i < 0 {
		return ""
	}
	return link[i+1:]
}
