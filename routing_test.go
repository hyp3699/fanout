package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseDomainEntries(t *testing.T) {
	text := `
# 注释行
Netflix.com, nflxvideo.net；*.nflximg.net
full:WWW.Example.com   keyword:Disney  regex:^.+\.Hulu\.com$
1.2.3.4  10.0.0.0/8 ip:2001:db8::1
https://www.youtube.com/watch?v=1
geosite:Netflix
https://example.com/rules/my.srs
`
	domains, sets, err := parseDomainEntries(text)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"netflix.com", "nflxvideo.net", ".nflximg.net", "full:www.example.com",
		"keyword:disney", `regex:^.+\.Hulu\.com$`, "1.2.3.4/32", "10.0.0.0/8", "2001:db8::1/128", "www.youtube.com"}
	if strings.Join(domains, " ") != strings.Join(want, " ") {
		t.Errorf("domains =\n%v\nwant\n%v", domains, want)
	}
	if len(sets) != 2 || sets[0].Source != "geosite:netflix" || sets[1].Source != "https://example.com/rules/my.srs" {
		t.Errorf("误填进域名框的规则集应当被收下: %+v", sets)
	}

	f := domainFields(domains)
	if strings.Join(f["domain_suffix"], ",") != "netflix.com,nflxvideo.net,.nflximg.net,www.youtube.com" ||
		strings.Join(f["domain"], ",") != "www.example.com" ||
		strings.Join(f["domain_keyword"], ",") != "disney" ||
		len(f["domain_regex"]) != 1 ||
		strings.Join(f["ip_cidr"], ",") != "1.2.3.4/32,10.0.0.0/8,2001:db8::1/128" {
		t.Errorf("字段归类不对: %v", f)
	}

	for _, bad := range []string{"regex:([", "exa$mple", "a..b", "ip:999.1.1.1", "foo/bar"} {
		if _, _, err := parseDomainEntries(bad); err == nil {
			t.Errorf("%q 应当被拒绝", bad)
		}
	}
	// 重复的条目去重
	d, _, _ := parseDomainEntries("a.com\nA.com, a.com.")
	if len(d) != 1 {
		t.Errorf("去重失败: %v", d)
	}
}

func TestNormalizeRuleSetRef(t *testing.T) {
	ok := []struct {
		in   RuleSetRef
		url  string
		fmt  string
		tagp string
	}{
		{RuleSetRef{Source: "geosite:Netflix"}, "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-netflix.srs", "binary", "fanout-rs-geosite-netflix"},
		{RuleSetRef{Source: "geoip:jp"}, "https://raw.githubusercontent.com/SagerNet/sing-geoip/rule-set/geoip-jp.srs", "binary", "fanout-rs-geoip-jp"},
		{RuleSetRef{Source: "https://x.com/a/my-list.json"}, "https://x.com/a/my-list.json", "source", "fanout-rs-my-list-"},
		{RuleSetRef{Source: "https://x.com/rules", Format: "binary"}, "https://x.com/rules", "binary", "fanout-rs-rules-"},
		{RuleSetRef{Source: "geosite:geolocation-!cn"}, "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-geolocation-!cn.srs", "binary", "fanout-rs-geosite-geolocation--cn-"},
	}
	for _, c := range ok {
		n, err := normalizeRuleSetRef(c.in)
		if err != nil {
			t.Errorf("%+v: %v", c.in, err)
			continue
		}
		rs := resolveRuleSet(n)
		if rs.URL != c.url || rs.Format != c.fmt || !strings.HasPrefix(rs.Tag, c.tagp) {
			t.Errorf("%+v => %+v", c.in, rs)
		}
	}
	for _, bad := range []RuleSetRef{
		{Source: "geosite:"}, {Source: "geosite:a b"}, {Source: "ftp://x.com/a.srs"},
		{Source: "https://x.com/rules"}, // 看不出格式
		{Source: "https://x.com/a.srs", Format: "yaml"}, {Source: "netflix"},
	} {
		if _, err := normalizeRuleSetRef(bad); err == nil {
			t.Errorf("%+v 应当被拒绝", bad)
		}
	}
}

func TestNormalizeRuleConditions(t *testing.T) {
	known := map[string]bool{"a": true, "b": true}
	exits := map[string]bool{"jp": true}
	base := RuleInput{Users: []string{"a", "a", "b"}, Exit: "jp"}

	if _, err := normalizeRule(base, ruleEnv{Users: known, Exits: exits}, nil); err == nil {
		t.Error("没有条件也没勾全部流量，必须拒绝")
	}
	all := base
	all.All = true
	r, err := normalizeRule(all, ruleEnv{Users: known, Exits: exits}, nil)
	if err != nil || !r.All || len(r.Users) != 2 || !r.Enabled {
		t.Errorf("显式全部流量应当允许: %+v %v", r, err)
	}
	both := all
	both.Domains = "a.com"
	if _, err := normalizeRule(both, ruleEnv{Users: known, Exits: exits}, nil); err == nil {
		t.Error("全部流量和条件同时给，应当拒绝")
	}
	noIn := base
	noIn.Users = nil
	noIn.Domains = "a.com"
	if _, err := normalizeRule(noIn, ruleEnv{Users: known, Exits: exits}, nil); err == nil {
		t.Error("没选入站应当拒绝")
	}
	// 出口停掉后仍然可以改这条规则的其它字段
	gone := base
	gone.Exit = "us"
	gone.Domains = "b.com"
	if _, err := normalizeRule(gone, ruleEnv{Users: known, Exits: exits}, nil); err == nil {
		t.Error("新建规则指向不存在的出口应当拒绝")
	}
	if _, err := normalizeRule(gone, ruleEnv{Users: known, Exits: exits}, &RouteRule{Exit: "us"}); err != nil {
		t.Errorf("修改规则时保留原来（已停掉）的出口应当允许: %v", err)
	}
	off := false
	dis := base
	dis.Domains = "a.com"
	dis.Enabled = &off
	if r, _ := normalizeRule(dis, ruleEnv{Users: known, Exits: exits}, nil); r == nil || r.Enabled {
		t.Errorf("enabled=false 应当保留: %+v", r)
	}
}

func liveJP() map[string]string {
	return map[string]string{"jp": "fanout-exit-jp", "us": "fanout-exit-us"}
}

func TestBuildRouteRulesSkipsAndOrders(t *testing.T) {
	existing := map[string]bool{"a": true, "b": true}
	rules := []*RouteRule{
		{ID: 1, Enabled: true, Users: []string{"a"}, Exit: "jp", Domains: []string{"x.com", "1.1.1.0/24"},
			RuleSets: []RuleSetRef{{Source: "geosite:netflix"}, {Source: "geoip:jp"}}},
		{ID: 2, Enabled: true, Users: []string{"b"}, Exit: "kr", Domains: []string{"y.com"}},                          // 出口没连通
		{ID: 3, Enabled: false, Users: []string{"a"}, Exit: "jp", Domains: []string{"z.com"}},                         // 停用
		{ID: 4, Enabled: true, Users: []string{"gone"}, Exit: "jp", Domains: []string{"w.com"}},                       // 入站不在了
		{ID: 5, Enabled: true, Users: []string{"b"}, Exit: "jp"},                                                      // 空条件：绝不整条转发
		{ID: 6, Enabled: true, Users: []string{"b", "gone"}, Exit: "us", All: true},                                   // 显式全部流量
		{ID: 7, Enabled: true, Users: []string{"b"}, Exit: "us", RuleSets: []RuleSetRef{{Source: "geosite:netflix"}}}, // 规则集复用
	}
	out, sets := buildRouteRules(rules, liveJP(), existing, buildOptions{ForeignRuleSetTags: map[string]bool{"fanout-rs-geoip-jp": true}})
	var got []string
	for _, r := range out {
		m := r.(map[string]any)
		s := m["action"].(string) + ":" + strings.Join(toStrings(m["auth_user"]), "+")
		for _, k := range []string{"domain_suffix", "ip_cidr", "rule_set"} {
			if v, ok := m[k]; ok {
				s += " " + k + "=" + strings.Join(toStrings(v), "+")
			}
		}
		if o, ok := m["outbound"]; ok {
			s += " ->" + o.(string)
		}
		if st, ok := m["strategy"]; ok {
			s += " " + st.(string)
		}
		got = append(got, s)
	}
	want := []string{
		"sniff:a+b",
		"route:a domain_suffix=x.com ip_cidr=1.1.1.0/24 ->fanout-exit-jp",
		"route:a rule_set=fanout-rs-geosite-netflix+fanout-rs-geoip-jp-2 ->fanout-exit-jp",
		"resolve:a prefer_ipv4",
		"route:a ip_cidr=1.1.1.0/24 ->fanout-exit-jp",
		"route:a rule_set=fanout-rs-geoip-jp-2 ->fanout-exit-jp",
		"route:b ->fanout-exit-us",
		"route:b rule_set=fanout-rs-geosite-netflix ->fanout-exit-us",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("规则不对:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if len(sets) != 2 {
		t.Fatalf("规则集应去重成 2 个: %v", sets)
	}
	tags := []string{sets[0].(map[string]any)["tag"].(string), sets[1].(map[string]any)["tag"].(string)}
	if tags[0] != "fanout-rs-geosite-netflix" || tags[1] != "fanout-rs-geoip-jp-2" {
		t.Errorf("规则集 tag 不对（要避开第三方已有的 tag）: %v", tags)
	}
}

func TestRuleSetDefLegacyAndModern(t *testing.T) {
	rs := resolveRuleSet(RuleSetRef{Source: "geosite:netflix"})
	dir := t.TempDir()
	if err := os.WriteFile(ruleSetCachePath(dir, rs), []byte("SRS\x01x"), 0644); err != nil {
		t.Fatal(err)
	}
	modern := ruleSetDef(rs, buildOptions{RuleSetDir: dir})
	if modern["download_detour"] != nil || modern["http_client"] == nil || modern["initial_path"] != filepath.Join(dir, "fanout-rs-geosite-netflix.srs") {
		t.Errorf("1.14+ 应当用 http_client + initial_path: %v", modern)
	}
	if hc, _ := modern["http_client"].(map[string]any); len(hc) == 0 || hc["detour"] != nil {
		t.Errorf("http_client 要是非空、不带 detour 的内联对象: %v", modern["http_client"])
	}
	legacy := ruleSetDef(rs, buildOptions{LegacyRuleSet: true, RuleSetDir: dir, DirectTag: "direct"})
	if legacy["download_detour"] != "direct" || legacy["http_client"] != nil || legacy["initial_path"] != nil {
		t.Errorf("1.14 之前只能用 download_detour，指向已有的 direct: %v", legacy)
	}
	if l := ruleSetDef(rs, buildOptions{LegacyRuleSet: true}); l["download_detour"] != fanoutDirectTag {
		t.Errorf("没有已有 direct 时退回 fanout-direct: %v", l)
	}
	if modern["update_interval"] != "1d" || modern["type"] != "remote" {
		t.Errorf("rule_set 基本字段不对: %v", modern)
	}
}

func TestValidateRuleSetFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		_ = os.WriteFile(p, []byte(body), 0600)
		return p
	}
	if err := validateRuleSetFile(write("a.srs", "SRS\x01..."), "binary"); err != nil {
		t.Error(err)
	}
	if err := validateRuleSetFile(write("b.srs", "<html>404</html>"), "binary"); err == nil {
		t.Error("HTML 不是 .srs")
	}
	if err := validateRuleSetFile(write("c.json", `{"version":2,"rules":[{"domain_suffix":["a.com"]}]}`), "source"); err != nil {
		t.Error(err)
	}
	if err := validateRuleSetFile(write("d.json", `{"rules":[]}`), "source"); err == nil {
		t.Error("没有 version 不是源码规则集")
	}
}

func testForeign() foreignConf {
	return foreignConf{
		Outbounds: []ForeignOutbound{
			{Tag: "direct", Type: "direct", File: "outbounds.json"},
			{Tag: "warp-40000", Type: "socks", File: "outbounds.json"},
			{Tag: "blk", Type: "block", File: "outbounds.json"},
			{Tag: "wireguard-out", Type: "wireguard", File: "endpoints.json", Endpoint: true},
		},
		RuleSets: []ForeignRuleSet{
			{Tag: "geosite-openai", Type: "remote", URL: "https://x/geosite-openai.srs", File: "route.json"},
			{Tag: "geoip-cn", Type: "remote", URL: "https://x/geoip-cn.srs", File: "route.json"},
			{Tag: "my-list", Type: "local", Path: "/etc/x.srs", File: "route.json"},
		},
		HasFinal: true,
	}
}

func TestNormalizeRuleTargetsAndLocalRuleSets(t *testing.T) {
	fc := testForeign()
	env := ruleEnv{Users: map[string]bool{"a": true}, Exits: map[string]bool{"jp": true},
		Outbounds: fc.outboundTags(), RuleSets: fc.ruleSetTags()}

	r, err := normalizeRule(RuleInput{Users: []string{"a"}, Outbound: "direct", LocalRuleSets: []string{"geosite-openai", "geosite-openai"}}, env, nil)
	if err != nil || r.Outbound != "direct" || r.Exit != "" || len(r.LocalRuleSets) != 1 {
		t.Fatalf("已有出站 + 已有规则集应当允许: %+v %v", r, err)
	}
	if _, err := normalizeRule(RuleInput{Users: []string{"a"}, Outbound: "wireguard-out", Domains: "a.com"}, env, nil); err != nil {
		t.Errorf("端点也能当目标: %v", err)
	}
	bad := []RuleInput{
		{Users: []string{"a"}, Outbound: "nope", Domains: "a.com"},                        // 不存在的出站
		{Users: []string{"a"}, Outbound: "blk", Domains: "a.com"},                         // block 不能当目标
		{Users: []string{"a"}, Outbound: "fanout-exit-jp", Domains: "a.com"},              // fanout 自己的出站
		{Users: []string{"a"}, Outbound: "direct", Exit: "jp", Domains: "a.com"},          // 两个都选
		{Users: []string{"a"}, Domains: "a.com"},                                          // 一个都没选
		{Users: []string{"a"}, Exit: "jp", LocalRuleSets: []string{"nope"}},               // 不存在的规则集
		{Users: []string{"a"}, Exit: "jp", All: true, LocalRuleSets: []string{"my-list"}}, // 全部流量不能带条件
	}
	for _, in := range bad {
		if _, err := normalizeRule(in, env, nil); err == nil {
			t.Errorf("%+v 应当被拒绝", in)
		}
	}
	// 被别的脚本删掉的出站 / 规则集：修改规则时保留原样允许
	prev := &RouteRule{Outbound: "gone-out", LocalRuleSets: []string{"gone-set"}}
	if _, err := normalizeRule(RuleInput{Users: []string{"a"}, Outbound: "gone-out", LocalRuleSets: []string{"gone-set"}}, env, prev); err != nil {
		t.Errorf("修改规则时保留已经不在的出站 / 规则集应当允许: %v", err)
	}
}

func TestBuildRouteRulesExistingOutboundsAndRuleSets(t *testing.T) {
	existing := map[string]bool{"a": true, "b": true}
	rules := []*RouteRule{
		// 已有出站：没有任何连通出口也照样生效
		{ID: 1, Enabled: true, Users: []string{"a"}, Outbound: "direct", LocalRuleSets: []string{"geosite-openai"}},
		// 出站被删了：跳过
		{ID: 2, Enabled: true, Users: []string{"a"}, Outbound: "gone", Domains: []string{"x.com"}},
		// 引用的规则集全不在了：跳过，绝不退化成整条转发
		{ID: 3, Enabled: true, Users: []string{"b"}, Outbound: "warp-40000", LocalRuleSets: []string{"gone-set"}},
		// 不在的规则集去掉、在的留下；geoip 的已有规则集触发解析
		{ID: 4, Enabled: true, Users: []string{"b"}, Exit: "jp", LocalRuleSets: []string{"gone-set", "geoip-cn"},
			RuleSets: []RuleSetRef{{Source: "geosite:netflix"}}},
		// 端点也能当目标
		{ID: 5, Enabled: true, Users: []string{"b"}, Outbound: "wireguard-out", All: true},
		// 指向没连通出口的规则仍然跳过
		{ID: 6, Enabled: true, Users: []string{"b"}, Exit: "kr", LocalRuleSets: []string{"my-list"}},
	}
	out, sets := buildRouteRules(rules, map[string]string{"jp": "fanout-exit-jp"}, existing, buildOptions{Foreign: testForeign()})
	var got []string
	for _, r := range out {
		m := r.(map[string]any)
		s := m["action"].(string) + ":" + strings.Join(toStrings(m["auth_user"]), "+")
		if v, ok := m["rule_set"]; ok {
			s += " rule_set=" + strings.Join(toStrings(v), "+")
		}
		if o, ok := m["outbound"]; ok {
			s += " ->" + o.(string)
		}
		got = append(got, s)
	}
	want := []string{
		"sniff:a+b",
		"route:a rule_set=geosite-openai ->direct",
		"route:b rule_set=geoip-cn+fanout-rs-geosite-netflix ->fanout-exit-jp",
		"resolve:b",
		"route:b rule_set=geoip-cn ->fanout-exit-jp",
		"route:b ->wireguard-out",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("规则不对:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// 已有规则集只引用、不重复定义
	if len(sets) != 1 || sets[0].(map[string]any)["tag"] != "fanout-rs-geosite-netflix" {
		t.Errorf("只该定义 fanout 自己的规则集: %v", sets)
	}
}

func TestForeignDirectAndDefault(t *testing.T) {
	fc := testForeign()
	if fc.directTag() != "direct" || fc.defaultOutbound() != "direct" {
		t.Errorf("direct / default 不对: %q %q", fc.directTag(), fc.defaultOutbound())
	}
	// 没有叫 direct 的，取第一个 type=direct 的
	fc2 := foreignConf{Outbounds: []ForeignOutbound{{Tag: "warp", Type: "socks"}, {Tag: "d1", Type: "direct"}, {Tag: "d2", Type: "direct"}}}
	if fc2.directTag() != "d1" || fc2.defaultOutbound() != "warp" {
		t.Errorf("direct / default 不对: %q %q", fc2.directTag(), fc2.defaultOutbound())
	}
	// 端点不算默认出站，也不算直连
	fc3 := foreignConf{Outbounds: []ForeignOutbound{{Tag: "wg", Type: "wireguard", Endpoint: true}}}
	if fc3.directTag() != "" || fc3.defaultOutbound() != "" {
		t.Errorf("只有端点时应当都为空: %q %q", fc3.directTag(), fc3.defaultOutbound())
	}
	if len(testForeign().Routable()) != 3 {
		t.Errorf("block 不能出现在可选目标里: %v", testForeign().Routable())
	}
}

func TestBuildFanoutFilesReusesDirect(t *testing.T) {
	tun := &Tunnel{Port: 23456, Status: "up", Node: Node{HostName: "jp"}}
	outs := func(files map[string]any) []string {
		var tags []string
		for _, o := range files[fanoutOutboundsFile].(map[string]any)["outbounds"].([]any) {
			tags = append(tags, o.(map[string]any)["tag"].(string))
		}
		return tags
	}
	route := func(files map[string]any) map[string]any {
		return files[fanoutRouteFile].(map[string]any)["route"].(map[string]any)
	}

	// 有已有 direct、也有 final：只放出口，不写 final
	f := buildFanoutFiles(nil, nil, []*Tunnel{tun}, buildOptions{Foreign: testForeign()})
	if strings.Join(outs(f), ",") != "fanout-exit-jp" {
		t.Errorf("有已有 direct 时不该再建 fanout-direct: %v", outs(f))
	}
	if _, ok := route(f)["final"]; ok {
		t.Errorf("别的文件写了 final 时 fanout 不写: %v", route(f))
	}
	// 没有出口：outbounds 是空数组（不是 null）
	f = buildFanoutFiles(nil, nil, nil, buildOptions{Foreign: testForeign()})
	blob, _ := encodeFiles(f)
	if !strings.Contains(string(blob[fanoutOutboundsFile]), `"outbounds": []`) {
		t.Errorf("没有出口时应当写空数组: %s", blob[fanoutOutboundsFile])
	}
	// 别的文件没写 final：fanout 写 final = 没有 fanout 时本来的默认出站
	fc := testForeign()
	fc.HasFinal = false
	fc.Outbounds = append([]ForeignOutbound{{Tag: "warp-first", Type: "socks", File: "a.json"}}, fc.Outbounds...)
	f = buildFanoutFiles(nil, nil, []*Tunnel{tun}, buildOptions{Foreign: fc})
	if route(f)["final"] != "warp-first" {
		t.Errorf("final 应当保持原来的默认出站: %v", route(f))
	}
	// 目录里一个 direct 出站都没有：退回自建 fanout-direct，排在最前，并当 final
	f = buildFanoutFiles(nil, nil, []*Tunnel{tun}, buildOptions{LegacyRuleSet: true})
	if strings.Join(outs(f), ",") != "fanout-direct,fanout-exit-jp" || route(f)["final"] != fanoutDirectTag {
		t.Errorf("没有已有 direct 时应当自建 fanout-direct: %v %v", outs(f), route(f))
	}
}
