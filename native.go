package main

import (
	"encoding/base64"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"sync"
)

// Native 是 fanout 自己管理 sing-box 配置的后端。
//
// fanout 只写配置目录里的 fanout-outbounds.json 和 fanout-route.json：
// 出站由隧道列表推出来，路由由用户建的分流规则推出来。入站一律是目录里已有的
// （别的脚本建的，或老版本 fanout 留下的 fanout-in-*.json），只读。
// 每次改动整份重写两个文件，配置是纯函数产物，不会出现改了一半的中间态。
type Native struct {
	mu      sync.Mutex
	dir     string
	store   *nativeStore
	runner  *singboxRunner
	adopted []*adoptedInbound
}

func openNative(workDir string) (*Native, error) {
	if workDir == "" {
		return nil, fmt.Errorf("缺少工作目录")
	}
	if st, err := os.Stat(singbox.Bin); err != nil || st.IsDir() || st.Mode()&0111 == 0 {
		return nil, fmt.Errorf("找不到 sing-box 可执行文件 %s（重新跑 install.sh，或用 -singbox-bin 指定）", singbox.Bin)
	}
	if err := os.MkdirAll(singbox.ConfDir, 0755); err != nil {
		return nil, fmt.Errorf("创建 sing-box 配置目录失败: %w", err)
	}
	store, migrated, err := loadNativeStore(workDir)
	if err != nil {
		return nil, err
	}
	n := &Native{
		dir:    workDir,
		store:  store,
		runner: newSingboxRunner(workDir),
	}
	// 上次进程被强杀时遗留的 sing-box 子进程还占着入站端口，先收掉
	n.runner.reapOrphan()
	warnShadowingRoutes(singbox.ConfDir)
	n.refreshAdopted()
	if migrated {
		// 老版本的整条入站绑定还写在 fanout-route.json 里，立刻按新模型重写一次，
		// 别让它在第一条隧道连上之前继续生效
		if err := n.store.save(workDir); err != nil {
			log.Printf("保存迁移后的状态失败: %v", err)
		}
		n.mu.Lock()
		if err := n.apply(nil); err != nil {
			log.Printf("迁移后重写 sing-box 配置失败: %v", err)
		}
		n.mu.Unlock()
	}
	return n, nil
}

func (n *Native) Kind() string { return "native" }

func (n *Native) Describe() string {
	return "sing-box（" + n.runner.describe() + "）"
}

// refreshAdopted 重新扫描配置目录里的入站，并给新出现的分配界面 ID。
// 调用方必须已持有 n.mu（openNative 里除外）。
func (n *Native) refreshAdopted() {
	list := scanAdopted(singbox.ConfDir)
	added := false
	if n.store.Adopted == nil {
		n.store.Adopted = map[string]*adoptedState{}
	}
	for _, a := range list {
		st := n.store.Adopted[a.Tag]
		if st == nil {
			st = &adoptedState{ID: n.store.NextID}
			n.store.NextID++
			n.store.Adopted[a.Tag] = st
			added = true
		}
		a.ID = st.ID
	}
	n.adopted = list
	// 老规则按入站 tag 匹配，迁移成那些入站里的用户名（auth_user）
	inboundUsers := map[string][]string{}
	for _, a := range list {
		inboundUsers[a.Tag] = a.Users
	}
	for _, r := range n.store.Rules {
		if len(r.Users) == 0 && len(r.Inbounds) > 0 {
			if users := ruleUsers(r, inboundUsers); len(users) > 0 {
				r.Users, r.Inbounds = users, nil
				added = true
			}
		}
	}
	// 新发现的入站立刻记下 ID，重启后界面上的编号不变
	if added {
		if err := n.store.save(n.dir); err != nil {
			log.Printf("保存入站编号失败: %v", err)
		}
	}
}

func (n *Native) adoptedByID(id int) *adoptedInbound {
	for _, a := range n.adopted {
		if a.ID == id {
			return a
		}
	}
	return nil
}

// apply 重写 fanout 的两个配置文件，校验通过后让 sing-box 重载，然后落盘。
// 校验不过、或重载后 sing-box 没起来，就把 fanout 的文件恢复成改之前的样子。
// 调用方必须已持有 n.mu。
func (n *Native) apply(tunnels []*Tunnel) error {
	n.refreshAdopted()
	opts := buildOptions{
		RuleSetDir:    ruleSetDir(),
		LegacyRuleSet: n.runner.legacyRuleSet(),
		Foreign:       scanForeign(singbox.ConfDir),
	}
	if opts.LegacyRuleSet {
		opts.RuleSetDir = ""
	}
	built := buildFanoutFiles(n.store.Rules, n.adopted, tunnels, opts)
	files, err := encodeFiles(built)
	if err != nil {
		return err
	}
	snap, err := snapshotFanoutFiles(singbox.ConfDir)
	if err != nil {
		return err
	}
	unchanged := true
	for name, blob := range files {
		if string(snap[name]) != string(blob) {
			unchanged = false
		}
	}
	if err := writeFanoutFiles(singbox.ConfDir, files); err != nil {
		_ = writeFanoutFiles(singbox.ConfDir, snap)
		n.discardChanges()
		return err
	}
	rollback := func() {
		if rerr := writeFanoutFiles(singbox.ConfDir, snap); rerr != nil {
			log.Printf("回滚 sing-box 配置失败: %v", rerr)
		}
		n.discardChanges()
	}

	// 自己托管进程时，目录里没有入站就不必留着进程占资源
	want := n.needsRunning()
	if n.runner.initSys != "" || want {
		if err := n.runner.check(); err != nil {
			rollback()
			return err
		}
	}
	// 服务模式下文件内容没变（比如隧道重连但端口凭据都没变）就不打扰 sing-box，
	// 免得每次重载都断一下别的脚本的连接
	if unchanged && n.runner.initSys != "" {
		return n.store.save(n.dir)
	}
	if err := n.runner.reload(want); err != nil {
		rollback()
		_ = n.runner.reload(want)
		return err
	}
	// 有远程规则集时多确认一次：check 不下载规则集，下载失败要到启动时才暴露
	if routeHasRuleSets(built) && (n.runner.initSys != "" || want) {
		if err := n.runner.verifyAlive(); err != nil {
			rollback()
			if rerr := n.runner.reload(want); rerr != nil {
				log.Printf("回滚后重载 sing-box 失败: %v", rerr)
			}
			return fmt.Errorf("%v；已回滚 fanout 的配置", err)
		}
	}
	if err := n.store.save(n.dir); err != nil {
		return err
	}
	pruneRuleSetCache(ruleSetDir(), n.store.Rules)
	return nil
}

func routeHasRuleSets(built map[string]any) bool {
	doc, _ := built[fanoutRouteFile].(map[string]any)
	route, _ := doc["route"].(map[string]any)
	sets, _ := route["rule_set"].([]any)
	return len(sets) > 0
}

// discardChanges 丢掉内存里还没生效的改动，回到上次成功落盘的状态，
// 免得配置文件回滚了而内存里还留着坏规则，下次 apply 又写回去。
func (n *Native) discardChanges() {
	if st, _, err := loadNativeStore(n.dir); err == nil {
		n.store = st
	}
	n.refreshAdopted()
}

// needsRunning 判断托管模式下是否需要 sing-box 进程：配置目录里有入站就要。
func (n *Native) needsRunning() bool {
	return len(n.adopted) > 0
}

// OnTunnelsChanged 在隧道集合变化后重建配置。出站直接由隧道列表推导，
// 隧道一变就要重新生成，否则新出口没有对应的 socks 出站。
func (n *Native) OnTunnelsChanged(tunnels []*Tunnel) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.apply(tunnels)
}

// Close 停掉自己拉起的 sing-box 子进程；服务模式下不动服务。
func (n *Native) Close() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.runner.stop()
}

// ruleCount 统计每个入站被几条规则引用（规则里有这个入站的任一用户就算）。
func (n *Native) ruleCount() map[string]int {
	c := map[string]int{}
	for _, a := range n.adopted {
		for _, r := range n.store.Rules {
			for _, u := range r.Users {
				if strIn(a.Users, u) {
					c[a.Tag]++
					break
				}
			}
		}
	}
	return c
}

func (n *Native) Inbounds() ([]Inbound, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.refreshAdopted()

	count := n.ruleCount()
	out := make([]Inbound, 0, len(n.adopted))
	for _, a := range n.adopted {
		out = append(out, Inbound{
			ID: a.ID, Port: a.Port, Protocol: a.Type, Tag: a.Tag,
			Source: a.File, Rules: count[a.Tag], Users: append([]string{}, a.Users...),
		})
	}
	return out, nil
}

func (n *Native) InboundDetail(id int, publicHost string) (*InboundDetail, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	a := n.adoptedByID(id)
	if a == nil {
		n.refreshAdopted()
		if a = n.adoptedByID(id); a == nil {
			return nil, fmt.Errorf("入站 %d 不存在", id)
		}
	}
	detail := &InboundDetail{
		Inbound: Inbound{
			ID: a.ID, Port: a.Port, Protocol: a.Type, Tag: a.Tag,
			Source: a.File, Rules: n.ruleCount()[a.Tag], Users: append([]string{}, a.Users...),
		},
		Listen:  str(a.Raw["listen"]),
		Network: "tcp",
		TLS:     "none",
	}
	if ib := a.toNative(); ib != nil {
		detail.Network, detail.TLS = ib.netOrTCP(), ib.securityOrNone()
		for _, c := range ib.Clients {
			id := c.ID
			if id == "" {
				id = c.Password
			}
			detail.Clients = append(detail.Clients, ClientInfo{Email: c.Email, ID: id})
		}
	}
	detail.Links = n.adoptedLinks(a, publicHost)
	return detail, nil
}

// adoptedLinks 给入站出分享链接：优先用脚本自己导出的，没有再自己推。
func (n *Native) adoptedLinks(a *adoptedInbound, publicHost string) []string {
	if links := scriptLinks(singbox.URLDir, a.Port); len(links) > 0 {
		return links
	}
	ib := a.toNative()
	if ib == nil {
		return nil
	}
	var out []string
	for _, c := range ib.Clients {
		if l := shareLink(ib, c, publicHost); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func (n *Native) InboundLinks(ids []int, publicHost string) ([]string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.refreshAdopted()

	var out []string
	for _, id := range ids {
		if a := n.adoptedByID(id); a != nil {
			out = append(out, n.adoptedLinks(a, publicHost)...)
		}
	}
	return out, nil
}

// ---- 分流规则 ----

func (n *Native) Rules() []RouteRule {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]RouteRule, 0, len(n.store.Rules))
	for _, r := range n.store.Rules {
		out = append(out, cloneRule(r))
	}
	return out
}

// Existing 列出配置目录里别的文件定义的、可以在规则里引用的出站 / 端点和规则集。
func (n *Native) Existing() ([]ForeignOutbound, []ForeignRuleSet) {
	fc := scanForeign(singbox.ConfDir)
	sets := fc.RuleSets
	if sets == nil {
		sets = []ForeignRuleSet{}
	}
	return fc.Routable(), sets
}

func cloneRule(r *RouteRule) RouteRule {
	c := *r
	c.Users = append([]string(nil), r.Users...)
	c.Inbounds = append([]string(nil), r.Inbounds...)
	c.LocalRuleSets = append([]string(nil), r.LocalRuleSets...)
	c.Domains = append([]string(nil), r.Domains...)
	c.RuleSets = append([]RuleSetRef(nil), r.RuleSets...)
	return c
}

// SaveRule 新建或修改一条规则。
//
// 规则集要先下载校验（可能要好几秒），这一步放在锁外做，不卡住别的操作。
func (n *Native) SaveRule(in RuleInput, tunnels []*Tunnel) (*RouteRule, error) {
	n.mu.Lock()
	n.refreshAdopted()
	fc := scanForeign(singbox.ConfDir)
	env := ruleEnv{Users: map[string]bool{}, Exits: map[string]bool{},
		Outbounds: fc.outboundTags(), RuleSets: fc.ruleSetTags()}
	for _, a := range n.adopted {
		for _, u := range a.Users {
			env.Users[u] = true
		}
	}
	var prev *RouteRule
	if in.ID != 0 {
		_, p := n.store.ruleByID(in.ID)
		if p == nil {
			n.mu.Unlock()
			return nil, fmt.Errorf("规则 %d 不存在", in.ID)
		}
		c := cloneRule(p)
		prev = &c
	}
	n.mu.Unlock()

	for _, t := range tunnels {
		env.Exits[sanitizeTag(t.Node.HostName)] = true
	}
	rule, err := normalizeRule(in, env, prev)
	if err != nil {
		return nil, err
	}
	// 先下载校验一次：地址写错当场报出来；1.14+ 还拿它当 initial_path
	if err := prefetchRuleSets(ruleSetDir(), rule.RuleSets); err != nil {
		return nil, err
	}

	n.mu.Lock()
	defer n.mu.Unlock()
	if rule.ID == 0 {
		rule.ID = n.store.NextRuleID
		n.store.NextRuleID++
		n.store.Rules = append(n.store.Rules, rule)
	} else {
		i, _ := n.store.ruleByID(rule.ID)
		if i < 0 {
			return nil, fmt.Errorf("规则 %d 不存在", rule.ID)
		}
		n.store.Rules[i] = rule
	}
	if err := n.apply(tunnels); err != nil {
		return nil, err
	}
	c := cloneRule(rule)
	return &c, nil
}

func (n *Native) DeleteRule(id int, tunnels []*Tunnel) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	i, _ := n.store.ruleByID(id)
	if i < 0 {
		return fmt.Errorf("规则 %d 不存在", id)
	}
	n.store.Rules = append(n.store.Rules[:i:i], n.store.Rules[i+1:]...)
	return n.apply(tunnels)
}

func (n *Native) MoveRule(id int, delta int, tunnels []*Tunnel) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	i, _ := n.store.ruleByID(id)
	if i < 0 {
		return fmt.Errorf("规则 %d 不存在", id)
	}
	j := i + delta
	if j < 0 || j >= len(n.store.Rules) || delta == 0 {
		return nil
	}
	n.store.Rules[i], n.store.Rules[j] = n.store.Rules[j], n.store.Rules[i]
	return n.apply(tunnels)
}

func (n *Native) EnableRule(id int, on bool, tunnels []*Tunnel) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	_, r := n.store.ruleByID(id)
	if r == nil {
		return fmt.Errorf("规则 %d 不存在", id)
	}
	r.Enabled = on
	return n.apply(tunnels)
}

// Rebind 在出口换了节点之后，把指向旧节点的规则改指新节点。
// 出站 tag 跟着节点名走，不改的话规则会指向一个已经不存在的出口而被跳过。
func (n *Native) Rebind(oldHost string, target *Tunnel, tunnels []*Tunnel) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	oldTag := sanitizeTag(oldHost)
	newTag := sanitizeTag(target.Node.HostName)
	for _, r := range n.store.Rules {
		if r.Exit == oldTag {
			r.Exit = newTag
		}
	}
	return n.apply(tunnels)
}

func (n *Native) ResyncOutbound(t *Tunnel, tunnels []*Tunnel) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.apply(tunnels)
}

// shareLink 生成客户端可直接导入的分享链接。协议不支持时返回空串。
func shareLink(ib *nativeInbound, c nativeClient, host string) string {
	net := ib.netOrTCP()
	sec := ib.securityOrNone()
	frag := url.PathEscape(ib.Remark)
	hostPort := fmt.Sprintf("%s:%d", bracketIPv6(host), ib.Port)

	// TLS 公共参数：SNI，自签证书再带上跳过验证与证书指纹
	tlsQuery := func(q url.Values) {
		if ib.TLS == nil {
			return
		}
		if ib.TLS.ServerName != "" {
			q.Set("sni", ib.TLS.ServerName)
		}
		if ib.TLS.SelfSigned {
			q.Set("insecure", "1")
			q.Set("allowInsecure", "1")
			if ib.TLS.CertSha256 != "" {
				q.Set("pinSHA256", ib.TLS.CertSha256)
			}
		}
	}

	switch ib.Protocol {
	case "anytls":
		q := url.Values{}
		tlsQuery(q)
		return fmt.Sprintf("anytls://%s@%s?%s#%s", url.PathEscape(c.Password), hostPort, q.Encode(), frag)
	case "hysteria2":
		q := url.Values{}
		tlsQuery(q)
		return fmt.Sprintf("hysteria2://%s@%s?%s#%s", url.PathEscape(c.Password), hostPort, q.Encode(), frag)
	case "tuic":
		q := url.Values{}
		tlsQuery(q)
		if q.Get("insecure") == "1" {
			q.Set("allow_insecure", "1")
		}
		q.Set("alpn", "h3")
		q.Set("congestion_control", "bbr")
		q.Set("udp_relay_mode", "native")
		return fmt.Sprintf("tuic://%s:%s@%s?%s#%s", c.ID, url.PathEscape(c.Password), hostPort, q.Encode(), frag)
	case "shadowsocks":
		pass := c.Password
		if ib.ServerPassword != "" {
			pass = ib.ServerPassword + ":" + pass
		}
		// SIP002：2022 系列不允许 base64，按 URL 转义写明文
		var user string
		if strings.HasPrefix(ib.Method, "2022-") {
			user = url.PathEscape(ib.Method) + ":" + url.PathEscape(pass)
		} else {
			user = base64.RawURLEncoding.EncodeToString([]byte(ib.Method + ":" + pass))
		}
		return fmt.Sprintf("ss://%s@%s#%s", user, hostPort, frag)
	case "vless", "vmess", "trojan":
	default:
		return ""
	}

	q := url.Values{}
	q.Set("type", net)
	q.Set("security", sec)

	switch net {
	case "ws", "httpupgrade":
		q.Set("path", ib.Path)
		if ib.Host != "" {
			q.Set("host", ib.Host)
		}
	case "grpc":
		q.Set("serviceName", strings.TrimPrefix(ib.Path, "/"))
	}

	switch sec {
	case "tls":
		tlsQuery(q)
	case "reality":
		if ib.Reality != nil {
			if len(ib.Reality.ServerNames) > 0 {
				q.Set("sni", ib.Reality.ServerNames[0])
			}
			q.Set("pbk", ib.Reality.PublicKey)
			if len(ib.Reality.ShortIDs) > 0 {
				q.Set("sid", ib.Reality.ShortIDs[0])
			}
			if ib.Reality.Fingerprint != "" {
				q.Set("fp", ib.Reality.Fingerprint)
			}
		}
	}

	if c.Flow != "" && ib.Protocol == "vless" {
		q.Set("flow", c.Flow)
	}

	switch ib.Protocol {
	case "trojan":
		return fmt.Sprintf("trojan://%s@%s?%s#%s", url.PathEscape(c.Password), hostPort, q.Encode(), frag)
	case "vmess":
		// vmess 的 base64 形式各家客户端解析不一，用通用的 URI 形式
		q.Set("encryption", "auto")
		return fmt.Sprintf("vmess://%s@%s?%s#%s", c.ID, hostPort, q.Encode(), frag)
	default:
		q.Set("encryption", "none")
		return fmt.Sprintf("vless://%s@%s?%s#%s", c.ID, hostPort, q.Encode(), frag)
	}
}

// bracketIPv6 给 IPv6 地址加方括号，URL 里才能和端口分开。
func bracketIPv6(h string) string {
	if strings.Contains(h, ":") && !strings.HasPrefix(h, "[") {
		return "[" + h + "]"
	}
	return h
}
