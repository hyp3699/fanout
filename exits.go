package main

import (
	"sync"
	"time"
)

// ExitRule 是指向某个出口的一条分流规则的摘要，显示在出口那一行上。
type ExitRule struct {
	ID      int    `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

// Exit 是界面上的一行：一条隧道，以及指向它的分流规则。
type Exit struct {
	Slot    int       `json:"slot"`
	Port    int       `json:"port"` // SOCKS5 端口
	Host    string    `json:"host"`
	Label   string    `json:"label"` // 国旗 + 国名 + 节点完整 IP
	Region  string    `json:"region"`
	Country string    `json:"country"`
	ExitIP  string    `json:"exit_ip"`
	Status  string    `json:"status"`
	Err     string    `json:"err,omitempty"`
	Since   time.Time `json:"since"`
	// SOCKS5 凭据：界面要能看、能复制、能改
	SocksUser string     `json:"socks_user"`
	SocksPass string     `json:"socks_pass"`
	Rules     []ExitRule `json:"rules"`
}

// RuleView 是界面上的一条分流规则，带上算好的状态。
type RuleView struct {
	RouteRule
	// Target 是目标的种类：exit（fanout 出口）/ outbound（配置目录里已有的出站）
	Target string `json:"target"`
	// ExitHost 是出口的节点主机名（找不到隧道、或目标是已有出站时为空）
	ExitHost  string `json:"exit_host"`
	ExitLabel string `json:"exit_label"`
	// ExitState: up 已连通（已有出站：存在）/ down 隧道在但没连通 / gone 出口已停掉（已有出站：不存在了）
	ExitState string `json:"exit_state"`
	// MissingInbounds 是规则里引用了、但配置目录里已经没有的入站
	MissingInbounds []string `json:"missing_inbounds,omitempty"`
	// MissingRuleSets 是规则里引用了、但配置目录里已经没有的已有规则集
	MissingRuleSets []string `json:"missing_rule_sets,omitempty"`
	// Active 表示这条规则此刻真的写进了 sing-box 配置
	Active bool `json:"active"`
}

// ExitsView 是主界面需要的全部数据。
type ExitsView struct {
	Exits []Exit `json:"exits"`
	// Inbounds 是配置目录里现有的入站，只读
	Inbounds []Inbound `json:"inbounds"`
	// Rules 是有序的分流规则
	Rules []RuleView `json:"rules"`
	Panel string     `json:"panel"` // 后端不可用时的原因，空表示正常
	// Backend 目前固定是 "native"（fanout 管理的 sing-box）。
	Backend string `json:"backend"`
	// PanelInfo 是后端的一行说明，显示在标题旁
	PanelInfo string `json:"panel_info"`
	// PublicIP 是母机公网 IPv4，前端用它当 SOCKS5/分享链接的连接地址
	PublicIP string `json:"public_ip"`
	// Outbounds 是配置目录里已有的出站 / 端点，规则可以直接把流量送过去
	Outbounds []ForeignOutbound `json:"outbounds"`
	// RuleSets 是配置目录里已有的规则集，规则可以直接引用
	RuleSets []ForeignRuleSet `json:"rule_sets"`
}

// inboundCache 给入站列表做很短的缓存。界面每几秒轮询一次，
// 而每次读入站都要顺带扫一遍 sing-box 配置目录，没必要每次都真的去读。
type inboundCache struct {
	mu   sync.Mutex
	at   time.Time
	list []Inbound
	err  error
}

const inboundCacheTTL = 2500 * time.Millisecond

var ibCache inboundCache

func cachedInbounds() ([]Inbound, error) {
	ibCache.mu.Lock()
	defer ibCache.mu.Unlock()
	if time.Since(ibCache.at) < inboundCacheTTL {
		return ibCache.list, ibCache.err
	}

	var list []Inbound
	x, err := openPanel()
	if err == nil {
		list, err = x.Inbounds()
	}
	ibCache.at, ibCache.list, ibCache.err = time.Now(), list, err
	return list, err
}

// invalidateInbounds 在写操作之后调用，让下一次读立刻反映改动。
func invalidateInbounds() {
	ibCache.mu.Lock()
	ibCache.at = time.Time{}
	ibCache.mu.Unlock()
}

// ExitsOf 把隧道、入站和分流规则 join 成界面直接可用的形态。
func (m *Manager) ExitsOf() ExitsView {
	tunnels := m.Tunnels()
	view := ExitsView{
		Exits: make([]Exit, 0, len(tunnels)), Inbounds: []Inbound{}, Rules: []RuleView{},
		Outbounds: []ForeignOutbound{}, RuleSets: []ForeignRuleSet{},
		PublicIP: hostPublicIP(),
	}

	byHost := map[string]int{}
	for i, t := range tunnels {
		byHost[sanitizeTag(t.Node.HostName)] = i
		cred := t.credential()
		view.Exits = append(view.Exits, Exit{
			Slot: t.Slot, Port: t.Port, Host: t.Node.HostName, Label: exitLabel(t),
			Region: t.Node.CountryCode, Country: nodeLabel(t.Node),
			ExitIP: t.ExitIP, Status: t.Status, Err: t.Err, Since: t.Since,
			SocksUser: cred.User, SocksPass: cred.Pass, Rules: []ExitRule{},
		})
	}

	p, err := openPanel()
	if err != nil {
		view.Panel = err.Error()
		return view
	}
	view.Backend = p.Kind()
	view.PanelInfo = p.Describe()

	list, err := cachedInbounds()
	if err != nil {
		view.Panel = err.Error()
	} else if list != nil {
		view.Inbounds = list
	}
	view.Outbounds, view.RuleSets = p.Existing()
	view.Rules = ruleViews(p.Rules(), view.Exits, byHost, list, view.Outbounds, view.RuleSets)
	for _, rv := range view.Rules {
		if rv.Target != "exit" {
			continue
		}
		if i, ok := byHost[rv.Exit]; ok {
			view.Exits[i].Rules = append(view.Exits[i].Rules, ExitRule{ID: rv.ID, Name: rv.Name, Enabled: rv.Enabled})
		}
	}
	return view
}

// ruleViews 给每条规则算出出口状态与是否生效，算法与 buildRouteRules 的跳过条件一致。
func ruleViews(rules []RouteRule, exits []Exit, byHost map[string]int, inbounds []Inbound,
	outbounds []ForeignOutbound, ruleSets []ForeignRuleSet) []RuleView {
	have := map[string]bool{}
	for _, ib := range inbounds {
		have[ib.Tag] = true
	}
	outs := map[string]ForeignOutbound{}
	for _, o := range outbounds {
		outs[o.Tag] = o
	}
	sets := map[string]bool{}
	for _, rs := range ruleSets {
		sets[rs.Tag] = true
	}
	out := make([]RuleView, 0, len(rules))
	for _, r := range rules {
		rv := RuleView{RouteRule: r, Target: "exit", ExitState: "gone", ExitLabel: r.Exit}
		if r.Outbound != "" {
			rv.Target, rv.ExitLabel = "outbound", r.Outbound
			if o, ok := outs[r.Outbound]; ok {
				rv.ExitState = "up"
				if o.Type != "" {
					rv.ExitLabel = r.Outbound + "（" + o.Type + "）"
				}
			}
		} else if i, ok := byHost[r.Exit]; ok {
			e := exits[i]
			rv.ExitHost, rv.ExitLabel = e.Host, e.Label
			rv.ExitState = "down"
			if e.Status == "up" {
				rv.ExitState = "up"
			}
		}
		live := 0
		for _, t := range r.LocalRuleSets {
			if sets[t] {
				live++
			} else {
				rv.MissingRuleSets = append(rv.MissingRuleSets, t)
			}
		}
		hasCond := r.All || len(r.Domains) > 0 || len(r.RuleSets) > 0 || live > 0
		present := 0
		for _, t := range r.Inbounds {
			if have[t] {
				present++
			} else {
				rv.MissingInbounds = append(rv.MissingInbounds, t)
			}
		}
		rv.Active = r.Enabled && rv.ExitState == "up" && present > 0 && hasCond
		out = append(out, rv)
	}
	return out
}
