package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// 配置目录里别的脚本已经定义好的东西：出站 / 端点、规则集、route.final。
//
// fanout 只读它们：分流规则可以直接把流量送到已有的出站（比如 direct、warp），
// 也可以直接引用 route.json 里已有的规则集 tag，不重复定义；fanout 需要直连时
// （老版本 sing-box 的规则集 download_detour）也优先借用已有的 direct 出站。

// ForeignOutbound 是配置目录里别的文件定义的一个出站或端点。
type ForeignOutbound struct {
	Tag  string `json:"tag"`
	Type string `json:"type"`
	File string `json:"file"`
	// Endpoint 表示它来自 endpoints（wireguard、tailscale 等），1.11 起端点也能当出站用
	Endpoint bool `json:"endpoint,omitempty"`
}

// ForeignRuleSet 是配置目录里别的文件定义的一个 route.rule_set。
type ForeignRuleSet struct {
	Tag    string `json:"tag"`
	Type   string `json:"type,omitempty"`   // remote / local / inline
	Format string `json:"format,omitempty"` // binary / source
	URL    string `json:"url,omitempty"`
	Path   string `json:"path,omitempty"`
	File   string `json:"file"`
}

// foreignConf 是扫描配置目录得到的第三方定义，顺序与 sing-box 合并顺序一致（按文件名、数组顺序）。
type foreignConf struct {
	Outbounds []ForeignOutbound
	RuleSets  []ForeignRuleSet
	// HasFinal 表示别的文件已经写了 route.final
	HasFinal bool
}

// unroutableTypes 是不能当路由目标的出站类型：block / dns 是 1.11 之前的特殊出站
// （新版用 reject / hijack-dns 动作代替），把流量送过去不是"分流"。
var unroutableTypes = map[string]bool{"block": true, "dns": true}

// scanForeign 读取配置目录里除 fanout 自己两个文件之外的所有 .json。
// 解析不了的文件跳过（sing-box check 会报真正的错）。
func scanForeign(dir string) foreignConf {
	var fc foreignConf
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fc
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") && !isManagedFile(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		blob, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		var doc struct {
			Outbounds []map[string]any `json:"outbounds"`
			Endpoints []map[string]any `json:"endpoints"`
			Route     struct {
				Final   string           `json:"final"`
				RuleSet []map[string]any `json:"rule_set"`
			} `json:"route"`
		}
		if json.Unmarshal(bytes.TrimSpace(blob), &doc) != nil {
			continue
		}
		for _, o := range doc.Outbounds {
			if tag := str(o["tag"]); tag != "" {
				fc.Outbounds = append(fc.Outbounds, ForeignOutbound{Tag: tag, Type: str(o["type"]), File: name})
			}
		}
		for _, o := range doc.Endpoints {
			if tag := str(o["tag"]); tag != "" {
				fc.Outbounds = append(fc.Outbounds, ForeignOutbound{Tag: tag, Type: str(o["type"]), File: name, Endpoint: true})
			}
		}
		if doc.Route.Final != "" {
			fc.HasFinal = true
		}
		for _, rs := range doc.Route.RuleSet {
			base := ForeignRuleSet{Type: str(rs["type"]), Format: str(rs["format"]), URL: str(rs["url"]), Path: str(rs["path"]), File: name}
			// 1.14 起 tag 也可以是数组
			for _, tag := range strList(rs["tag"]) {
				if tag != "" {
					r := base
					r.Tag = tag
					fc.RuleSets = append(fc.RuleSets, r)
				}
			}
		}
	}
	return fc
}

// Routable 是界面上可选作规则目标的已有出站。
func (fc foreignConf) Routable() []ForeignOutbound {
	out := []ForeignOutbound{}
	for _, o := range fc.Outbounds {
		if !unroutableTypes[o.Type] {
			out = append(out, o)
		}
	}
	return out
}

// outboundTags 是能当路由目标的已有出站 / 端点 tag。
func (fc foreignConf) outboundTags() map[string]bool {
	m := map[string]bool{}
	for _, o := range fc.Routable() {
		m[o.Tag] = true
	}
	return m
}

// ruleSetTags 是已有的规则集 tag。
func (fc foreignConf) ruleSetTags() map[string]bool {
	m := map[string]bool{}
	for _, r := range fc.RuleSets {
		m[r.Tag] = true
	}
	return m
}

func (fc foreignConf) ruleSet(tag string) (ForeignRuleSet, bool) {
	for _, r := range fc.RuleSets {
		if r.Tag == tag {
			return r, true
		}
	}
	return ForeignRuleSet{}, false
}

// directTag 返回可以借用的已有直连出站：优先 tag 就叫 direct 的，否则按合并顺序第一个
// type 为 direct 的出站。没有时返回空串，fanout 才自己建 fanout-direct。
func (fc foreignConf) directTag() string {
	first := ""
	for _, o := range fc.Outbounds {
		if o.Endpoint || o.Type != "direct" {
			continue
		}
		if o.Tag == "direct" {
			return o.Tag
		}
		if first == "" {
			first = o.Tag
		}
	}
	return first
}

// defaultOutbound 是没有 fanout 时 sing-box 会用的默认出站：没写 route.final 时取合并后的
// 第一个出站（端点不算）。没有任何出站时返回空串（sing-box 会自己补一个直连）。
func (fc foreignConf) defaultOutbound() string {
	for _, o := range fc.Outbounds {
		if !o.Endpoint {
			return o.Tag
		}
	}
	return ""
}

// kind 粗判一个已有规则集是否按 IP 匹配：tag、地址或路径里带 geoip 的当作 IP 规则集，
// 带 geosite 的当作纯域名；别的看不出来，按规则上「有 IP 段」的勾选。
func (r ForeignRuleSet) kind() (ip, domain bool) {
	s := strings.ToLower(r.Tag + " " + r.URL + " " + r.Path)
	return strings.Contains(s, "geoip"), strings.Contains(s, "geosite")
}

// foreignRuleSetTags 收集配置目录里别的文件已经定义的 route.rule_set tag，
// fanout 生成的规则集 tag 要避开它们（sing-box 遇到重复 tag 会拒绝启动）。
func foreignRuleSetTags(dir string) map[string]bool { return scanForeign(dir).ruleSetTags() }
