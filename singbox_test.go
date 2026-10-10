package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// legacyRealityInbound 是老版本 fanout 自建入站留下的文件内容（vless + REALITY）。
func legacyRealityInbound(priv string) string {
	return `{"inbounds": [{"type": "vless", "tag": "fanout-in-20000-tcp", "listen": "::", "listen_port": 20000,
  "users": [{"name": "v", "uuid": "22222222-2222-4222-8222-222222222222", "flow": "xtls-rprx-vision"}],
  "tls": {"enabled": true, "server_name": "www.tesla.com",
    "reality": {"enabled": true, "handshake": {"server": "www.tesla.com", "server_port": 443},
      "private_key": "` + priv + `", "short_id": ["abcd1234"]}}}]}`
}

// sbFixture 仿照目标机上 sb.sh 写的 /etc/sing-box/conf：每个文件只包一个顶层键。
// 值都是假的，结构照抄。外加一个老版本 fanout 留下的 fanout-in-*.json。
func sbFixture(t *testing.T) (dir, cert, key, realityPub string) {
	t.Helper()
	dir = t.TempDir()
	cert, key = testSelfSignedCert(t, "bing.com")
	priv, pub := testRealityKeys(t)
	files := map[string]string{
		"config.json": `{
  "log": {"level": "warn", "timestamp": true},
  "dns": {"servers": [{"type": "local", "tag": "local"}]},
  "experimental": {"v2ray_api": {"listen": "127.0.0.1:9094", "stats": {"enabled": true, "users": ["u1", "u2"]}}}
}`,
		"endpoints.json": `{"endpoints": [{"type": "wireguard", "tag": "wireguard-out", "address": ["172.16.0.2/32"], "private_key": "x", "peers": []}]}`,
		"outbounds.json": `{"outbounds": [
  {"type": "direct", "tag": "direct"},
  {"type": "socks", "tag": "warp-40000", "server": "127.0.0.1", "server_port": 40000}
]}`,
		"route.json": `{"route": {"rule_set": [{"type": "remote", "tag": "geosite-cn", "url": "https://example.com/cn.srs"}], "rules": [], "final": "direct", "default_http_client": "x"}}`,
		"anytls-1.json": `{"inbounds": [{
  "type": "anytls", "tag": "anytls-1", "listen": "::", "listen_port": 31001,
  "users": [{"name": "u1", "password": "pa1"}, {"name": "u2", "password": "pa2"}],
  "tls": {"enabled": true, "certificate_path": "` + cert + `", "key_path": "` + key + `"}
}]}`,
		"tuic-1.json": `{"inbounds": [{
  "type": "tuic", "tag": "tuic-1", "listen": "::", "listen_port": 31002,
  "users": [{"name": "u1", "uuid": "11111111-1111-4111-8111-111111111111", "password": "pt1"}],
  "congestion_control": "bbr",
  "tls": {"enabled": true, "alpn": ["h3"], "certificate_path": "` + cert + `", "key_path": "` + key + `"}
}]}`,
		// 老版本 fanout 自建的入站：新版当作普通已有入站
		"fanout-in-20000-tcp.json": legacyRealityInbound(priv),
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir, cert, key, pub
}

func TestScanAdoptedFindsExistingInbounds(t *testing.T) {
	dir, _, _, pub := sbFixture(t)
	// fanout 自己的两个文件里即使出现 inbounds 也不能当成已有入站
	if err := os.WriteFile(filepath.Join(dir, fanoutRouteFile), []byte(`{"inbounds":[{"type":"mixed","tag":"x","listen_port":1}]}`), 0600); err != nil {
		t.Fatal(err)
	}

	list := scanAdopted(dir)
	if len(list) != 3 {
		t.Fatalf("应当找到 fanout-in-20000-tcp、anytls-1 与 tuic-1，实际 %d 个: %+v", len(list), list)
	}
	if list[0].Tag != "fanout-in-20000-tcp" || list[0].File != "fanout-in-20000-tcp.json" {
		t.Errorf("老版本 fanout 的入站应当当作已有入站: %+v", list[0])
	}
	if list[1].Tag != "anytls-1" || list[1].Port != 31001 || list[1].File != "anytls-1.json" {
		t.Errorf("anytls 解析不对: %+v", list[1])
	}
	if list[2].Tag != "tuic-1" || list[2].Type != "tuic" {
		t.Errorf("tuic 解析不对: %+v", list[2])
	}

	// anytls：多用户各出一条链接，自签证书带 insecure 与 SNI（取证书 CN）
	at := list[1].toNative()
	if len(at.Clients) != 2 {
		t.Fatalf("anytls 用户数 = %d", len(at.Clients))
	}
	link := shareLink(at, at.Clients[0], "1.2.3.4")
	for _, want := range []string{"anytls://pa1@1.2.3.4:31001?", "insecure=1", "sni=bing.com", "#anytls-1"} {
		if !strings.Contains(link, want) {
			t.Errorf("anytls 链接缺少 %s: %s", want, link)
		}
	}

	tu := list[2].toNative()
	link = shareLink(tu, tu.Clients[0], "1.2.3.4")
	for _, want := range []string{"tuic://11111111-1111-4111-8111-111111111111:pt1@1.2.3.4:31002?",
		"alpn=h3", "congestion_control=bbr", "allow_insecure=1", "sni=bing.com"} {
		if !strings.Contains(link, want) {
			t.Errorf("tuic 链接缺少 %s: %s", want, link)
		}
	}

	// REALITY：公钥由私钥推出
	re := list[0].toNative()
	link = shareLink(re, re.Clients[0], "1.2.3.4")
	for _, want := range []string{"vless://22222222-2222-4222-8222-222222222222@1.2.3.4:20000?",
		"security=reality", "pbk=" + pub, "sid=abcd1234", "sni=www.tesla.com", "flow=xtls-rprx-vision"} {
		if !strings.Contains(link, want) {
			t.Errorf("REALITY 链接缺少 %s: %s", want, link)
		}
	}
}

func TestRealityPublicKeyMatchesGenerated(t *testing.T) {
	priv, pub := testRealityKeys(t)
	got, err := realityPublicKey(priv)
	if err != nil || got != pub {
		t.Errorf("由私钥推出的公钥 = %q (%v), want %q", got, err, pub)
	}
}

func TestScriptLinksMatchesPort(t *testing.T) {
	dir := t.TempDir()
	body := "anytls://pa1@example.com:31001?insecure=1#a\ntuic://u:p@example.com:31002?alpn=h3#t\nnot a link\n"
	if err := os.WriteFile(filepath.Join(dir, "links.txt"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	got := scriptLinks(dir, 31002)
	if len(got) != 1 || !strings.HasPrefix(got[0], "tuic://") {
		t.Errorf("scriptLinks = %v", got)
	}
}

// sing-box -C 按文件名排序后合并，数组按顺序追加：fanout-route.json 排在
// route.json 前面，所以 fanout 的规则先于 sb.sh 的规则匹配，不会被它的 final 吞掉；
// fanout-outbounds.json 排在 outbounds.json 前面，fanout-direct 是第一个出站。
func TestFanoutFilesSortBeforeThirdParty(t *testing.T) {
	names := []string{"route.json", "config.json", fanoutRouteFile, "outbounds.json", fanoutOutboundsFile,
		"anytls-1.json", "tuic-1.json", "endpoints.json"}
	sort.Strings(names)
	idx := map[string]int{}
	for i, n := range names {
		idx[n] = i
	}
	if idx[fanoutRouteFile] > idx["route.json"] {
		t.Error("fanout-route.json 应排在 route.json 前面")
	}
	if idx[fanoutOutboundsFile] > idx["outbounds.json"] {
		t.Error("fanout-outbounds.json 应排在 outbounds.json 前面")
	}
}

func TestParseSingboxVersion(t *testing.T) {
	cases := map[string][3]int{
		"sing-box version 1.14.1\n\nEnvironment: go1.24.6 linux/amd64": {1, 14, 1},
		"sing-box version 1.13.0-beta.2":                               {1, 13, 0},
		"garbage":                                                      {},
	}
	for in, want := range cases {
		if got := parseSingboxVersion(in); got != want {
			t.Errorf("parseSingboxVersion(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestForeignRuleSetTags(t *testing.T) {
	dir, _, _, _ := sbFixture(t)
	tags := foreignRuleSetTags(dir)
	if !tags["geosite-cn"] || len(tags) != 1 {
		t.Errorf("foreignRuleSetTags = %v", tags)
	}
}

func TestScanForeign(t *testing.T) {
	dir, _, _, _ := sbFixture(t)
	// fanout 自己的文件里的出站不算已有出站
	if err := os.WriteFile(filepath.Join(dir, fanoutOutboundsFile), []byte(`{"outbounds":[{"type":"direct","tag":"fanout-direct"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	fc := scanForeign(dir)
	var got []string
	for _, o := range fc.Routable() {
		got = append(got, o.Tag+"/"+o.Type+"@"+o.File)
	}
	// 按文件名排序：endpoints.json 在 outbounds.json 前面
	if strings.Join(got, " ") != "wireguard-out/wireguard@endpoints.json direct/direct@outbounds.json warp-40000/socks@outbounds.json" {
		t.Errorf("已有出站 = %v", got)
	}
	if fc.directTag() != "direct" || fc.defaultOutbound() != "direct" || !fc.HasFinal {
		t.Errorf("direct=%q default=%q final=%v", fc.directTag(), fc.defaultOutbound(), fc.HasFinal)
	}
	if len(fc.RuleSets) != 1 || fc.RuleSets[0].Tag != "geosite-cn" || fc.RuleSets[0].File != "route.json" {
		t.Errorf("已有规则集 = %+v", fc.RuleSets)
	}
}

// 规则可以把流量送到已有出站、引用已有规则集；fanout-outbounds.json 里不再有 fanout-direct。
func TestNativeExistingTargets(t *testing.T) {
	conf, _, _, _ := sbFixture(t)
	useFakeSingBox(t, conf)
	n, err := openNative(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()

	// 没有任何出口
	if _, err := n.SaveRule(RuleInput{Name: "国内直连", Users: []string{"u1", "u2"}, Outbound: "direct",
		LocalRuleSets: []string{"geosite-cn"}}, nil); err != nil {
		t.Fatalf("指向已有出站的规则保存失败: %v", err)
	}
	if _, err := n.SaveRule(RuleInput{Users: []string{"u1", "u2"}, Outbound: "nope", Domains: "a.com"}, nil); err == nil {
		t.Error("不存在的已有出站应当拒绝")
	}
	if _, err := n.SaveRule(RuleInput{Users: []string{"u1", "u2"}, Outbound: "direct", LocalRuleSets: []string{"nope"}}, nil); err == nil {
		t.Error("不存在的已有规则集应当拒绝")
	}
	rules, sets, raw := readRoute(t, conf)
	if len(rules) != 2 || rules[1]["outbound"] != "direct" || strings.Join(toStrings(rules[1]["rule_set"]), ",") != "geosite-cn" {
		t.Errorf("没有出口时指向已有出站的规则也要生效: %s", raw)
	}
	if len(sets) != 0 {
		t.Errorf("已有规则集只引用、不重复定义: %v", sets)
	}
	blob, _ := os.ReadFile(filepath.Join(conf, fanoutOutboundsFile))
	var doc struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal(blob, &doc); err != nil || doc.Outbounds == nil || len(doc.Outbounds) != 0 {
		t.Errorf("没有出口时 fanout-outbounds.json 应当是空的 outbounds 数组: %s", blob)
	}

	// 别的脚本把 direct 删了：规则跳过，不写坏配置；fanout 退回自建 fanout-direct
	if err := os.WriteFile(filepath.Join(conf, "outbounds.json"), []byte(`{"outbounds":[{"type":"socks","tag":"warp-40000","server":"127.0.0.1","server_port":40000}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := n.OnTunnelsChanged(nil); err != nil {
		t.Fatal(err)
	}
	rules, _, raw = readRoute(t, conf)
	if len(rules) != 0 {
		t.Errorf("已有出站不在了，规则应当跳过: %s", raw)
	}
	blob, _ = os.ReadFile(filepath.Join(conf, fanoutOutboundsFile))
	if !strings.Contains(string(blob), fanoutDirectTag) {
		t.Errorf("目录里没有 direct 出站时应当自建 fanout-direct: %s", blob)
	}
	rv := ruleViews(n.Rules(), nil, nil, []Inbound{{Tag: "anytls-1"}}, scanForeign(conf).Routable(), scanForeign(conf).RuleSets)
	if len(rv) != 1 || rv[0].Target != "outbound" || rv[0].ExitState != "gone" || rv[0].Active {
		t.Errorf("界面状态不对: %+v", rv)
	}
}

func TestWriteFanoutFilesOnlyTouchesOwnFiles(t *testing.T) {
	dir, _, _, _ := sbFixture(t)
	before := map[string]string{}
	for _, n := range []string{"config.json", "route.json", "anytls-1.json", "tuic-1.json", "outbounds.json", "endpoints.json", "fanout-in-20000-tcp.json"} {
		b, _ := os.ReadFile(filepath.Join(dir, n))
		before[n] = string(b)
	}

	files, err := encodeFiles(map[string]any{
		fanoutRouteFile:     map[string]any{"route": map[string]any{"rules": []any{}}},
		fanoutOutboundsFile: map[string]any{"outbounds": []any{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFanoutFiles(dir, files); err != nil {
		t.Fatal(err)
	}
	for n, b := range before {
		after, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil || string(after) != b {
			t.Errorf("%s 不归 fanout 管，不能被改或删 (%v)", n, err)
		}
	}
	var doc map[string]any
	blob, _ := os.ReadFile(filepath.Join(dir, fanoutRouteFile))
	if err := json.Unmarshal(blob, &doc); err != nil {
		t.Errorf("写出的文件不是合法 JSON: %v", err)
	}

	// 只许写自己的两个文件
	if err := writeFanoutFiles(dir, map[string][]byte{"fanout-in-1-tcp.json": []byte("{}")}); err == nil {
		t.Error("写 fanout-in-*.json 应当被拒绝：fanout 不再建入站")
	}
	if err := writeFanoutFiles(dir, map[string][]byte{"route.json": []byte("{}")}); err == nil {
		t.Error("写 route.json 应当被拒绝")
	}

	// 快照里没有的文件回滚时要删掉
	empty := t.TempDir()
	snap, _ := snapshotFanoutFiles(empty)
	_ = writeFanoutFiles(empty, files)
	if err := writeFanoutFiles(empty, snap); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(empty, fanoutRouteFile)); !os.IsNotExist(err) {
		t.Error("回滚到原来没有的状态时，fanout-route.json 应当被删掉")
	}
}

// useFakeSingBox 换上一个假的 sing-box：check 时如果配置目录里有 BAD 标记就失败，
// run 时一直睡着，version 报 1.14.1。服务名设为 none，走 fanout 自己托管进程的路径。
// 同时把规则集下载换成写一个假的 .srs，规则集目录指到临时目录。
func useFakeSingBox(t *testing.T, confDir string) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "sing-box")
	script := `#!/bin/sh
case "$1" in
  check) if grep -rq BAD "$3" 2>/dev/null; then echo "FATAL bad config" >&2; exit 1; fi; exit 0 ;;
  run) exec sleep 30 ;;
  version) echo "sing-box version 1.14.1"; exit 0 ;;
esac
exit 0
`
	if err := os.WriteFile(bin, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	prev := singbox
	configureSingBox(bin, confDir, "none")
	prevFetch, prevDelay := fetchURL, verifyDelay
	fetchURL = func(u, dst string) error {
		if strings.Contains(u, "nosuch") {
			return os.ErrNotExist
		}
		return os.WriteFile(dst, []byte("SRS\x01fake"), 0600)
	}
	verifyDelay = 10 * time.Millisecond
	t.Cleanup(func() { singbox = prev; fetchURL = prevFetch; verifyDelay = prevDelay })
}

func readRoute(t *testing.T, conf string) (rules []map[string]any, sets []map[string]any, raw string) {
	t.Helper()
	blob, err := os.ReadFile(filepath.Join(conf, fanoutRouteFile))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Route struct {
			Rules   []map[string]any `json:"rules"`
			RuleSet []map[string]any `json:"rule_set"`
			Final   any              `json:"final"`
		} `json:"route"`
	}
	if err := json.Unmarshal(blob, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Route.Final != nil {
		t.Error("fanout-route.json 不能写 final")
	}
	return doc.Route.Rules, doc.Route.RuleSet, string(blob)
}

func TestNativeRulesApplyAndRollBack(t *testing.T) {
	conf, _, _, _ := sbFixture(t)
	useFakeSingBox(t, conf)

	work := t.TempDir()
	n, err := openNative(work)
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()

	up := &Tunnel{Port: 23456, Status: "up", Node: Node{HostName: "jp-home"}}
	down := &Tunnel{Port: 23457, Status: "starting", Node: Node{HostName: "us-home"}}
	tunnels := []*Tunnel{up, down}

	// 没有条件、也没勾全部流量：必须拒绝，不能退化成整条入站转发
	if _, err := n.SaveRule(RuleInput{Users: []string{"u1", "u2"}, Exit: "jp-home"}, tunnels); err == nil {
		t.Fatal("空条件规则应当被拒绝")
	}
	// 不存在的入站 / 出口
	if _, err := n.SaveRule(RuleInput{Users: []string{"nope"}, Exit: "jp-home", Domains: "a.com"}, tunnels); err == nil {
		t.Error("引用不存在的入站应当被拒绝")
	}
	if _, err := n.SaveRule(RuleInput{Users: []string{"u1", "u2"}, Exit: "kr-x", Domains: "a.com"}, tunnels); err == nil {
		t.Error("引用不存在的出口应当被拒绝")
	}
	// 规则集下载失败当场报错，不写配置
	if _, err := n.SaveRule(RuleInput{Users: []string{"u1", "u2"}, Exit: "jp-home",
		RuleSets: []RuleSetRef{{Source: "geosite:nosuch"}}}, tunnels); err == nil {
		t.Error("规则集下载失败应当报错")
	}

	r1, err := n.SaveRule(RuleInput{
		Name: "奈飞", Users: []string{"u1", "u2"}, Exit: "jp-home",
		Domains:  "netflix.com, nflxvideo.net\nfull:www.example.com keyword:nflx",
		RuleSets: []RuleSetRef{{Source: "geosite:netflix"}},
	}, tunnels)
	if err != nil {
		t.Fatalf("保存规则失败: %v", err)
	}
	// 指向没连通出口的规则：保存成功，但不写进配置
	if _, err := n.SaveRule(RuleInput{Users: []string{"u1", "u2"}, Exit: "us-home", Domains: "hulu.com"}, tunnels); err != nil {
		t.Fatalf("保存指向未连通出口的规则失败: %v", err)
	}

	rules, sets, raw := readRoute(t, conf)
	if len(rules) != 3 {
		t.Fatalf("应当是 sniff + 域名规则 + 规则集规则，实际 %d 条: %s", len(rules), raw)
	}
	if rules[0]["action"] != "sniff" || strings.Join(toStrings(rules[0]["auth_user"]), ",") != "u1,u2" {
		t.Errorf("第一条应当是这些入站的 sniff: %v", rules[0])
	}
	if rules[1]["outbound"] != "fanout-exit-jp-home" || rules[1]["action"] != "route" ||
		strings.Join(toStrings(rules[1]["domain_suffix"]), ",") != "netflix.com,nflxvideo.net" ||
		strings.Join(toStrings(rules[1]["domain"]), ",") != "www.example.com" ||
		strings.Join(toStrings(rules[1]["domain_keyword"]), ",") != "nflx" {
		t.Errorf("域名规则不对: %v", rules[1])
	}
	if strings.Join(toStrings(rules[2]["rule_set"]), ",") != "fanout-rs-geosite-netflix" {
		t.Errorf("规则集规则不对: %v", rules[2])
	}
	if strings.Contains(raw, "hulu") || strings.Contains(raw, "us-home") {
		t.Errorf("指向未连通出口的规则不该写进配置: %s", raw)
	}
	if len(sets) != 1 || sets[0]["tag"] != "fanout-rs-geosite-netflix" || sets[0]["type"] != "remote" ||
		sets[0]["format"] != "binary" || sets[0]["download_detour"] != nil || sets[0]["http_client"] == nil {
		t.Errorf("rule_set 不对: %v", sets)
	}
	if p, _ := sets[0]["initial_path"].(string); p == "" || !fileExists(p) {
		t.Errorf("预下载的规则集应当写进 initial_path: %v", sets[0])
	}

	// 没有新建任何入站文件，老的 fanout-in 也还在
	entries, _ := os.ReadDir(conf)
	var fan []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "fanout-") {
			fan = append(fan, e.Name())
		}
	}
	if strings.Join(fan, ",") != "fanout-in-20000-tcp.json,fanout-outbounds.json,fanout-route.json" {
		t.Errorf("fanout 只该写两个文件、不碰老入站文件，实际: %v", fan)
	}

	// 入站列表只读，带引用计数
	list, _ := n.Inbounds()
	for _, ib := range list {
		if ib.Tag == "anytls-1" && ib.Rules != 2 {
			t.Errorf("anytls-1 被 2 条规则引用，实际 %d", ib.Rules)
		}
	}

	// 换节点：规则跟着改指新节点
	newNode := &Tunnel{Port: 23456, Status: "up", Node: Node{HostName: "jp-other"}}
	if err := n.Rebind("jp-home", newNode, []*Tunnel{newNode, down}); err != nil {
		t.Fatal(err)
	}
	if rs := n.Rules(); rs[0].Exit != "jp-other" {
		t.Errorf("换节点后规则应指向新节点: %+v", rs[0])
	}
	_, _, raw = readRoute(t, conf)
	if !strings.Contains(raw, "fanout-exit-jp-other") {
		t.Errorf("换节点后配置没跟上: %s", raw)
	}
	tunnels = []*Tunnel{newNode, down}

	// 调顺序、停用
	rs := n.Rules()
	if err := n.MoveRule(rs[1].ID, -1, tunnels); err != nil {
		t.Fatal(err)
	}
	if got := n.Rules(); got[0].ID != rs[1].ID {
		t.Errorf("上移后顺序不对: %+v", got)
	}
	if err := n.EnableRule(r1.ID, false, tunnels); err != nil {
		t.Fatal(err)
	}
	rules, sets, _ = readRoute(t, conf)
	if len(rules) != 0 || len(sets) != 0 {
		t.Errorf("规则全停用 / 出口没连通时不该有任何规则: %v %v", rules, sets)
	}
	if err := n.EnableRule(r1.ID, true, tunnels); err != nil {
		t.Fatal(err)
	}

	// 校验失败要回滚：正则保留大小写，塞进 BAD 让假 sing-box check 不过
	prevRoute, _ := os.ReadFile(filepath.Join(conf, fanoutRouteFile))
	if _, err := n.SaveRule(RuleInput{Users: []string{"u1"}, Exit: "jp-other", Domains: "regex:BAD"}, tunnels); err == nil {
		t.Fatal("check 失败时保存应当报错")
	}
	nowRoute, _ := os.ReadFile(filepath.Join(conf, fanoutRouteFile))
	if string(prevRoute) != string(nowRoute) {
		t.Error("check 失败后 fanout-route.json 应当回滚")
	}
	if got := n.Rules(); len(got) != 2 {
		t.Errorf("check 失败后坏规则不该留在内存里: %+v", got)
	}

	// 删除
	if err := n.DeleteRule(r1.ID, tunnels); err != nil {
		t.Fatal(err)
	}
	if got := n.Rules(); len(got) != 1 {
		t.Errorf("删除后剩 %d 条", len(got))
	}
	// 规则集不再被引用，预下载文件清掉
	if matches, _ := filepath.Glob(filepath.Join(ruleSetDir(), "fanout-rs-*")); len(matches) != 0 {
		t.Errorf("没被引用的规则集文件应当清掉: %v", matches)
	}

	// 重启后规则还在
	n2, err := openNative(work)
	if err != nil {
		t.Fatal(err)
	}
	if got := n2.Rules(); len(got) != 1 || got[0].Exit != "us-home" {
		t.Errorf("重启后规则丢了: %+v", got)
	}
}

// 老版本的整条入站绑定必须丢掉，不能悄悄保留成整条转发；自建入站文件原样保留。
func TestMigrationDropsWholeInboundBindings(t *testing.T) {
	conf, _, _, _ := sbFixture(t)
	useFakeSingBox(t, conf)
	work := t.TempDir()

	old := `{
  "next_id": 7,
  "inbounds": [{"id": 3, "port": 20000, "protocol": "vless", "network": "tcp", "security": "reality",
    "remark": "🇯🇵 日本 54", "enable": true, "clients": [], "bound_to": "jp-home"}],
  "adopted": {"anytls-1": {"id": 5, "bound_to": "jp-home"}, "tuic-1": {"id": 6, "bound_to": ""}}
}`
	if err := os.WriteFile(filepath.Join(work, "native.json"), []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	oldRoute := `{"route":{"rules":[{"inbound":["anytls-1","fanout-in-20000-tcp"],"action":"route","outbound":"fanout-exit-jp-home"}]}}`
	if err := os.WriteFile(filepath.Join(conf, fanoutRouteFile), []byte(oldRoute), 0600); err != nil {
		t.Fatal(err)
	}
	legacyBefore, _ := os.ReadFile(filepath.Join(conf, "fanout-in-20000-tcp.json"))

	n, err := openNative(work)
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()

	rules, _, raw := readRoute(t, conf)
	if len(rules) != 0 {
		t.Errorf("迁移后不该再有整条入站转发的规则: %s", raw)
	}
	legacyAfter, err := os.ReadFile(filepath.Join(conf, "fanout-in-20000-tcp.json"))
	if err != nil || string(legacyAfter) != string(legacyBefore) {
		t.Error("老版本自建入站的文件要原样保留")
	}

	list, _ := n.Inbounds()
	ids := map[string]int{}
	for _, ib := range list {
		ids[ib.Tag] = ib.ID
	}
	if ids["fanout-in-20000-tcp"] != 3 || ids["anytls-1"] != 5 || ids["tuic-1"] != 6 {
		t.Errorf("迁移后界面编号应当沿用: %v", ids)
	}

	blob, _ := os.ReadFile(filepath.Join(work, "native.json"))
	if strings.Contains(string(blob), "bound_to") || strings.Contains(string(blob), `"inbounds"`) {
		t.Errorf("迁移后状态文件里不该再有老字段: %s", blob)
	}
}

// TestRealSingBoxCheck 用真的 sing-box 校验 fanout 生成的文件与 sb.sh 风格的文件合在一起是否合法、
// 有没有废弃警告。只在设置了 FANOUT_SINGBOX_BIN（指向 sing-box 可执行文件）时运行：
//
//	FANOUT_SINGBOX_BIN=/etc/sing-box/sing-box go test -run RealSingBox -v .
//
// 能连上 GitHub 时会顺带真实下载规则集当 initial_path；连不上也不影响 check。
func TestRealSingBoxCheck(t *testing.T) {
	bin := os.Getenv("FANOUT_SINGBOX_BIN")
	if bin == "" {
		t.Skip("没设 FANOUT_SINGBOX_BIN，跳过真实 sing-box 校验")
	}
	cert, key := testSelfSignedCert(t, "bing.com")
	priv, _ := testRealityKeys(t)
	conf := t.TempDir()
	// FANOUT_CHECK_DIR 指定时把生成的目录留下来，方便手动 `sing-box check -C` 复查
	if d := os.Getenv("FANOUT_CHECK_DIR"); d != "" {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
		conf = d
		// 证书也搬出临时目录，否则测试结束后复查会读不到
		for _, f := range []*string{&cert, &key} {
			blob, _ := os.ReadFile(*f)
			dst := filepath.Join(filepath.Dir(d), filepath.Base(*f))
			if err := os.WriteFile(dst, blob, 0600); err != nil {
				t.Fatal(err)
			}
			*f = dst
		}
	}
	// wireguard 端点要真密钥才过得了 check，现场生成一对
	wgPriv, wgPub := "", ""
	if out, err := exec.Command(bin, "generate", "wg-keypair").Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if v, ok := strings.CutPrefix(line, "PrivateKey: "); ok {
				wgPriv = strings.TrimSpace(v)
			}
			if v, ok := strings.CutPrefix(line, "PublicKey: "); ok {
				wgPub = strings.TrimSpace(v)
			}
		}
	}
	// route.json 里自带的规则集（本地源码格式，免得 run 时要联网）
	localRS := filepath.Join(filepath.Dir(cert), "my-cn.json")
	if err := os.WriteFile(localRS, []byte(`{"version":3,"rules":[{"domain_suffix":["cn","baidu.com"]}]}`), 0644); err != nil {
		t.Fatal(err)
	}
	// 第三方脚本的文件（去掉了需要真实密钥的 wireguard / http_clients），外加老版本 fanout 的入站
	third := map[string]string{
		"config.json":    `{"log":{"level":"warn","timestamp":true},"dns":{"servers":[{"type":"local","tag":"local"}]}}`,
		"outbounds.json": `{"outbounds":[{"type":"direct","tag":"direct"},{"type":"socks","tag":"warp-40000","server":"127.0.0.1","server_port":40000}]}`,
		"route.json": `{"route":{"rule_set":[{"type":"local","tag":"my-cn","format":"source","path":"` + localRS + `"}],` +
			`"rules":[{"inbound":["tuic-1"],"action":"route","outbound":"direct"}],"final":"direct"}}`,
		"anytls-1.json":            `{"inbounds":[{"type":"anytls","tag":"anytls-1","listen":"::","listen_port":47321,"users":[{"name":"u1","password":"pa1"}],"tls":{"enabled":true,"certificate_path":"` + cert + `","key_path":"` + key + `"}}]}`,
		"tuic-1.json":              `{"inbounds":[{"type":"tuic","tag":"tuic-1","listen":"::","listen_port":47322,"users":[{"name":"u1","uuid":"11111111-1111-4111-8111-111111111111","password":"pt1"}],"congestion_control":"bbr","tls":{"enabled":true,"alpn":["h3"],"certificate_path":"` + cert + `","key_path":"` + key + `"}}]}`,
		"fanout-in-20000-tcp.json": strings.Replace(legacyRealityInbound(priv), `"listen_port": 20000`, `"listen_port": 47320`, 1),
	}
	if wgPriv != "" {
		third["endpoints.json"] = `{"endpoints":[{"type":"wireguard","tag":"wireguard-out","address":["172.16.0.2/32"],"private_key":"` + wgPriv +
			`","peers":[{"address":"162.159.192.1","port":2408,"public_key":"` + wgPub + `","allowed_ips":["0.0.0.0/0"]}]}]}`
	}
	for name, body := range third {
		if err := os.WriteFile(filepath.Join(conf, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}

	in := []RuleInput{
		{Name: "奈飞", Users: []string{"u1", "u2"}, Exit: "jp",
			Domains: "netflix.com\nfull:www.example.com\nkeyword:nflx\nregex:^.+\\.nflximg\\.net$", RuleSets: []RuleSetRef{{Source: "geosite:netflix"}}},
		{Name: "日本 IP", Users: []string{"u1", "u2"}, Exit: "jp", Domains: "203.0.113.0/24", RuleSets: []RuleSetRef{{Source: "geoip:jp"}}},
		{Name: "老入站整条", Users: []string{"v"}, Exit: "jp", All: true},
		{Name: "国内直连", Users: []string{"u1", "u2"}, Outbound: "direct", LocalRuleSets: []string{"my-cn"}},
		{Name: "走 warp", Users: []string{"u1", "u2"}, Outbound: "warp-40000", Domains: "openai.com"},
	}
	fc := scanForeign(conf)
	env := ruleEnv{Users: map[string]bool{"u1": true, "u2": true, "v": true},
		Exits: map[string]bool{"jp": true}, Outbounds: fc.outboundTags(), RuleSets: fc.ruleSetTags()}
	var rules []*RouteRule
	rsDir := t.TempDir()
	if os.Getenv("FANOUT_CHECK_DIR") != "" {
		rsDir = filepath.Join(filepath.Dir(conf), "fanout-rulesets")
	}
	for i, ri := range in {
		r, err := normalizeRule(ri, env, nil)
		if err != nil {
			t.Fatal(err)
		}
		r.ID = i + 1
		rules = append(rules, r)
		if err := prefetchRuleSets(rsDir, r.RuleSets); err != nil {
			t.Logf("规则集预下载失败（不影响 check）: %v", err)
		}
	}
	tun := &Tunnel{Port: 23456, Status: "up", Node: Node{HostName: "jp"}}
	tun.setCredential(SocksCred{User: "fo", Pass: "pw"})

	files, err := encodeFiles(buildFanoutFiles(rules, scanAdopted(conf), []*Tunnel{tun},
		buildOptions{RuleSetDir: rsDir, Foreign: fc}))
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFanoutFiles(conf, files); err != nil {
		t.Fatal(err)
	}
	t.Logf("fanout-outbounds.json:\n%s", files[fanoutOutboundsFile])
	t.Logf("fanout-route.json:\n%s", files[fanoutRouteFile])
	if strings.Contains(string(files[fanoutOutboundsFile]), fanoutDirectTag) {
		t.Errorf("目录里有 direct 时不该再建 fanout-direct: %s", files[fanoutOutboundsFile])
	}
	for _, want := range []string{`"outbound": "direct"`, `"my-cn"`, `"outbound": "warp-40000"`} {
		if !strings.Contains(string(files[fanoutRouteFile]), want) {
			t.Errorf("fanout-route.json 缺少 %s", want)
		}
	}
	out, err := exec.Command(bin, "check", "-C", conf).CombinedOutput()
	if err != nil {
		t.Fatalf("sing-box check 失败: %s", out)
	}
	if len(strings.TrimSpace(string(out))) > 0 {
		t.Errorf("sing-box check 应当没有任何输出（包括废弃警告）: %s", out)
	}
	// 合并后的规则顺序：fanout 的规则要排在第三方 route.json 的规则之前
	mergedPath := filepath.Join(t.TempDir(), "merged.json")
	if out, err := exec.Command(bin, "merge", mergedPath, "-C", conf).CombinedOutput(); err != nil {
		t.Fatalf("sing-box merge 失败: %s", out)
	}
	blob, _ := os.ReadFile(mergedPath)
	var merged struct {
		Route struct {
			Rules []map[string]any `json:"rules"`
			Final string           `json:"final"`
		} `json:"route"`
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal(blob, &merged); err != nil {
		t.Fatal(err)
	}
	if merged.Route.Final != "direct" {
		t.Errorf("第三方的 final 应当保留，实际 %q", merged.Route.Final)
	}
	rs := merged.Route.Rules
	if len(rs) < 2 || rs[0]["action"] != "sniff" || rs[1]["outbound"] != "fanout-exit-jp" {
		t.Errorf("fanout 的 sniff 和分流规则应排在最前: %v", rs)
	}
	if last := rs[len(rs)-1]; last["outbound"] != "direct" {
		t.Errorf("第三方 route.json 的规则应排在 fanout 之后: %v", last)
	}
	for _, o := range merged.Outbounds {
		if o["tag"] == fanoutDirectTag {
			t.Errorf("合并后不该有 fanout-direct: %v", merged.Outbounds)
		}
	}

	// 真跑几秒：check 过了不代表起得来（证书读不了、端口冲突、规则集下载失败没有 initial_path）
	cmd := exec.Command(bin, "run", "-C", conf)
	var logBuf strings.Builder
	cmd.Stdout, cmd.Stderr = &logBuf, &logBuf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		t.Fatalf("sing-box run 立刻退出: %v\n%s", err, logBuf.String())
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
		<-done
	}
	if strings.Contains(logBuf.String(), "deprecated") {
		t.Errorf("用到了已废弃的字段:\n%s", logBuf.String())
	}
	t.Logf("sing-box run 输出: %s", logBuf.String())
}
