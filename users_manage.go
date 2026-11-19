package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// 订阅用户（脚本「添加用户」建的那套）：
//
//	/etc/sing-box/url/<用户名>/{<用户名>, <用户名>-uuid, <用户名>-sub, <用户名>-path}
//	/etc/nginx/conf.d/singbox_users/<用户名>.conf
//
// 一个用户同时挂在多个入站上，每个入站里都有一条同名 user；链接是复制入站链接后
// 换上这个用户自己的 UUID/密码。网页这边完全按同样的约定读写。

// subscriber 是界面上的一个订阅用户。
type subscriber struct {
	Name        string   `json:"name"`
	UUID        string   `json:"uuid"`
	Path        string   `json:"path"`
	SubURL      string   `json:"sub_url"`
	Protocols   []string `json:"protocols"`             // 出现在哪些入站 tag 里
	DisabledIn  []string `json:"disabled_in,omitempty"` // 到量被移出的入站
	Links       []string `json:"links"`
	HasSub      bool     `json:"has_sub"`
	HasNginx    bool     `json:"has_nginx"`
	TrafficUsed int64    `json:"traffic_used"`
	LimitBytes  int64    `json:"limit_bytes"`
	LimitOn     bool     `json:"limit_on"`
	SpeedText   string   `json:"speed_text"`
}

// listSubscribers 列出所有订阅用户。
func listSubscribers() []subscriber {
	lp := panelPaths()
	entries, readErr := os.ReadDir(lp.URLDir)
	if readErr != nil {
		return nil
	}
	nodes, _ := listNodes()
	speeds := loadSpeedLimits()
	var out []subscriber
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if !validUserName(name) {
			continue
		}
		dir := filepath.Join(lp.URLDir, name)
		uuidBlob, err := os.ReadFile(filepath.Join(dir, name+"-uuid"))
		if err != nil {
			continue
		}
		parts := strings.Split(strings.TrimSpace(string(uuidBlob)), "\n")
		sub := subscriber{Name: name}
		if len(parts) > 1 {
			sub.UUID = strings.TrimSpace(parts[1])
		}
		if blob, err := os.ReadFile(filepath.Join(dir, name+"-path")); err == nil {
			sub.Path = strings.TrimSpace(string(blob))
		}
		if blob, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
			for _, line := range strings.Split(string(blob), "\n") {
				if s := strings.TrimSpace(line); s != "" {
					sub.Links = append(sub.Links, s)
				}
			}
		}
		sub.HasSub = pathExists(filepath.Join(dir, name+"-sub"))
		sub.HasNginx = pathExists(filepath.Join(lp.NginxUserDir, name+".conf"))
		sub.SubURL = userSubURL(name)
		for _, n := range nodes {
			for _, u := range n.Users {
				if u.Name == name {
					sub.Protocols = append(sub.Protocols, n.Tag)
					break
				}
			}
		}
		if len(sub.Protocols) == 0 {
			sub.DisabledIn = disabledTags(name)
		}
		sub.TrafficUsed, sub.LimitBytes, sub.LimitOn = subscriberQuota(name)
		if sp, ok := speeds[name]; ok {
			sub.SpeedText = sp
		}
		out = append(out, sub)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// loadSpeedLimits 读限速存储（与 sb.sh 共用），只取显示用的文本。
func loadSpeedLimits() map[string]string {
	lp := panelPaths()
	out := map[string]string{}
	blob, err := os.ReadFile(lp.SpeedStore)
	if err != nil {
		return out
	}
	var doc map[string]any
	if json.Unmarshal(blob, &doc) != nil {
		return out
	}
	for name, v := range doc {
		o := obj(v)
		if o == nil {
			continue
		}
		if speed := str(o["speed"]); speed != "" {
			bps := speedToBps(speed)
			if bps <= 0 {
				continue
			}
			label := speedText(bps)
			switch str(o["mode"]) {
			case "upload":
				label += " 仅上传"
			case "download":
				label += " 仅下载"
			}
			out[name] = label
		}
	}
	return out
}

// subscriberQuota 读流量用量与限额。
func subscriberQuota(name string) (used, limit int64, on bool) {
	lp := panelPaths()
	if blob, err := os.ReadFile(lp.TrafficState); err == nil {
		var st struct {
			Users map[string]struct {
				PeriodTotal float64 `json:"period_total"`
				Total       float64 `json:"total"`
			} `json:"users"`
		}
		if json.Unmarshal(blob, &st) == nil {
			u := st.Users[name]
			if u.PeriodTotal > 0 {
				used = int64(u.PeriodTotal)
			} else {
				used = int64(u.Total)
			}
		}
	}
	if blob, err := os.ReadFile(filepath.Join(lp.LimitsDir, name+".json")); err == nil {
		var ld struct {
			Enabled    bool    `json:"enabled"`
			LimitBytes float64 `json:"limit_bytes"`
		}
		if json.Unmarshal(blob, &ld) == nil {
			on, limit = ld.Enabled, int64(ld.LimitBytes)
		}
	}
	return
}

// subscriberDetail 读一个用户。
func subscriberDetail(name string) (*subscriber, error) {
	if !validUserName(name) {
		return nil, fmt.Errorf("用户名无效")
	}
	for _, s := range listSubscribers() {
		if s.Name == name {
			item := s
			return &item, nil
		}
	}
	return nil, fmt.Errorf("没有用户 %s", name)
}

// subAddRequest 是「添加用户」的表单。
type subAddRequest struct {
	Name  string   `json:"name"`  // 留空自动 test-user-N
	UUID  string   `json:"uuid"`  // 留空随机
	Path  string   `json:"path"`  // 订阅路径（重建时沿用原来的）
	Files []string `json:"files"` // 要挂到哪些入站（*用户名-N.json*）
}

// addSubscriber 在选中的入站里加一个用户，并生成链接 / 订阅 / Nginx 片段。
func addSubscriber(req subAddRequest) (*subscriber, error) {
	lp := panelPaths()
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = nextSubscriberName()
	}
	if !validUserName(name) {
		return nil, fmt.Errorf("用户名只能用字母、数字、. _ -（最长 48）")
	}
	if len(req.Files) == 0 {
		return nil, fmt.Errorf("至少选一个入站")
	}
	uuid := strings.TrimSpace(req.UUID)
	if uuid == "" {
		uuid = newUUID()
	}
	// 先确认所有入站都能写，避免加一半
	type target struct {
		file string
		doc  map[string]any
		kind string
	}
	var targets []target
	for _, f := range req.Files {
		if !validNodeFile(f) {
			return nil, fmt.Errorf("非法的入站文件 %q", f)
		}
		doc, err := readNodeDoc(f)
		if err != nil {
			return nil, fmt.Errorf("%s 读取失败: %w", f, err)
		}
		kind, _, _ := splitNodeFile(f)
		targets = append(targets, target{file: f, doc: doc, kind: kind})
	}

	// 与脚本一样：先按第一个用户做模板复制，没有模板就按协议拼
	created := 0
	for _, t := range targets {
		ib := firstInbound(t.doc)
		if ib == nil {
			continue
		}
		if err := addUserToInbound(ib, name, uuid); err != nil {
			return nil, fmt.Errorf("%s: %w", t.file, err)
		}
		if err := saveNodeDoc(t.file, t.doc); err != nil {
			return nil, fmt.Errorf("%s: %w", t.file, err)
		}
		created++
	}
	if created == 0 {
		return nil, fmt.Errorf("选中的入站里没有可用的配置")
	}
	_ = addV2RayUser(name)

	// 链接：把每个入站自己的链接复制一份并换上这个用户的凭据
	userDir := filepath.Join(lp.URLDir, name)
	if err := os.MkdirAll(userDir, 0755); err != nil {
		return nil, err
	}
	if err := writeFileAtomic(filepath.Join(userDir, name+"-uuid"), []byte(name+"\n"+uuid+"\n"), 0644); err != nil {
		return nil, err
	}
	// 每个入站的链接复制一份，换上这个用户自己的凭据。
	// 密码类协议（tuic / anytls / hysteria2）每个入站有自己的密码，所以逐入站取。
	var newLinks []string
	for _, t := range targets {
		kind, number, _ := splitNodeFile(t.file)
		src := filepath.Join(lp.URLDir, fmt.Sprintf("%s-%d.txt", kind, number))
		blob, err := os.ReadFile(src)
		if err != nil {
			continue
		}
		cred := userLineFor(t.doc, name, uuid)
		for _, line := range strings.Split(string(blob), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || !linkUsable(line) {
				continue
			}
			newLinks = append(newLinks, applyUserCred(line, cred, uuid))
		}
	}
	if err := writeFileAtomic(filepath.Join(userDir, name), []byte(strings.Join(newLinks, "\n")+"\n"), 0644); err != nil {
		return nil, err
	}

	// 订阅路径 + Nginx 片段：重建时沿用旧路径，老客户端还能继续拉订阅
	subPath := strings.TrimSpace(req.Path)
	if subPath == "" {
		subPath = generateSubPath()
	} else if !strings.HasPrefix(subPath, "/") || strings.ContainsAny(subPath, " \t{}\"") || strings.Contains(subPath, "..") {
		return nil, fmt.Errorf("订阅路径格式无效")
	}
	if err := writeFileAtomic(filepath.Join(userDir, name+"-path"), []byte(subPath), 0644); err != nil {
		return nil, err
	}
	if err := writeNginxUserConf(name, subPath); err != nil {
		return nil, err
	}
	writeSubscriberFile(name, newLinks)

	_ = applySingboxReload()
	if err := nginxApply(); err != nil {
		return nil, fmt.Errorf("用户已创建，但 nginx 生效失败: %w", err)
	}
	return subscriberDetail(name)
}

// addUserToInbound 往入站里加一个用户（脚本 add_user_to_config 的逻辑）。
func addUserToInbound(ib map[string]any, name, uuid string) error {
	proto := str(ib["type"])
	users, _ := ib["users"].([]any)
	if users == nil {
		return fmt.Errorf("入站没有 users 数组")
	}
	for _, u := range users {
		o := obj(u)
		if o == nil {
			continue
		}
		if str(o["name"]) == name {
			return fmt.Errorf("用户 %s 已经在里面了", name)
		}
	}
	var template map[string]any
	for _, u := range users {
		if o := obj(u); o != nil {
			template = o
			break
		}
	}
	newUser := map[string]any{}
	if template != nil {
		for k, v := range template {
			newUser[k] = v
		}
	}
	newUser["name"] = name
	if _, ok := newUser["username"]; ok {
		newUser["username"] = name
	}
	switch {
	case newUser["uuid"] != nil:
		newUser["uuid"] = uuid
	case newUser["password"] != nil:
		newUser["password"] = uuid
	}
	// TUIC 同时有 uuid 和 password，密码不能跟主用户共用
	if proto == "tuic" && newUser["uuid"] != nil && newUser["password"] != nil {
		newUser["password"] = randomAPIKey(24)
	}
	switch proto {
	case "tuic", "hysteria2", "anytls":
		if newUser["password"] == nil {
			newUser["password"] = uuid
		}
	}
	if newUser["uuid"] == nil && (proto == "vless" || proto == "vmess" || proto == "trojan") {
		newUser["uuid"] = uuid
	}
	/* 协议兜底：没有模板时上面已经按需要填了 uuid / password */
	ib["users"] = append(users, newUser)
	return nil
}

// userLineFor 造一条「这个用户的链接」用于取凭据。
func userLineFor(doc map[string]any, name, uuid string) string {
	ib := firstInbound(doc)
	if ib == nil {
		return ""
	}
	for _, u := range inboundUserMaps(ib) {
		if str(u["name"]) != name {
			continue
		}
		switch str(ib["type"]) {
		case "tuic":
			return "tuic://" + uuid + ":" + str(u["password"]) + "@0.0.0.0:1"
		case "socks", "naive":
			return "socks://" + str(u["username"]) + ":" + str(u["password"]) + "@0.0.0.0:1"
		case "vless":
			return "vless://" + uuid + "@0.0.0.0:1"
		case "vmess":
			return vmessLink(map[string]any{"id": uuid})
		case "trojan":
			return "trojan://" + uuid + "@0.0.0.0:1"
		default:
			if pw := str(u["password"]); pw != "" {
				return "hysteria2://" + pw + "@0.0.0.0:1"
			}
			return "anytls://" + uuid + "@0.0.0.0:1"
		}
	}
	return ""
}

// linkUsable 判断链接能不能给客户端用：纯 IP 又没 TLS/SNI 的会被客户端直接跳过。
func linkUsable(line string) bool {
	if strings.HasPrefix(line, "vmess://") {
		vm, ok := decodeVmess(line)
		if !ok {
			return false
		}
		host := str(vm["add"])
		sni := str(vm["sni"])
		return !(isIPLiteral(host) && sni == "")
	}
	sl := splitShareLink(line)
	if sl == nil {
		return false
	}
	switch sl.Scheme {
	case "vless", "trojan":
		sni := ""
		for _, kv := range sl.Query {
			if kv.K == "sni" {
				sni = kv.V
			}
		}
		return !(isIPLiteral(sl.Host) && sni == "")
	}
	return true
}

func isIPLiteral(host string) bool {
	h := strings.Trim(host, "[]")
	return net.ParseIP(h) != nil
}

// generateSubPath 造一个不重复的订阅路径。
func generateSubPath() string {
	lp := panelPaths()
	used := map[string]bool{}
	if entries, err := os.ReadDir(lp.NginxUserDir); err == nil {
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".conf") {
				continue
			}
			blob, err := os.ReadFile(filepath.Join(lp.NginxUserDir, e.Name()))
			if err != nil {
				continue
			}
			for _, m := range matchAll(string(blob), `location = (/[A-Za-z0-9._-]+)`) {
				used[m] = true
			}
		}
	}
	for i := 0; i < 20; i++ {
		p := "/" + randomAPIKey(22)
		if !used[p] {
			return p
		}
	}
	return "/" + randomAPIKey(26)
}

// writeNginxUserConf 写用户订阅片段（与脚本生成的内容一致）。
func writeNginxUserConf(name, subPath string) error {
	lp := panelPaths()
	if err := os.MkdirAll(lp.NginxUserDir, 0755); err != nil {
		return err
	}
	conf := fmt.Sprintf(`location = %s {
proxy_pass %s/sub/%s;
proxy_http_version 1.1;
proxy_set_header Host $host;
proxy_set_header X-Real-IP $remote_addr;
proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
proxy_no_cache 1;
proxy_cache_bypass 1;
}
`, subPath, lp.SubServiceURL, name)
	path := filepath.Join(lp.NginxUserDir, name+".conf")
	return writeVerified(path, []byte(conf), 0644, nginxTest)
}

// writeSubscriberFile 写用户的 <用户名>-sub（base64 的流量行 + 链接）。
func writeSubscriberFile(name string, links []string) {
	lp := panelPaths()
	dir := filepath.Join(lp.URLDir, name)
	content := trafficLineFor(name) + "\n" + strings.Join(links, "\n")
	_ = writeFileAtomic(filepath.Join(dir, name+"-sub"),
		[]byte(base64.StdEncoding.EncodeToString([]byte(content))), 0644)
}

// nextSubscriberName 与脚本一样从 test-user-N 往后排。
func nextSubscriberName() string {
	lp := panelPaths()
	max := 0
	for i := 1; i < 1000; i++ {
		name := fmt.Sprintf("test-user-%d", i)
		if !pathExists(filepath.Join(lp.URLDir, name)) {
			max = i
			break
		}
	}
	if max == 0 {
		max = 1
	}
	return fmt.Sprintf("test-user-%d", max)
}

// deleteSubscriber 删用户：从所有入站里去掉、删订阅目录与 Nginx 片段。
func deleteSubscriber(name string, preserveData bool) error {
	lp := panelPaths()
	if !validUserName(name) {
		return fmt.Errorf("用户名无效")
	}
	if !pathExists(filepath.Join(lp.URLDir, name)) {
		return fmt.Errorf("没有用户 %s", name)
	}
	// 1. 从所有入站里摘掉
	entries, err := os.ReadDir(lp.ConfDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		fname := e.Name()
		if e.IsDir() || !strings.HasSuffix(fname, ".json") || isManagedFile(fname) || fname == "cloudflared.json" {
			continue
		}
		doc, err := readNodeDoc(fname)
		if err != nil {
			continue
		}
		changed := false
		for _, ib := range inboundsOf(doc) {
			users, _ := ib["users"].([]any)
			if users == nil {
				continue
			}
			var kept []any
			for _, u := range users {
				o := obj(u)
				if o != nil && str(o["name"]) == name {
					changed = true
					continue
				}
				kept = append(kept, u)
			}
			if changed {
				ib["users"] = kept
			}
		}
		if changed {
			if err := saveNodeDoc(fname, doc); err != nil {
				return err
			}
		}
	}
	_ = removeV2RayUser(name)
	if !preserveData {
		_ = deleteUserTrafficData(name)
	}
	// 2. 移动用户目录与 Nginx 片段到回收目录
	if dir := filepath.Join(lp.URLDir, name); pathExists(dir) {
		if _, err := moveToTrash(filepath.Join(lp.URLDir, ".fanout-trash"), dir); err != nil {
			return err
		}
	}
	if conf := filepath.Join(lp.NginxUserDir, name+".conf"); pathExists(conf) {
		_, _ = moveToTrash(lp.NginxTrashDir, conf)
	}
	_ = applySingboxReload()
	if nginxInstalled() && nginxTest() == nil {
		_ = nginxApply()
	}
	return nil
}

// rebuildSubscriber 重新把用户挂回全部入站（脚本的「重新添加协议」）。
func rebuildSubscriber(name string) (*subscriber, error) {
	detail, err := subscriberDetail(name)
	if err != nil {
		return nil, err
	}
	if detail.UUID == "" {
		return nil, fmt.Errorf("用户 UUID 文件损坏")
	}
	if len(detail.Protocols) == 0 && len(disabledTags(name)) > 0 {
		return nil, fmt.Errorf("用户已到量停用，先在 限速/流量 里解除")
	}
	files := make([]string, 0, len(detail.Protocols))
	for _, tag := range detail.Protocols {
		// tag 形如 vless-reality-1，对应文件 vless-reality-1.json；socks 例外
		if f := nodeFileForTag(tag); f != "" {
			files = append(files, f)
		}
	}
	if len(files) == 0 {
		nodes, _ := listNodes()
		for _, n := range nodes {
			files = append(files, n.File)
		}
	}
	if err := deleteSubscriber(name, true); err != nil {
		return nil, err
	}
	return addSubscriber(subAddRequest{Name: name, UUID: detail.UUID, Path: detail.Path, Files: files})
}

// nodeFileForTag 由 tag 找配置文件（tag 与文件名基本一致，socks-N 对应 socks5-N.json）。
func nodeFileForTag(tag string) string {
	lp := panelPaths()
	if validNodeFile(tag+".json") && pathExists(filepath.Join(lp.ConfDir, tag+".json")) {
		return tag + ".json"
	}
	if strings.HasPrefix(tag, "socks-") {
		f := "socks5-" + strings.TrimPrefix(tag, "socks-") + ".json"
		if pathExists(filepath.Join(lp.ConfDir, f)) {
			return f
		}
	}
	// 兜底：扫一遍，按 tag 精确匹配
	entries, err := os.ReadDir(lp.ConfDir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || isManagedFile(e.Name()) {
			continue
		}
		doc, err := readNodeDoc(e.Name())
		if err != nil {
			continue
		}
		if ib := firstInbound(doc); ib != nil && str(ib["tag"]) == tag {
			return e.Name()
		}
	}
	return ""
}

// inboundsOf 取文件里的全部入站。
func inboundsOf(doc map[string]any) []map[string]any {
	list, _ := doc["inbounds"].([]any)
	var out []map[string]any
	for _, e := range list {
		if o := obj(e); o != nil {
			out = append(out, o)
		}
	}
	return out
}
