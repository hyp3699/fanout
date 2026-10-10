package main

import (
	"strings"
	"sync"
)

// Panel 是 fanout 管理 sing-box 配置的后端。
//
// 目前只有一个实现：fanout 自己生成 sing-box 配置的 Native。
// 入站不归 fanout 管：它只列出配置目录里已有的入站（带分享链接），
// 并按用户建的分流规则把其中部分流量送进出口。
type Panel interface {
	// Kind 返回后端类型，目前固定是 "native"。
	Kind() string
	// Describe 给出一行人能读的后端说明。
	Describe() string

	// Inbounds 列出配置目录里现有的入站（只读）。
	Inbounds() ([]Inbound, error)
	InboundDetail(id int, publicHost string) (*InboundDetail, error)
	InboundLinks(ids []int, publicHost string) ([]string, error)

	// Existing 列出配置目录里别的文件定义的出站 / 端点（可作规则目标）和规则集（可直接引用）。
	Existing() ([]ForeignOutbound, []ForeignRuleSet)

	// Rules 返回有序的分流规则（副本）。
	Rules() []RouteRule
	// SaveRule 新建（ID 为 0）或修改一条分流规则，并重建 sing-box 配置。
	SaveRule(in RuleInput, tunnels []*Tunnel) (*RouteRule, error)
	// DeleteRule 删除一条规则。
	DeleteRule(id int, tunnels []*Tunnel) error
	// MoveRule 把规则上移（delta<0）或下移（delta>0）一位。规则按顺序匹配。
	MoveRule(id int, delta int, tunnels []*Tunnel) error
	// EnableRule 启用或停用一条规则，停用的规则不写进配置。
	EnableRule(id int, on bool, tunnels []*Tunnel) error

	// Rebind 在出口换了节点之后，把指向旧节点的规则改指新节点。
	Rebind(oldHost string, target *Tunnel, tunnels []*Tunnel) error
	ResyncOutbound(t *Tunnel, tunnels []*Tunnel) error

	// OnTunnelsChanged 在隧道集合变化后调用。出站完全由隧道列表推导，
	// 新开的出口必须重建配置才有对应出站。
	OnTunnelsChanged(tunnels []*Tunnel) error

	// Close 释放后端占用的资源。自己拉起的 sing-box 要停掉，
	// 否则 fanout 退出后它会变成孤儿进程，下次启动撞端口。
	Close()
}

// Inbound 是配置目录里的一个已有入站。
type Inbound struct {
	ID       int    `json:"id"`
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
	Tag      string `json:"tag"`    // sing-box 里的入站 tag，规则按它引用
	Source   string `json:"source"` // 来源文件名
	Rules    int    `json:"rules"`  // 引用它的用户的分流规则条数
	// Users 是入站里的用户名，分流规则按用户名匹配
	Users []string `json:"users"`
}

// InboundDetail 是某个入站的完整信息，含客户端与分享链接。
type InboundDetail struct {
	Inbound
	Clients []ClientInfo `json:"clients"`
	Links   []string     `json:"links"`
	Listen  string       `json:"listen"`
	Network string       `json:"network"`
	TLS     string       `json:"tls"`
}

type ClientInfo struct {
	Email string `json:"email"`
	ID    string `json:"id"`
}

// fanoutTagPrefix 是 fanout 生成的出站 tag 前缀，便于识别，不碰用户自己的条目。
const fanoutTagPrefix = "fanout-"

// tunnelTag 用节点主机名而非槽位号做标识：槽位在 fanout 重启后会重新分配，
// 用它做 tag 会让已有的分流规则悄悄串到别的节点上。
func tunnelTag(t *Tunnel) string {
	return fanoutTagPrefix + "exit-" + sanitizeTag(t.Node.HostName)
}

// sanitizeTag 把主机名收敛成安全的 tag 片段（也用于文件名）。
func sanitizeTag(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}

// exitLabel 给出口起个好认的名字：国旗 + 中文国名 + VPN Gate 节点的完整 IP。
// 同一地区可能有多条隧道，带上 IP 才能区分。界面上规则指向哪个出口就用它显示。
func exitLabel(t *Tunnel) string {
	place := nodeLabel(t.Node)

	addr := t.Node.IP
	if addr == "" {
		addr = t.ExitIP
	}
	if addr == "" {
		addr = t.Node.HostName
	}

	if place == "" {
		return addr
	}
	return place + " " + addr
}

// closePanel 在进程退出时释放后端资源。
func closePanel() {
	panelState.mu.Lock()
	p := panelState.current
	panelState.mu.Unlock()
	if p != nil {
		p.Close()
	}
}

// panelState 缓存已打开的后端。
var panelState struct {
	mu      sync.Mutex
	current Panel
	workDir string
}

// configurePanel 记录工作目录。sing-box 的路径由 configureSingBox 设置。
func configurePanel(workDir string) {
	panelState.mu.Lock()
	defer panelState.mu.Unlock()
	panelState.workDir = workDir
	panelState.current = nil
}

// openPanel 返回当前可用的后端（自建 sing-box）。
func openPanel() (Panel, error) {
	panelState.mu.Lock()
	defer panelState.mu.Unlock()

	if panelState.current != nil {
		return panelState.current, nil
	}
	n, err := openNative(panelState.workDir)
	if err != nil {
		return nil, err
	}
	panelState.current = n
	return n, nil
}
