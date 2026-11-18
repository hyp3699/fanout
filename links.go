package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// 这一份是 sing-box-08.sh 里 refresh_user_subscriptions / update_sub_file 的 Go 版。
//
// 脚本用 python 按「链接的协议+地址+路径+参数+备注」把每个用户的旧链接对回
// 现在的入站链接，再换上用户自己的 UUID/密码。改端口、改 UUID、加 CDN/Tunnel 之后，
// 用户订阅里的节点跟着变，网页这边必须用同一套匹配规则，否则两边生成结果会打架。

// parsedShareLink 是拆开后的分享链接。
type parsedShareLink struct {
	Scheme   string
	Userinfo string
	Host     string
	Port     int
	Path     string
	Query    []linkKV
	Fragment string
	Vmess    map[string]any
	Raw      string
}

type linkKV struct{ K, V string }

// decodeVmess 解出 vmess:// 里的 JSON（兼容标准/URL 安全两种 base64）。
func decodeVmess(line string) (map[string]any, bool) {
	if !strings.HasPrefix(line, "vmess://") {
		return nil, false
	}
	rest := strings.TrimPrefix(line, "vmess://")
	if i := strings.Index(rest, "#"); i >= 0 {
		rest = rest[:i]
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return nil, false
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if blob, err := enc.DecodeString(rest + strings.Repeat("=", (4-len(rest)%4)%4)); err == nil {
			var m map[string]any
			if json.Unmarshal(blob, &m) == nil {
				return m, true
			}
		}
	}
	return nil, false
}

// splitShareLink 拆链接；解析不了返回 nil。
func splitShareLink(line string) *parsedShareLink {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	if strings.HasPrefix(line, "vmess://") {
		vm, ok := decodeVmess(line)
		if !ok {
			return nil
		}
		frag := ""
		if i := strings.Index(line, "#"); i >= 0 {
			frag = line[i+1:]
		}
		return &parsedShareLink{Scheme: "vmess", Vmess: vm, Fragment: frag, Raw: line}
	}
	u, err := url.Parse(line)
	if err != nil || u.Scheme == "" {
		return nil
	}
	sl := &parsedShareLink{
		Scheme:   u.Scheme,
		Host:     u.Hostname(),
		Path:     u.Path,
		Fragment: u.Fragment,
		Raw:      line,
	}
	if p := u.Port(); p != "" {
		sl.Port, _ = strconv.Atoi(p)
	}
	if u.User != nil {
		sl.Userinfo = u.User.String()
	}
	for _, kv := range strings.Split(u.RawQuery, "&") {
		if kv == "" {
			continue
		}
		parts := strings.SplitN(kv, "=", 2)
		v := ""
		if len(parts) > 1 {
			v = parts[1]
		}
		sl.Query = append(sl.Query, linkKV{parts[0], v})
	}
	sort.Slice(sl.Query, func(i, j int) bool { return sl.Query[i].K < sl.Query[j].K })
	return sl
}

func quoteKV(list []linkKV, skipKey string) string {
	parts := make([]string, 0, len(list))
	for _, kv := range list {
		if skipKey != "" && kv.K == skipKey {
			continue
		}
		parts = append(parts, kv.K+"="+kv.V)
	}
	return strings.Join(parts, "&")
}

// exactKey 是强匹配：协议、主机、端口、路径、参数、备注全一致。
func exactKey(sl *parsedShareLink) string {
	if sl == nil {
		return ""
	}
	if sl.Scheme == "vmess" {
		return strings.Join([]string{"vmess", str(sl.Vmess["add"]), str(sl.Vmess["port"]),
			str(sl.Vmess["net"]), str(sl.Vmess["path"]), str(sl.Vmess["host"]),
			str(sl.Vmess["tls"]), str(sl.Vmess["sni"]), str(sl.Vmess["ps"])}, "\x00")
	}
	return strings.Join([]string{sl.Scheme, sl.Host, strconv.Itoa(sl.Port), sl.Path,
		quoteKV(sl.Query, ""), sl.Fragment}, "\x00")
}

// looseKey 忽略端口和 sni：换了端口或加了 CDN 也能对上。
func looseKey(sl *parsedShareLink) string {
	if sl == nil {
		return ""
	}
	if sl.Scheme == "vmess" {
		return strings.Join([]string{"vmess", str(sl.Vmess["add"]), str(sl.Vmess["net"]),
			str(sl.Vmess["path"]), str(sl.Vmess["host"]), str(sl.Vmess["tls"]), str(sl.Vmess["ps"])}, "\x00")
	}
	return strings.Join([]string{sl.Scheme, sl.Host, sl.Path,
		quoteKV(sl.Query, "sni"), sl.Fragment}, "\x00")
}

// quickTunnelKey 按备注认临时隧道链接：域名每次都变，只能按备注对。
func quickTunnelKey(sl *parsedShareLink) (string, bool) {
	if sl == nil {
		return "", false
	}
	if sl.Scheme == "vmess" {
		ps := str(sl.Vmess["ps"])
		if strings.Contains(ps, "临时隧道") || strings.Contains(ps, "QuickTunnel-") {
			return ps, true
		}
		return "", false
	}
	frag, _ := url.QueryUnescape(sl.Fragment)
	if strings.Contains(frag, "临时隧道") || strings.Contains(frag, "QuickTunnel-") {
		return frag, true
	}
	return "", false
}

// applyUserCred 把源链接（入站当前的）换成指定用户的凭据。
// 与脚本 with_user_cred 一致：地址参数全用源的，只换身份。
func applyUserCred(src, userLine, uuid string) string {
	sp := splitShareLink(src)
	if sp == nil {
		return src
	}
	up := splitShareLink(userLine)
	if sp.Scheme == "vmess" {
		obj := map[string]any{}
		for k, v := range sp.Vmess {
			obj[k] = v
		}
		obj["id"] = uuid
		blob, _ := json.Marshal(obj)
		out := "vmess://" + base64.StdEncoding.EncodeToString(blob)
		if sp.Fragment != "" {
			out += "#" + sp.Fragment
		}
		return out
	}
	if up == nil {
		return src
	}
	switch sp.Scheme {
	case "tuic":
		pass := ""
		if i := strings.Index(up.Userinfo, ":"); i >= 0 {
			pass = up.Userinfo[i+1:]
		}
		return replaceUserinfo(src, "tuic", uuid+":"+pass)
	case "vless", "trojan", "hysteria2", "hy2", "anytls":
		return replaceUserinfo(src, sp.Scheme, uuid)
	case "socks", "socks5", "naive+https", "naive+quic", "https":
		return replaceUserinfo(src, sp.Scheme, up.Userinfo)
	}
	return src
}

// replaceUserinfo 只替换 scheme://<userinfo>@ 这一段。
func replaceUserinfo(line, scheme, userinfo string) string {
	prefix := scheme + "://"
	if !strings.HasPrefix(line, prefix) {
		return line
	}
	rest := line[len(prefix):]
	at := strings.LastIndex(rest, "@")
	if at < 0 {
		return line
	}
	return prefix + userinfo + rest[at:]
}

// rebuildSubFile 重建 /etc/sing-box/sub.txt：url/*.txt 依次拼接后 base64。
func rebuildSubFile() error {
	lp := panelPaths()
	entries, err := os.ReadDir(lp.URLDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".txt") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	var sb strings.Builder
	for _, name := range names {
		blob, err := os.ReadFile(filepath.Join(lp.URLDir, name))
		if err != nil {
			continue
		}
		sb.Write(blob)
		if !strings.HasSuffix(string(blob), "\n") {
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}
	enc := base64.StdEncoding.EncodeToString([]byte(sb.String()))
	return writeFileAtomic(lp.SubFile, []byte(enc), 0644)
}

// subSource 是一个可用的入站链接。
type subSource struct {
	line  string
	exact string
	loose string
	qt    string
	hasQT bool
	schem string
}

// loadSubSources 收集 url/*.txt 里的全部链接。
func loadSubSources() []subSource {
	lp := panelPaths()
	entries, err := os.ReadDir(lp.URLDir)
	if err != nil {
		return nil
	}
	var out []subSource
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".txt") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		blob, err := os.ReadFile(filepath.Join(lp.URLDir, name))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(blob), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			sl := splitShareLink(line)
			if sl == nil {
				continue
			}
			src := subSource{line: line, exact: exactKey(sl), loose: looseKey(sl), schem: sl.Scheme}
			if qt, ok := quickTunnelKey(sl); ok {
				src.qt, src.hasQT = qt, true
			}
			out = append(out, src)
		}
	}
	return out
}

// refreshUserSubs 重算所有用户的链接与订阅（脚本 refresh_user_subscriptions）。
func refreshUserSubs() error {
	lp := panelPaths()
	entries, err := os.ReadDir(lp.URLDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	sources := loadSubSources()
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if !validUserName(name) {
			continue
		}
		dir := filepath.Join(lp.URLDir, name)
		uuidFile := filepath.Join(dir, name+"-uuid")
		linksFile := filepath.Join(dir, name)
		blob, err := os.ReadFile(uuidFile)
		if err != nil {
			continue
		}
		parts := strings.Split(strings.TrimSpace(string(blob)), "\n")
		if len(parts) < 2 {
			continue
		}
		uuid := strings.TrimSpace(parts[1])
		if uuid == "" {
			continue
		}
		linkBlob, err := os.ReadFile(linksFile)
		if err != nil {
			continue
		}
		var oldLines []string
		for _, ln := range strings.Split(string(linkBlob), "\n") {
			if s := strings.TrimSpace(ln); s != "" {
				oldLines = append(oldLines, s)
			}
		}
		// 隧道停掉时暂存的临时隧道链接，隧道恢复后自动放回
		pendingFile := filepath.Join(dir, name+"-qtpending")
		if pb, err := os.ReadFile(pendingFile); err == nil {
			for _, ln := range strings.Split(string(pb), "\n") {
				if s := strings.TrimSpace(ln); s != "" {
					oldLines = append(oldLines, s)
				}
			}
		}

		used := map[int]bool{}
		var newLines, pending []string
		for _, oldLine := range oldLines {
			sl := splitShareLink(oldLine)
			if sl == nil {
				continue
			}
			ex, lo := exactKey(sl), looseKey(sl)
			hit := -1
			for i, src := range sources {
				if !used[i] && src.exact == ex {
					hit = i
					break
				}
			}
			if hit < 0 {
				for i, src := range sources {
					if !used[i] && src.loose == lo {
						hit = i
						break
					}
				}
			}
			qt, hasQT := quickTunnelKey(sl)
			if hit < 0 && hasQT {
				for i, src := range sources {
					if !used[i] && src.hasQT && src.qt == qt {
						hit = i
						break
					}
				}
				// 兼容旧版备注
				if hit < 0 && strings.Contains(qt, "QuickTunnel-") {
					for i, src := range sources {
						if !used[i] && src.hasQT && src.schem == sl.Scheme {
							hit = i
							break
						}
					}
				}
			}
			if hit < 0 {
				if hasQT {
					pending = append(pending, oldLine)
				}
				continue
			}
			used[hit] = true
			newLines = append(newLines, applyUserCred(sources[hit].line, oldLine, uuid))
		}

		var sb strings.Builder
		for _, ln := range newLines {
			sb.WriteString(ln)
			sb.WriteString("\n")
		}
		if err := writeFileAtomic(linksFile, []byte(sb.String()), 0644); err != nil {
			continue
		}
		if len(pending) > 0 {
			var pb strings.Builder
			for _, ln := range pending {
				pb.WriteString(ln)
				pb.WriteString("\n")
			}
			_ = writeFileAtomic(pendingFile, []byte(pb.String()), 0644)
		} else {
			_ = os.Remove(pendingFile)
		}

		subFile := filepath.Join(dir, name+"-sub")
		if pathExists(subFile) {
			content := trafficLineFor(name) + "\n" + strings.Join(newLines, "\n")
			_ = writeFileAtomic(subFile, []byte(base64.StdEncoding.EncodeToString([]byte(content))), 0644)
		}
	}
	return nil
}

// updateURLFilePort 改链接文件里的端口（脚本 update_url_file_port）。
func updateURLFilePort(path string, oldPort, newPort int) error {
	if !pathExists(path) {
		return nil
	}
	blob, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	op, np := strconv.Itoa(oldPort), strconv.Itoa(newPort)
	var lines []string
	for _, line := range strings.Split(string(blob), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "vmess://") {
			if vm, ok := decodeVmess(line); ok {
				vm["port"] = np
				frag := ""
				if i := strings.Index(line, "#"); i >= 0 {
					frag = "#" + line[i+1:]
				}
				lines = append(lines, vmessLink(vm)+frag)
				continue
			}
			lines = append(lines, line)
			continue
		}
		// 只换 host:port 那段，别碰路径里同样出现的数字
		if sl := splitShareLink(line); sl != nil && sl.Port == oldPort {
			line = strings.Replace(line, ":"+op, ":"+np, 1)
		} else {
			line = strings.Replace(line, "mport="+op+",", "mport="+np+",", 1)
		}
		lines = append(lines, line)
	}
	return writeFileAtomic(path, []byte(strings.Join(lines, "\n")+"\n"), 0644)
}

// addV2RayUser / removeV2RayUser 维护 config.json 里流量统计的用户名单。
func addV2RayUser(name string) error {
	if name == "" {
		return nil
	}
	return editV2RayUsers(func(users []string) []string {
		for _, u := range users {
			if u == name {
				return users
			}
		}
		return append(users, name)
	})
}

func removeV2RayUser(name string) error {
	if name == "" {
		return nil
	}
	return editV2RayUsers(func(users []string) []string {
		out := users[:0]
		for _, u := range users {
			if u != name {
				out = append(out, u)
			}
		}
		return out
	})
}

// editV2RayUsers 改 config.json 的 experimental.v2ray_api.stats.users。
func editV2RayUsers(fn func([]string) []string) error {
	lp := panelPaths()
	blob, err := os.ReadFile(lp.MainConfig)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var doc map[string]any
	if err := json.Unmarshal(blob, &doc); err != nil {
		return fmt.Errorf("config.json 解析失败: %w", err)
	}
	exp, _ := doc["experimental"].(map[string]any)
	if exp == nil {
		exp = map[string]any{}
		doc["experimental"] = exp
	}
	api, _ := exp["v2ray_api"].(map[string]any)
	if api == nil {
		api = map[string]any{"listen": "127.0.0.1:9094"}
		exp["v2ray_api"] = api
	}
	stats, _ := api["stats"].(map[string]any)
	if stats == nil {
		stats = map[string]any{"enabled": true}
		api["stats"] = stats
	}
	users := strList(stats["users"])
	next := fn(users)
	out := make([]any, 0, len(next))
	for _, u := range next {
		out = append(out, u)
	}
	stats["users"] = out
	newBlob, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(lp.MainConfig, append(newBlob, '\n'), 0600)
}

// trafficLineFor 生成订阅第一行的流量备注（脚本 traffic_line_for）。
func trafficLineFor(name string) string {
	lp := panelPaths()
	used := 0
	limitBytes := 0
	enabled := false
	if blob, err := os.ReadFile(lp.TrafficState); err == nil {
		var st struct {
			Users map[string]struct {
				PeriodTotal float64 `json:"period_total"`
			} `json:"users"`
		}
		if json.Unmarshal(blob, &st) == nil {
			used = int(st.Users[name].PeriodTotal)
		}
	}
	if blob, err := os.ReadFile(filepath.Join(lp.LimitsDir, name+".json")); err == nil {
		var ld struct {
			Enabled    bool    `json:"enabled"`
			LimitBytes float64 `json:"limit_bytes"`
		}
		if json.Unmarshal(blob, &ld) == nil {
			enabled, limitBytes = ld.Enabled, int(ld.LimitBytes)
		}
	}
	var remark string
	if enabled && limitBytes > 0 {
		rem := limitBytes - used
		if rem < 0 {
			rem = 0
		}
		remark = fmt.Sprintf("📊 剩余流量: %s | 已用: %s", formatBytes(float64(rem)), formatBytes(float64(used)))
	} else {
		remark = fmt.Sprintf("📊 剩余流量: 无限制 | 已用: %s", formatBytes(float64(used)))
	}
	return "vless://00000000-0000-0000-0000-000000000000@127.0.0.1:10000?encryption=none&security=none&type=tcp#" + urlEscape(remark)
}

// formatBytes 是脚本 format_bytes 的同款（B/KB/MB/GB，两位小数）。
func formatBytes(v float64) string {
	switch {
	case v >= 1024*1024*1024:
		return fmt.Sprintf("%.2f GB", v/(1024*1024*1024))
	case v >= 1024*1024:
		return fmt.Sprintf("%.2f MB", v/(1024*1024))
	case v >= 1024:
		return fmt.Sprintf("%.2f KB", v/1024)
	}
	return fmt.Sprintf("%d B", int64(v))
}

// userSubURL 由 nginx 主站配置推出某个用户的订阅地址。
func userSubURL(name string) string {
	lp := panelPaths()
	path := ""
	if blob, err := os.ReadFile(filepath.Join(lp.URLDir, name, name+"-path")); err == nil {
		path = strings.TrimSpace(string(blob))
	}
	domain, port := nginxSubDomain()
	if path == "" || domain == "" {
		return ""
	}
	if port != "" && port != "443" {
		return fmt.Sprintf("https://%s:%s%s", bracketIPv6(domain), port, path)
	}
	return fmt.Sprintf("https://%s%s", bracketIPv6(domain), path)
}
