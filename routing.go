package main

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// 分流规则。
//
// 一条规则 = 若干个入站（按 sing-box tag）+ 匹配条件 → 一个目标。
// 目标是 fanout 的出口（fanout-exit-*），或者配置目录里已有的出站 / 端点（direct、warp 等）。
// 条件是"自定义域名"和"规则集"两类，命中任意一条就走目标：
//
//	入站匹配 && (任一域名 || 任一规则集)
//
// 规则集可以是 geosite:/geoip: 简写、远程地址（fanout 自己定义 fanout-rs-*），
// 也可以直接引用配置目录里已有的 route.rule_set tag（不重复定义）。
// 整条入站都走目标必须显式勾「全部流量」，不会因为条件留空就把整条入站带走。
//
// 规则按顺序写进 fanout-route.json，排在第三方 route.json 之前，先匹配先生效。

// RouteRule 是一条分流规则，存在工作目录的 native.json 里。
type RouteRule struct {
	ID      int    `json:"id"`
	Name    string `json:"name,omitempty"`
	Enabled bool   `json:"enabled"`
	// Inbounds 是入站 tag 列表（配置目录里已有的入站）
	Inbounds []string `json:"inbounds"`
	// Exit 是出口节点主机名经 sanitizeTag 后的形式，对应出站 fanout-exit-<Exit>。
	// 与 Outbound 二选一。
	Exit string `json:"exit"`
	// Outbound 是配置目录里已有的出站 / 端点 tag（direct、warp 等）。与 Exit 二选一。
	Outbound string `json:"outbound,omitempty"`
	// Domains 是规范化后的域名条目：
	//   example.com        domain_suffix（含自身和所有子域）
	//   .example.com       domain_suffix（只匹配子域）
	//   full:a.example.com domain
	//   keyword:netflix    domain_keyword
	//   regex:^.+\.cn$     domain_regex
	//   1.2.3.0/24         ip_cidr
	Domains  []string     `json:"domains,omitempty"`
	RuleSets []RuleSetRef `json:"rule_sets,omitempty"`
	// LocalRuleSets 是直接引用的、配置目录里已有的 route.rule_set tag
	LocalRuleSets []string `json:"local_rule_sets,omitempty"`
	// All 表示这条规则不看条件，入站的全部流量都走出口。必须显式勾选。
	All bool `json:"all,omitempty"`
	// ResolveIP 表示先把域名解析成 IP 再匹配一遍 IP 类条件。
	// 有 ip_cidr 条目或 geoip: 规则集时自动开启；自定义规则集里有 IP 段时手动勾。
	ResolveIP bool `json:"resolve_ip,omitempty"`
}

// RuleSetRef 是规则里引用的一个规则集。
type RuleSetRef struct {
	// Source 是 geosite:<名字>、geoip:<名字> 简写，或 http(s) 地址
	Source string `json:"source"`
	// Format 是 binary / source；留空按地址扩展名判断（.srs / .json）
	Format string `json:"format,omitempty"`
}

// RuleInput 是界面提交的一条规则（新建或修改）。
type RuleInput struct {
	ID       int          `json:"id"`
	Name     string       `json:"name"`
	Enabled  *bool        `json:"enabled"`
	Inbounds []string     `json:"inbounds"`
	Exit     string       `json:"exit"`     // 出口的节点主机名；与 Outbound 二选一
	Outbound string       `json:"outbound"` // 已有出站 / 端点的 tag；与 Exit 二选一
	Domains  string       `json:"domains"`  // 文本框原文：一行一个，也容忍逗号和空格
	RuleSets []RuleSetRef `json:"rule_sets"`
	// LocalRuleSets 是勾选的已有规则集 tag
	LocalRuleSets []string `json:"local_rule_sets"`
	All           bool     `json:"all"`
	// ResolveIP 见 RouteRule.ResolveIP
	ResolveIP bool `json:"resolve_ip"`
}

const (
	ruleSetTagPrefix = fanoutTagPrefix + "rs-"
	// 规则集的更新间隔。sing-box 自己按这个周期重新下载
	ruleSetUpdateInterval = "1d"

	geositeURL = "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-%s.srs"
	geoipURL   = "https://raw.githubusercontent.com/SagerNet/sing-geoip/rule-set/geoip-%s.srs"
)

var geoNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9!@._-]*$`)

// ---- 域名条目 ----

// splitEntries 把文本框内容拆成条目：按行、逗号、分号、空白切，# 开头的行当注释。
func splitEntries(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		out = append(out, strings.FieldsFunc(line, func(r rune) bool {
			return r == ',' || r == ';' || r == '，' || r == '；' || r == '、' || unicode.IsSpace(r)
		})...)
	}
	return out
}

// parseDomainEntries 把文本框内容转成规范化的域名条目。
//
// 顺手收下误填进来的 geosite:/geoip: 简写和 .srs/.json 地址，转成规则集，
// 免得用户因为填错了框而被拒绝。
func parseDomainEntries(text string) (domains []string, sets []RuleSetRef, err error) {
	seen := map[string]bool{}
	add := func(d string) {
		if !seen[d] {
			seen[d] = true
			domains = append(domains, d)
		}
	}
	for _, raw := range splitEntries(text) {
		d, rs, err := normalizeDomainEntry(raw)
		if err != nil {
			return nil, nil, err
		}
		if rs != nil {
			sets = append(sets, *rs)
			continue
		}
		add(d)
	}
	return domains, sets, nil
}

func normalizeDomainEntry(raw string) (string, *RuleSetRef, error) {
	entry := strings.TrimSpace(raw)
	prefix, value := "", entry
	if i := strings.Index(entry, ":"); i > 0 {
		p := strings.ToLower(entry[:i])
		switch p {
		case "full", "domain", "suffix", "keyword", "regex", "regexp", "ip", "cidr", "ip_cidr", "geosite", "geoip":
			prefix, value = p, strings.TrimSpace(entry[i+1:])
		}
	}
	if value == "" {
		return "", nil, fmt.Errorf("条目 %q 是空的", raw)
	}
	switch prefix {
	case "geosite", "geoip":
		return "", &RuleSetRef{Source: prefix + ":" + strings.ToLower(value)}, nil
	case "regex", "regexp":
		if _, err := regexp.Compile(value); err != nil {
			return "", nil, fmt.Errorf("正则 %q 写得不对: %v", value, err)
		}
		return "regex:" + value, nil, nil
	case "keyword":
		v := strings.ToLower(value)
		if strings.ContainsAny(v, " \t/") {
			return "", nil, fmt.Errorf("关键字 %q 里不能有空格或斜杠", value)
		}
		return "keyword:" + v, nil, nil
	case "ip", "cidr", "ip_cidr":
		c, ok := normalizeCIDR(value)
		if !ok {
			return "", nil, fmt.Errorf("%q 不是合法的 IP 或 CIDR", value)
		}
		return c, nil, nil
	}

	// 没有前缀：可能是网址、IP/CIDR 或域名
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		u, err := url.Parse(value)
		if err != nil || u.Host == "" {
			return "", nil, fmt.Errorf("%q 不是合法的网址", value)
		}
		ext := strings.ToLower(path.Ext(u.Path))
		if prefix == "" && (ext == ".srs" || ext == ".json") {
			return "", &RuleSetRef{Source: value}, nil
		}
		lower = strings.ToLower(u.Hostname())
	}
	if prefix == "" {
		if c, ok := normalizeCIDR(lower); ok {
			return c, nil, nil
		}
	}
	d, err := normalizeHost(lower)
	if err != nil {
		return "", nil, err
	}
	if prefix == "full" {
		return "full:" + strings.TrimPrefix(d, "."), nil, nil
	}
	return d, nil, nil
}

// normalizeHost 规范化一个域名后缀。"*.a.com" 写法转成 ".a.com"（只匹配子域）。
func normalizeHost(s string) (string, error) {
	orig := s
	s = strings.TrimSuffix(s, ".")
	lead := ""
	if strings.HasPrefix(s, "*.") {
		s, lead = s[2:], "."
	} else if strings.HasPrefix(s, ".") {
		s, lead = s[1:], "."
	}
	if s == "" {
		return "", fmt.Errorf("%q 不是合法的域名", orig)
	}
	for _, r := range s {
		ok := r == '.' || r == '-' || r == '_' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r > 0x7f
		if !ok {
			return "", fmt.Errorf("%q 不是合法的域名（需要通配请用 keyword: 或 regex: 前缀）", orig)
		}
	}
	if strings.Contains(s, "..") {
		return "", fmt.Errorf("%q 不是合法的域名", orig)
	}
	return lead + s, nil
}

// normalizeCIDR 把 IP 或 CIDR 规范化成 CIDR，裸 IP 补成 /32 或 /128。
func normalizeCIDR(s string) (string, bool) {
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return "", false
		}
		return p.Masked().String(), true
	}
	ip := net.ParseIP(s)
	if ip == nil {
		return "", false
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String() + "/32", true
	}
	return ip.String() + "/128", true
}

// domainFields 把规范化条目分到 sing-box 的各个匹配字段里。
func domainFields(entries []string) map[string][]string {
	f := map[string][]string{}
	for _, e := range entries {
		switch {
		case strings.HasPrefix(e, "full:"):
			f["domain"] = append(f["domain"], strings.TrimPrefix(e, "full:"))
		case strings.HasPrefix(e, "keyword:"):
			f["domain_keyword"] = append(f["domain_keyword"], strings.TrimPrefix(e, "keyword:"))
		case strings.HasPrefix(e, "regex:"):
			f["domain_regex"] = append(f["domain_regex"], strings.TrimPrefix(e, "regex:"))
		case strings.Contains(e, "/"):
			f["ip_cidr"] = append(f["ip_cidr"], e)
		default:
			f["domain_suffix"] = append(f["domain_suffix"], e)
		}
	}
	return f
}

// ---- 规则集 ----

// resolvedRuleSet 是展开后的规则集：tag、下载地址、格式。
type resolvedRuleSet struct {
	Tag    string
	URL    string
	Format string // binary | source
	IP     bool   // geoip：按 IP 匹配
	Domain bool   // geosite：只有域名，解析之后不必再匹配
	// cacheName 是预下载文件名用的 tag（跟第三方撞名加后缀之前的那个）
	cacheName string
}

// normalizeRuleSetRef 校验并规范化一个规则集引用。
func normalizeRuleSetRef(r RuleSetRef) (RuleSetRef, error) {
	src := strings.TrimSpace(r.Source)
	format := strings.ToLower(strings.TrimSpace(r.Format))
	switch format {
	case "", "auto":
		format = ""
	case "binary", "srs":
		format = "binary"
	case "source", "json":
		format = "source"
	default:
		return r, fmt.Errorf("规则集格式只能是 binary 或 source，收到 %q", r.Format)
	}
	low := strings.ToLower(src)
	for _, kind := range []string{"geosite:", "geoip:"} {
		if strings.HasPrefix(low, kind) {
			name := strings.TrimSpace(low[len(kind):])
			if !geoNameRe.MatchString(name) {
				return r, fmt.Errorf("%q 的名字不合法，形如 geosite:netflix、geoip:jp", src)
			}
			// SagerNet 的 geosite/geoip 规则集只有 .srs
			return RuleSetRef{Source: kind + name}, nil
		}
	}
	u, err := url.Parse(src)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return r, fmt.Errorf("规则集 %q 既不是 geosite:/geoip: 简写，也不是 http(s) 地址", src)
	}
	if format == "" {
		switch strings.ToLower(path.Ext(u.Path)) {
		case ".srs":
		case ".json":
		default:
			return r, fmt.Errorf("规则集 %s 看不出格式（不是 .srs / .json 结尾），请选 binary 或 source", src)
		}
	}
	return RuleSetRef{Source: src, Format: format}, nil
}

// resolveRuleSet 把规则集引用展开成 sing-box 需要的字段。引用必须已经规范化。
func resolveRuleSet(r RuleSetRef) resolvedRuleSet {
	low := strings.ToLower(r.Source)
	switch {
	case strings.HasPrefix(low, "geosite:"):
		name := low[len("geosite:"):]
		return withCache(resolvedRuleSet{Tag: ruleSetTag("geosite-"+name, r.Source), URL: fmt.Sprintf(geositeURL, name), Format: "binary", Domain: true})
	case strings.HasPrefix(low, "geoip:"):
		name := low[len("geoip:"):]
		return withCache(resolvedRuleSet{Tag: ruleSetTag("geoip-"+name, r.Source), URL: fmt.Sprintf(geoipURL, name), Format: "binary", IP: true})
	}
	format := r.Format
	if format == "" {
		if strings.EqualFold(path.Ext(urlPath(r.Source)), ".json") {
			format = "source"
		} else {
			format = "binary"
		}
	}
	base := strings.TrimSuffix(path.Base(urlPath(r.Source)), path.Ext(urlPath(r.Source)))
	if base == "" || base == "." || base == "/" {
		base = "url"
	}
	// 地址各不相同但文件名常常一样（geosite-netflix.srs 有好几家镜像），带上哈希
	return withCache(resolvedRuleSet{Tag: ruleSetTag(base+"-"+ruleHash(r.Source+"|"+format), ""), URL: r.Source, Format: format})
}

func withCache(rs resolvedRuleSet) resolvedRuleSet {
	rs.cacheName = rs.Tag
	return rs
}

func urlPath(s string) string {
	if u, err := url.Parse(s); err == nil {
		return u.Path
	}
	return s
}

// ruleSetTag 生成 fanout-rs- 前缀的 tag。名字收敛后跟原来不一样（比如 geolocation-!cn）
// 时补一段哈希，免得两个不同的名字收敛成同一个 tag。
func ruleSetTag(name, orig string) string {
	clean := sanitizeTag(strings.ToLower(name))
	if orig != "" && clean != strings.ToLower(name) {
		clean += "-" + ruleHash(orig)
	}
	return ruleSetTagPrefix + clean
}

func ruleHash(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:3])
}

// ---- 规则校验 ----

// ruleEnv 是校验规则时可选的东西。
type ruleEnv struct {
	Inbounds  map[string]bool // 配置目录里现有的入站 tag
	Exits     map[string]bool // fanout 的出口（sanitizeTag 后的主机名）
	Outbounds map[string]bool // 配置目录里已有的、能当目标的出站 / 端点 tag
	RuleSets  map[string]bool // 配置目录里已有的规则集 tag
}

// normalizeRule 校验界面提交的规则并转成存盘形态。
//
// prev 是修改前的规则（新建时为 nil）：出口停掉、已有出站或规则集被别的脚本删掉之后，
// 仍允许保存这条规则的其它改动（生成配置时会跳过不存在的引用）。
func normalizeRule(in RuleInput, env ruleEnv, prev *RouteRule) (*RouteRule, error) {
	r := &RouteRule{ID: in.ID, Enabled: true, All: in.All, ResolveIP: in.ResolveIP}
	if in.Enabled != nil {
		r.Enabled = *in.Enabled
	} else if prev != nil {
		r.Enabled = prev.Enabled
	}
	r.Name = strings.TrimSpace(in.Name)
	if len([]rune(r.Name)) > 64 {
		return nil, fmt.Errorf("规则名太长（最多 64 个字）")
	}

	seen := map[string]bool{}
	for _, tag := range in.Inbounds {
		tag = strings.TrimSpace(tag)
		if tag == "" || seen[tag] {
			continue
		}
		if !env.Inbounds[tag] {
			return nil, fmt.Errorf("入站 %s 不存在（fanout 只能使用配置目录里已有的入站）", tag)
		}
		seen[tag] = true
		r.Inbounds = append(r.Inbounds, tag)
	}
	if len(r.Inbounds) == 0 {
		return nil, fmt.Errorf("至少选一个入站")
	}

	exit, outbound := strings.TrimSpace(in.Exit), strings.TrimSpace(in.Outbound)
	switch {
	case exit != "" && outbound != "":
		return nil, fmt.Errorf("出口和已有出站只能选一个")
	case exit != "":
		r.Exit = sanitizeTag(exit)
		if !env.Exits[r.Exit] && (prev == nil || prev.Exit != r.Exit) {
			return nil, fmt.Errorf("出口 %s 不存在，先在上面开出口", exit)
		}
	case outbound != "":
		if strings.HasPrefix(outbound, fanoutTagPrefix) {
			return nil, fmt.Errorf("%s 是 fanout 自己的出站，请在「fanout 出口」里选", outbound)
		}
		if !env.Outbounds[outbound] && (prev == nil || prev.Outbound != outbound) {
			return nil, fmt.Errorf("出站 %s 在配置目录里不存在", outbound)
		}
		r.Outbound = outbound
	default:
		return nil, fmt.Errorf("要选一个出口或已有出站")
	}

	domains, extra, err := parseDomainEntries(in.Domains)
	if err != nil {
		return nil, err
	}
	r.Domains = domains

	seenRS := map[string]bool{}
	for _, ref := range append(append([]RuleSetRef{}, in.RuleSets...), extra...) {
		if strings.TrimSpace(ref.Source) == "" {
			continue
		}
		n, err := normalizeRuleSetRef(ref)
		if err != nil {
			return nil, err
		}
		key := strings.ToLower(n.Source) + "|" + n.Format
		if seenRS[key] {
			continue
		}
		seenRS[key] = true
		r.RuleSets = append(r.RuleSets, n)
	}
	prevLocal := map[string]bool{}
	if prev != nil {
		for _, t := range prev.LocalRuleSets {
			prevLocal[t] = true
		}
	}
	seenLocal := map[string]bool{}
	for _, tag := range in.LocalRuleSets {
		tag = strings.TrimSpace(tag)
		if tag == "" || seenLocal[tag] {
			continue
		}
		if !env.RuleSets[tag] && !prevLocal[tag] {
			return nil, fmt.Errorf("规则集 %s 在配置目录里不存在", tag)
		}
		seenLocal[tag] = true
		r.LocalRuleSets = append(r.LocalRuleSets, tag)
	}
	if len(r.RuleSets)+len(r.LocalRuleSets) > 32 {
		return nil, fmt.Errorf("一条规则最多 32 个规则集")
	}

	hasCond := len(r.Domains) > 0 || len(r.RuleSets) > 0 || len(r.LocalRuleSets) > 0
	if r.All && hasCond {
		return nil, fmt.Errorf("勾了「全部流量」就不要再填域名或规则集；只想分流部分流量请取消勾选")
	}
	if !r.All && !hasCond {
		return nil, fmt.Errorf("至少填一个域名或规则集；确实要把整条入站都送过去，请勾选「全部流量」")
	}
	if r.All {
		r.ResolveIP = false
	}
	return r, nil
}

// needsResolve 判断这条规则要不要先解析域名再匹配 IP 类条件。
func (r *RouteRule) needsResolve() bool {
	if r.All {
		return false
	}
	if r.ResolveIP {
		return true
	}
	for _, d := range r.Domains {
		if strings.Contains(d, "/") && !strings.HasPrefix(d, "regex:") {
			return true
		}
	}
	for _, rs := range r.RuleSets {
		if resolveRuleSet(rs).IP {
			return true
		}
	}
	return false
}

// ---- 生成 sing-box 路由 ----

// buildOptions 是生成路由时依赖的环境信息。
type buildOptions struct {
	// RuleSetDir 存放 fanout 预先下载好的规则集文件，存在时写进 initial_path，
	// sing-box 启动时下载不到也能用它起来。空表示不写。
	RuleSetDir string
	// LegacyRuleSet 表示 sing-box 早于 1.14：没有 http_client / initial_path，
	// 只能用 download_detour。
	LegacyRuleSet bool
	// ForeignRuleSetTags 是别的脚本已经定义的规则集 tag，fanout 的 tag 要避开
	ForeignRuleSetTags map[string]bool
	// Foreign 是配置目录里别的文件定义的出站、规则集和 final
	Foreign foreignConf
	// DirectTag 是直连出站的 tag（已有的 direct，没有时是 fanout-direct），由 buildFanoutFiles 填
	DirectTag string
}

// buildRouteRules 由分流规则生成 route.rules 与 route.rule_set。
//
// live 是连通出口：sanitizeTag(主机名) -> 出站 tag。existing 是配置目录里现有的入站 tag。
// 出口没连通、禁用、入站全不在了的规则直接跳过（流量保持原走向），保证配置一定能过 check。
// 目标是已有出站的规则不看隧道，始终生效；已有出站或引用的已有规则集在配置目录里不见了时，
// 跳过（或去掉那个规则集）并记日志。
func buildRouteRules(rules []*RouteRule, live map[string]string, existing map[string]bool, opts buildOptions) (outRules []any, outSets []any) {
	type activeRule struct {
		r        *RouteRule
		inbounds []any
		outbound string
		sets     []resolvedRuleSet
		// local 是引用的已有规则集；localIP 是其中需要解析后再匹配一遍的
		local, localIP []string
	}
	foreignOut := opts.Foreign.outboundTags()
	var active []activeRule
	sniffSeen := map[string]bool{}
	var sniff []string

	// 规则集 tag 按来源去重；跟第三方撞名时加后缀
	setTag := map[string]string{}
	usedTag := map[string]bool{}
	for t := range opts.ForeignRuleSetTags {
		usedTag[t] = true
	}
	for t := range opts.Foreign.ruleSetTags() {
		usedTag[t] = true
	}
	var setDefs []any

	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		var outbound string
		if r.Outbound != "" {
			if !foreignOut[r.Outbound] {
				log.Printf("分流规则 %s 跳过：出站 %s 在配置目录里已经不存在", ruleName(r), r.Outbound)
				continue
			}
			outbound = r.Outbound
		} else {
			o, ok := live[r.Exit]
			if !ok {
				continue
			}
			outbound = o
		}
		var tags []string
		for _, t := range r.Inbounds {
			if existing[t] {
				tags = append(tags, t)
			}
		}
		if len(tags) == 0 {
			continue
		}
		a := activeRule{r: r, inbounds: toAnySlice(tags), outbound: outbound}
		if !r.All {
			for _, t := range r.LocalRuleSets {
				frs, ok := opts.Foreign.ruleSet(t)
				if !ok {
					log.Printf("分流规则 %s：规则集 %s 在配置目录里已经不存在，先不引用它", ruleName(r), t)
					continue
				}
				a.local = append(a.local, t)
				if ip, domain := frs.kind(); ip || (!domain && r.ResolveIP) {
					a.localIP = append(a.localIP, t)
				}
			}
		}
		if !r.All && len(r.Domains) == 0 && len(r.RuleSets) == 0 && len(a.local) == 0 {
			// 老数据、手改出来的、或引用的规则集都不在了的空条件规则，绝不能退化成整条入站转发
			if len(r.LocalRuleSets) > 0 {
				log.Printf("分流规则 %s 跳过：引用的规则集都不在了", ruleName(r))
			}
			continue
		}
		for _, ref := range r.RuleSets {
			rs := resolveRuleSet(ref)
			key := rs.URL + "|" + rs.Format
			if tag, ok := setTag[key]; ok {
				rs.Tag = tag
			} else {
				base := rs.Tag
				for i := 2; usedTag[rs.Tag]; i++ {
					rs.Tag = fmt.Sprintf("%s-%d", base, i)
				}
				usedTag[rs.Tag] = true
				setTag[key] = rs.Tag
				setDefs = append(setDefs, ruleSetDef(rs, opts))
			}
			a.sets = append(a.sets, rs)
		}
		active = append(active, a)
		if !r.All {
			for _, t := range tags {
				if !sniffSeen[t] {
					sniffSeen[t] = true
					sniff = append(sniff, t)
				}
			}
		}
	}

	outRules = []any{}
	// 域名匹配要靠嗅探拿到 SNI / Host。sing-box 1.11 起嗅探是路由动作，
	// 对已经嗅探过的连接再嗅一次没有副作用，也不改目标地址，不影响第三方的规则。
	if len(sniff) > 0 {
		outRules = append(outRules, map[string]any{"inbound": toAnySlice(sniff), "action": "sniff"})
	}
	for _, a := range active {
		route := func(m map[string]any) map[string]any {
			m["inbound"] = a.inbounds
			m["action"] = "route"
			m["outbound"] = a.outbound
			return m
		}
		if a.r.All {
			outRules = append(outRules, route(map[string]any{}))
			continue
		}
		fields := domainFields(a.r.Domains)
		if len(fields) > 0 {
			m := map[string]any{}
			for _, k := range []string{"domain", "domain_suffix", "domain_keyword", "domain_regex", "ip_cidr"} {
				if len(fields[k]) > 0 {
					m[k] = toAnySlice(fields[k])
				}
			}
			outRules = append(outRules, route(m))
		}
		setTags := append([]string{}, a.local...)
		ipSetTags := append([]string{}, a.localIP...)
		for _, rs := range a.sets {
			setTags = append(setTags, rs.Tag)
			// 解析之后只需再匹配可能含 IP 段的规则集：geoip，以及勾了「含 IP 段」的自定义规则集
			if rs.IP || (!rs.Domain && a.r.ResolveIP) {
				ipSetTags = append(ipSetTags, rs.Tag)
			}
		}
		// 域名规则和规则集分成两条：同一条里规则集是否与域名字段"合并"取决于规则集的形状，
		// 分开写语义最清楚，两条都送到同一个出口
		if len(setTags) > 0 {
			outRules = append(outRules, route(map[string]any{"rule_set": toAnySlice(setTags)}))
		}
		if a.r.needsResolve() || len(a.localIP) > 0 {
			// 目标是域名时 IP 类条件（ip_cidr、geoip）匹配不到，先解析再匹配一遍。
			// 解析只作用于这些入站里前面没命中的流量；prefer_ipv4 照顾隧道里没有 IPv6。
			outRules = append(outRules, map[string]any{"inbound": a.inbounds, "action": "resolve", "strategy": "prefer_ipv4"})
			if cidr := fields["ip_cidr"]; len(cidr) > 0 {
				outRules = append(outRules, route(map[string]any{"ip_cidr": toAnySlice(cidr)}))
			}
			if len(ipSetTags) > 0 {
				outRules = append(outRules, route(map[string]any{"rule_set": toAnySlice(ipSetTags)}))
			}
		}
	}
	return outRules, setDefs
}

func ruleName(r *RouteRule) string {
	if r.Name != "" {
		return fmt.Sprintf("#%d「%s」", r.ID, r.Name)
	}
	return fmt.Sprintf("#%d", r.ID)
}

// ruleSetDef 生成一个 route.rule_set 条目。
func ruleSetDef(rs resolvedRuleSet, opts buildOptions) map[string]any {
	def := map[string]any{
		"type":            "remote",
		"tag":             rs.Tag,
		"format":          rs.Format,
		"url":             rs.URL,
		"update_interval": ruleSetUpdateInterval,
	}
	if opts.LegacyRuleSet {
		// 1.14 之前只有 download_detour，指向直连出站（已有的 direct，没有时是 fanout-direct）
		d := opts.DirectTag
		if d == "" {
			d = fanoutDirectTag
		}
		def["download_detour"] = d
		return def
	}
	// 1.14 起 download_detour 废弃，换成 http_client。
	// 写成内联对象并带一个字段：空对象会被当成"没配"，退回已废弃的隐式默认客户端；
	// 也不能 detour 到 direct 出站（sing-box 拒绝 detour 到空的 direct 出站）。
	// 不带 detour 的内联客户端就是从本机直连下载，和直连出站一样。
	def["http_client"] = map[string]any{"connect_timeout": "30s"}
	if opts.RuleSetDir != "" {
		if p := ruleSetCachePath(opts.RuleSetDir, rs); fileExists(p) {
			// 启动时还没有缓存就先用 fanout 预下载的这份，下载失败也不会让 sing-box 起不来
			def["initial_path"] = p
		}
	}
	return def
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir() && st.Size() > 0
}

// ruleSetCachePath 是规则集预下载文件的位置。
func ruleSetCachePath(dir string, rs resolvedRuleSet) string {
	ext := ".srs"
	if rs.Format == "source" {
		ext = ".json"
	}
	name := rs.cacheName
	if name == "" {
		name = rs.Tag
	}
	return filepath.Join(dir, name+ext)
}

// ruleSetDir 是规则集预下载目录：放在 sing-box 自己的目录下（不在 -C 加载的配置目录里，
// 免得 .json 规则集被当成配置合并；也保证以别的用户跑的 sing-box 读得到）。
func ruleSetDir() string { return filepath.Join(filepath.Dir(singbox.Bin), "fanout-rulesets") }

// fetchURL 下载 url 到 dst。走母机网络命名空间里的 curl（见 netnsguard.go），测试里可替换。
var fetchURL = func(rawURL, dst string) error {
	out, err := cmdCombined(exec.Command("curl", "-fsSL", "--max-time", "30", "--retry", "1", "-o", dst, rawURL))
	if err != nil {
		return fmt.Errorf("%s", trimOutput(out))
	}
	return nil
}

// prefetchRuleSets 把规则集先下载一份并校验。
//
// 两个目的：一是保存规则时就发现地址写错（404、不是规则集文件），而不是等 sing-box
// 启动时下载失败直接起不来；二是留一份本地文件作 initial_path。
// 已经下过的不再下载，sing-box 自己会按 update_interval 更新。
func prefetchRuleSets(dir string, refs []RuleSetRef) error {
	if len(refs) == 0 {
		return nil
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("创建规则集目录失败: %w", err)
	}
	for _, ref := range refs {
		rs := resolveRuleSet(ref)
		dst := ruleSetCachePath(dir, rs)
		if fileExists(dst) {
			continue
		}
		tmp := dst + ".tmp"
		if err := fetchURL(rs.URL, tmp); err != nil {
			os.Remove(tmp)
			return fmt.Errorf("规则集 %s 下载失败（%s）: %v", ref.Source, rs.URL, err)
		}
		if err := validateRuleSetFile(tmp, rs.Format); err != nil {
			os.Remove(tmp)
			return fmt.Errorf("规则集 %s（%s）不可用: %v", ref.Source, rs.URL, err)
		}
		_ = os.Chmod(tmp, 0644)
		if err := os.Rename(tmp, dst); err != nil {
			return err
		}
	}
	return nil
}

// validateRuleSetFile 粗查文件确实是对应格式的规则集。
func validateRuleSetFile(p, format string) error {
	blob, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	if len(blob) == 0 {
		return fmt.Errorf("下载到的是空文件")
	}
	if format == "binary" {
		if len(blob) < 4 || string(blob[:3]) != "SRS" {
			return fmt.Errorf("不是 sing-box 二进制规则集（.srs）；源码格式请选 source")
		}
		return nil
	}
	var doc struct {
		Version *int              `json:"version"`
		Rules   []json.RawMessage `json:"rules"`
	}
	if err := json.Unmarshal(blob, &doc); err != nil || doc.Version == nil {
		return fmt.Errorf("不是 sing-box 源码规则集（JSON，含 version 和 rules）")
	}
	return nil
}

// pruneRuleSetCache 删掉不再被任何规则引用的预下载文件。
func pruneRuleSetCache(dir string, rules []*RouteRule) {
	keep := map[string]bool{}
	for _, r := range rules {
		for _, ref := range r.RuleSets {
			keep[filepath.Base(ruleSetCachePath(dir, resolveRuleSet(ref)))] = true
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), ruleSetTagPrefix) && !keep[e.Name()] {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
