package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// 入站的修改操作：改端口、改 UUID/密码、改 Reality 域名、删入站、
// Hysteria2 端口跳跃与 Gecko 混淆。每一步都按脚本的顺序来：
// 改配置 → sing-box check（不过就回滚）→ 放行/清理端口 → 改链接与订阅 → 重载。

// nodeURLFile 返回入站对应的链接文件路径。
func nodeURLFile(n *node) string {
	lp := panelPaths()
	return filepath.Join(lp.URLDir, fmt.Sprintf("%s-%d.txt", n.Kind, n.Number))
}

// loadNode 按文件名读一个入站。
func loadNode(file string) (*node, map[string]any, error) {
	doc, err := readNodeDoc(file)
	if err != nil {
		return nil, nil, err
	}
	n := describeNode(file, doc)
	if n == nil {
		return nil, nil, fmt.Errorf("%s 里没有可管理的入站", file)
	}
	fillNodeRuntime([]*node{n})
	return n, doc, nil
}

// deleteNode 删入站：临时隧道、端口规则、流量统计用户、配置与链接一起清掉。
func deleteNode(file string, removeCDN bool) error {
	n, doc, err := loadNode(file)
	if err != nil {
		return err
	}
	if removeCDN {
		for _, d := range n.CDNDomains {
			_ = cfDeleteCDN(d)
		}
	}
	// 临时隧道服务先拆掉，否则 systemd 会一直拉起一个指向已删入站的 cloudflared
	if n.Quick || quickUnitExists(quickUnitName(n.Kind, n.Number)) {
		quickRemove(n.Kind, n.Number)
	}
	if n.Kind == "hysteria2" {
		removeHy2HopRules(n.Number)
	}
	if n.Port > 0 && n.Port != 443 {
		purgePortRules(n.Port)
	}
	first := ""
	if len(n.Users) > 0 {
		first = n.Users[0].Name
	}
	if first != "" {
		_ = resetV2RayUserStats(first)
		_ = removeV2RayUser(first)
		_ = deleteUserTrafficData(first)
	}
	lp := panelPaths()
	if err := os.Remove(filepath.Join(lp.ConfDir, file)); err != nil && !os.IsNotExist(err) {
		return err
	}
	_ = os.Remove(nodeURLFile(n))
	_ = rebuildSubFile()
	_ = refreshUserSubs()
	_ = doc
	return applySingboxReload()
}

// setNodePort 改端口。旧端口规则清掉、新端口放行，链接与订阅跟着改。
func setNodePort(file string, newPort int) (*node, error) {
	n, doc, err := loadNode(file)
	if err != nil {
		return nil, err
	}
	if !validPort(newPort) {
		return nil, fmt.Errorf("端口必须在 1-65535 之间")
	}
	if newPort == n.Port {
		return n, nil
	}
	proto := "tcp"
	if n.Kind == "hysteria2" || n.Kind == "tuic" {
		proto = "udp"
	}
	if portInUse(newPort, proto) {
		return nil, fmt.Errorf("端口 %d 已被占用", newPort)
	}
	ib := firstInbound(doc)
	if ib == nil {
		return nil, fmt.Errorf("入站配置为空")
	}
	ib["listen_port"] = newPort
	if err := saveNodeDoc(file, doc); err != nil {
		return nil, err
	}
	protos := []string{"tcp"}
	switch n.Kind {
	case "hysteria2", "tuic":
		protos = []string{"udp"}
	case "xhttp-udp-tls", "naiveproxy":
		protos = []string{"tcp", "udp"}
	}
	for _, p := range protos {
		_ = allowPort(newPort, p)
	}
	old := n.Port
	if old > 0 && old != 443 {
		purgePortRules(old)
	}
	_ = updateURLFilePort(nodeURLFile(n), old, newPort)
	_ = rebuildSubFile()
	_ = refreshUserSubs()
	reloadErr := applySingboxReload()

	n, _, err = loadNode(file)
	if err != nil {
		return nil, err
	}
	if reloadErr != nil {
		n.Remark = reloadErr.Error()
	}
	return n, nil
}

// rotateNodeCred 换掉某个用户的 UUID / 密码（传空值就随机生成），并同步链接。
// 脚本 modify_inbound_uuid 的做法：只换这一个用户，链接里凡是这个旧值都替换。
func rotateNodeCred(file, userName, value string) (string, error) {
	n, doc, err := loadNode(file)
	if err != nil {
		return "", err
	}
	ib := firstInbound(doc)
	if ib == nil {
		return "", fmt.Errorf("入站配置为空")
	}
	users := inboundUserMaps(ib)
	if len(users) == 0 {
		return "", fmt.Errorf("这个入站还没有用户")
	}
	target := -1
	for i, u := range users {
		if userName == "" || str(u["name"]) == userName {
			target = i
			break
		}
	}
	if target < 0 {
		return "", fmt.Errorf("入站里没有用户 %s", userName)
	}
	u := users[target]
	field := "uuid"
	old := str(u["uuid"])
	if old == "" {
		field, old = "password", str(u["password"])
	}
	if old == "" {
		field, old = "username", str(u["username"])
	}
	if old == "" {
		return "", fmt.Errorf("用户 %s 没有可修改的 UUID/密码", userName)
	}
	newVal := strings.TrimSpace(value)
	if newVal == "" {
		newVal = newUUID()
	}
	if len(newVal) < 4 || len(newVal) > 128 {
		return "", fmt.Errorf("新值长度要在 4-128 之间")
	}
	u[field] = newVal
	if err := saveNodeDoc(file, doc); err != nil {
		return "", err
	}
	// 链接文件里把旧值替换成新值（与脚本一致，只动身份部分）
	urlFile := nodeURLFile(n)
	if blob, err := os.ReadFile(urlFile); err == nil {
		body := rewriteCredInText(string(blob), old, newVal, u)
		_ = writeFileAtomic(urlFile, []byte(body), 0644)
	}
	_ = rebuildSubFile()
	_ = refreshUserSubs()
	_ = applySingboxReload()
	return newVal, nil
}

// rewriteCredInText 替换链接文本里的凭证：普通链接直接换，vmess 要解 base64。
func rewriteCredInText(text, old, newVal string, user map[string]any) string {
	var out []string
	username := str(user["username"])
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.HasPrefix(line, "vmess://") {
			if vm, ok := decodeVmess(line); ok && str(vm["id"]) == old {
				vm["id"] = newVal
				frag := ""
				if i := strings.Index(line, "#"); i >= 0 {
					frag = "#" + line[i+1:]
				}
				out = append(out, vmessLink(vm)+frag)
				continue
			}
			out = append(out, line)
			continue
		}
		if strings.HasPrefix(line, "tuic://") && strings.Contains(line, "://"+old+":") {
			line = strings.Replace(line, "://"+old+":", "://"+newVal+":", 1)
		} else if username != "" &&
			(strings.HasPrefix(line, "socks://"+username+":"+old+"@") ||
				strings.HasPrefix(line, "naive+https://"+username+":"+old+"@") ||
				strings.HasPrefix(line, "naive+quic://"+username+":"+old+"@")) {
			line = strings.Replace(line, username+":"+old+"@", username+":"+newVal+"@", 1)
		} else {
			for _, scheme := range []string{"vless", "hysteria2", "anytls", "trojan", "socks", "hy2"} {
				if strings.HasPrefix(line, scheme+"://"+old+"@") {
					line = strings.Replace(line, scheme+"://"+old+"@", scheme+"://"+newVal+"@", 1)
					break
				}
			}
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n") + "\n"
}

// setRealitySNI 改 Reality 伪装域名（脚本 modify_reality_domain）。
func setRealitySNI(file, sni string) error {
	sni = strings.TrimSpace(sni)
	if !validDomain(sni) {
		return fmt.Errorf("域名格式无效")
	}
	n, doc, err := loadNode(file)
	if err != nil {
		return err
	}
	ib := firstInbound(doc)
	tls := obj(ib["tls"])
	r := obj(tls["reality"])
	if tls == nil || r == nil || tls["enabled"] != true {
		return fmt.Errorf("这个入站不是 Reality 配置")
	}
	tls["server_name"] = sni
	hs := obj(r["handshake"])
	if hs == nil {
		hs = map[string]any{}
		r["handshake"] = hs
	}
	hs["server"] = sni
	if err := saveNodeDoc(file, doc); err != nil {
		return err
	}
	urlFile := nodeURLFile(n)
	if blob, err := os.ReadFile(urlFile); err == nil {
		body := replaceQueryParam(string(blob), "sni", sni)
		_ = writeFileAtomic(urlFile, []byte(body), 0644)
	}
	_ = rebuildSubFile()
	_ = refreshUserSubs()
	return applySingboxReload()
}

// replaceQueryParam 把所有链接里的某个参数换成新值（保留其余部分）。
func replaceQueryParam(text, key, value string) string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.HasPrefix(line, "vmess://") {
			if vm, ok := decodeVmess(line); ok && str(vm[key]) != "" {
				vm[key] = value
				frag := ""
				if i := strings.Index(line, "#"); i >= 0 {
					frag = "#" + line[i+1:]
				}
				out = append(out, vmessLink(vm)+frag)
				continue
			}
			out = append(out, line)
			continue
		}
		head, frag := line, ""
		if i := strings.Index(line, "#"); i >= 0 {
			head, frag = line[:i], line[i:]
		}
		parts := strings.Split(head, "&")
		for i, p := range parts {
			if strings.HasPrefix(p, key+"=") {
				parts[i] = key + "=" + value
			} else if strings.Contains(p, "?"+key+"=") {
				idx := strings.Index(p, "?"+key+"=")
				parts[i] = p[:idx] + "?" + key + "=" + value
			}
		}
		out = append(out, strings.Join(parts, "&")+frag)
	}
	return strings.Join(out, "\n") + "\n"
}

// ---- Hysteria2 端口跳跃 ----

func hy2HopComment(number int) string { return fmt.Sprintf("Hysteria2_Hop_%d", number) }

// hy2HopEnabled 看 nft 里有没有这个入站的跳跃规则。
func hy2HopEnabled(number int) bool {
	comment := hy2HopComment(number)
	for _, fam := range []string{"ip", "ip6"} {
		out := nftChain(fam + " hysteria_nat prerouting")
		if strings.Contains(out, comment) {
			return true
		}
	}
	return false
}

// removeHy2HopRules 删掉某个入站的全部跳跃规则。
func removeHy2HopRules(number int) {
	if !haveCmd("nft") {
		return
	}
	comment := "\"" + hy2HopComment(number) + "\""
	for _, fam := range []string{"ip", "ip6"} {
		if nftChain(fam+" hysteria_nat prerouting") == "" {
			continue
		}
		out, err := adminExec("nft", "-a", "list", "chain", fam, "hysteria_nat", "prerouting")
		if err != nil {
			continue
		}
		for _, line := range strings.Split(out, "\n") {
			if !strings.Contains(line, comment) {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			if _, err := strconv.Atoi(fields[len(fields)-1]); err != nil {
				continue
			}
			_, _ = adminExec("nft", "delete", "rule", fam, "hysteria_nat", "prerouting", "handle", fields[len(fields)-1])
		}
	}
	_ = saveNftRules()
}

// setHy2Hopping 开端口跳跃：nft dnat 区间到监听端口，链接带 mport。
func setHy2Hopping(file string, minPort, maxPort int) error {
	n, doc, err := loadNode(file)
	if err != nil {
		return err
	}
	if n.Kind != "hysteria2" {
		return fmt.Errorf("只有 Hysteria2 入站可以开端口跳跃")
	}
	if !validPort(minPort) || !validPort(maxPort) || maxPort <= minPort {
		return fmt.Errorf("跳跃区间无效：结束端口要大于起始端口")
	}
	if maxPort-minPort > 1000 {
		return fmt.Errorf("区间最多 1000 个端口")
	}
	if !haveCmd("nft") {
		return fmt.Errorf("没有 nft 命令")
	}
	ib := firstInbound(doc)
	if ib == nil {
		return fmt.Errorf("入站配置为空")
	}
	listen := n.Port
	if listen == 0 {
		return fmt.Errorf("读不到监听端口")
	}
	comment := hy2HopComment(n.Number)
	for _, fam := range []string{"ip", "ip6"} {
		_, _ = adminExec("nft", "add", "table", fam, "hysteria_nat")
		_, _ = adminExec("nft", "add", "chain", fam, "hysteria_nat", "prerouting",
			"{ type nat hook prerouting priority -100; policy accept; }")
	}
	removeHy2HopRules(n.Number)
	args := []string{"add", "rule", "ip", "hysteria_nat", "prerouting", "udp", "dport",
		fmt.Sprintf("%d-%d", minPort, maxPort), "dnat", "to", fmt.Sprintf(":%d", listen), "comment", comment}
	if out, err := adminExec("nft", args...); err != nil {
		return fmt.Errorf("添加跳跃规则失败：%s", trimOut(out))
	}
	if pathExists("/proc/net/if_inet6") {
		_, _ = adminExec("nft", "add", "rule", "ip6", "hysteria_nat", "prerouting", "udp", "dport",
			fmt.Sprintf("%d-%d", minPort, maxPort), "dnat", "to", fmt.Sprintf(":%d", listen), "comment", comment)
	}
	_ = saveNftRules()

	// 链接：保留 insecure / pinSHA256 / obfs，加上 mport
	urlFile := nodeURLFile(n)
	old, _ := os.ReadFile(urlFile)
	oldLine := firstLinkLine(string(old))
	parts := strings.Split(oldLine, "?")
	var query []string
	if len(parts) > 1 {
		q := parts[1]
		if i := strings.Index(q, "#"); i >= 0 {
			q = q[:i]
		}
		for _, kv := range strings.Split(q, "&") {
			if kv == "" || strings.HasPrefix(kv, "mport=") {
				continue
			}
			query = append(query, kv)
		}
	}
	pw := ""
	if len(n.Users) > 0 {
		pw = n.Users[0].Password
	}
	host := hostPublicIP()
	query = append(query, fmt.Sprintf("mport=%d,%d-%d", listen, minPort, maxPort))
	link := fmt.Sprintf("hysteria2://%s@%s:%d/?%s#hysteria2-%d", pw, bracketIPv6(host), listen,
		strings.Join(query, "&"), n.Number)
	if err := writeFileAtomic(urlFile, []byte(link+"\n"), 0644); err != nil {
		return err
	}
	_ = rebuildSubFile()
	_ = refreshUserSubs()
	return applySingboxReload()
}

// disableHy2Hopping 关跳跃：删规则、链接去掉 mport。
func disableHy2Hopping(file string) error {
	n, _, err := loadNode(file)
	if err != nil {
		return err
	}
	if n.Kind != "hysteria2" {
		return fmt.Errorf("只有 Hysteria2 入站有端口跳跃")
	}
	removeHy2HopRules(n.Number)
	urlFile := nodeURLFile(n)
	if blob, err := os.ReadFile(urlFile); err == nil {
		body := dropQueryParams(string(blob), "mport")
		_ = writeFileAtomic(urlFile, []byte(body), 0644)
	}
	_ = rebuildSubFile()
	_ = refreshUserSubs()
	return applySingboxReload()
}

// ---- Hysteria2 Gecko 混淆 ----

// hy2ObfsEnabled 看配置里有没有 gecko 混淆。
func hy2ObfsEnabled(n *node) bool {
	doc, err := readNodeDoc(n.File)
	if err != nil {
		return false
	}
	ib := firstInbound(doc)
	if ib == nil {
		return false
	}
	if o := obj(ib["obfs"]); o != nil && str(o["type"]) == "gecko" {
		return true
	}
	for _, line := range n.Links {
		if strings.Contains(line, "obfs=gecko") {
			return true
		}
	}
	return false
}

// setHy2Obfs 开关 Gecko 混淆；开着的时候链接带上 obfs 参数。
func setHy2Obfs(file string, on bool, password string) (string, error) {
	n, doc, err := loadNode(file)
	if err != nil {
		return "", err
	}
	if n.Kind != "hysteria2" {
		return "", fmt.Errorf("只有 Hysteria2 入站有混淆")
	}
	ib := firstInbound(doc)
	if ib == nil {
		return "", fmt.Errorf("入站配置为空")
	}
	if !on {
		delete(ib, "obfs")
		if err := saveNodeDoc(file, doc); err != nil {
			return "", err
		}
		urlFile := nodeURLFile(n)
		if blob, err := os.ReadFile(urlFile); err == nil {
			body := dropQueryParams(string(blob), "obfs", "obfs-password", "obfs-min", "obfs-max")
			_ = writeFileAtomic(urlFile, []byte(body), 0644)
		}
		_ = rebuildSubFile()
		_ = refreshUserSubs()
		_ = applySingboxReload()
		return "", nil
	}
	if password == "" {
		password = randomAPIKey(20)
	}
	ib["obfs"] = map[string]any{
		"type": "gecko", "password": password,
		"min_packet_size": 512, "max_packet_size": 1200,
	}
	if err := saveNodeDoc(file, doc); err != nil {
		return "", err
	}
	urlFile := nodeURLFile(n)
	if blob, err := os.ReadFile(urlFile); err == nil {
		body := dropQueryParams(string(blob), "obfs", "obfs-password", "obfs-min", "obfs-max")
		body = addQueryParams(body, "obfs=gecko", "obfs-password="+password, "obfs-min=512", "obfs-max=1200")
		_ = writeFileAtomic(urlFile, []byte(body), 0644)
	}
	_ = rebuildSubFile()
	_ = refreshUserSubs()
	_ = applySingboxReload()
	return password, nil
}

// dropQueryParams 去掉链接里的若干参数。
func dropQueryParams(text string, keys ...string) string {
	drop := map[string]bool{}
	for _, k := range keys {
		drop[k] = true
	}
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		frag := ""
		if i := strings.Index(line, "#"); i >= 0 {
			line, frag = line[:i], line[i:]
		}
		base, query := line, ""
		if i := strings.Index(line, "?"); i >= 0 {
			base, query = line[:i], line[i+1:]
		}
		if query == "" {
			out = append(out, line+frag)
			continue
		}
		var kept []string
		for _, kv := range strings.Split(query, "&") {
			if kv == "" {
				continue
			}
			k := kv
			if j := strings.Index(kv, "="); j >= 0 {
				k = kv[:j]
			}
			if drop[k] {
				continue
			}
			kept = append(kept, kv)
		}
		if len(kept) > 0 {
			base += "?" + strings.Join(kept, "&")
		}
		out = append(out, base+frag)
	}
	return strings.Join(out, "\n") + "\n"
}

// addQueryParams 把若干 key=value 塞进 ? 之后（没有 ? 就补一个）。
func addQueryParams(text string, params ...string) string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		frag := ""
		if i := strings.Index(line, "#"); i >= 0 {
			line, frag = line[:i], line[i:]
		}
		add := strings.Join(params, "&")
		if strings.Contains(line, "?") {
			line += "&" + add
		} else {
			line += "?" + add
		}
		out = append(out, line+frag)
	}
	return strings.Join(out, "\n") + "\n"
}

// firstLinkLine 取文件里第一条非空链接。
func firstLinkLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) != "" {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

// nodeConfigText 返回格式化后的入站 JSON。
func nodeConfigText(file string) (string, error) {
	doc, err := readNodeDoc(file)
	if err != nil {
		return "", err
	}
	blob, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	return string(blob), nil
}

// resetV2RayUserStats 让统计服务把某个用户的计数清零（脚本 reset_v2ray_api_user_stats）。
func resetV2RayUserStats(name string) error {
	lp := panelPaths()
	grpcurl := filepath.Join(lp.UserManagerDir, "traffic", "grpcurl")
	proto := filepath.Join(lp.UserManagerDir, "traffic", "stats.proto")
	if name == "" || !pathExists(grpcurl) || !pathExists(proto) {
		return nil
	}
	body := fmt.Sprintf(`{"pattern":"user>>>%s>>>traffic>>>.*","reset":true,"regexp":true}`, name)
	out, err := adminExec(grpcurl, "-plaintext", "-import-path", filepath.Dir(proto),
		"-proto", proto, "-d", body, "127.0.0.1:9094",
		"v2ray.core.app.stats.command.StatsService/QueryStats")
	if err != nil {
		return fmt.Errorf("重置流量统计失败：%s", trimOut(out))
	}
	return nil
}

// deleteUserTrafficData 标记删除某个用户的流量数据（脚本 delete_user_traffic_data）。
func deleteUserTrafficData(name string) error {
	if !validUserName(name) {
		return nil
	}
	lp := panelPaths()
	dir := filepath.Join(lp.UserManagerDir, "traffic", "delete_requests")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	_ = os.Chmod(dir, 0700)
	if err := writeFileAtomic(filepath.Join(dir, name), nil, 0600); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(lp.LimitsDir, name+".json"))
	return nil
}

// hy2ObfsPassword 读出现有的 Gecko 密码，界面显示用。
func hy2ObfsPassword(n *node) string {
	doc, err := readNodeDoc(n.File)
	if err != nil {
		return ""
	}
	ib := firstInbound(doc)
	if ib == nil {
		return ""
	}
	if o := obj(ib["obfs"]); o != nil && str(o["type"]) == "gecko" {
		return str(o["password"])
	}
	return ""
}
