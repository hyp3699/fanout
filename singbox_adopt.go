package main

import (
	"bufio"
	"encoding/json"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// adoptedInbound 是 sing-box 配置目录里已有的一个入站（别的脚本如 sb.sh 建的，
// 或者老版本 fanout 留下的 fanout-in-*.json）。
//
// fanout 不建、不改、不删入站，只读出来列在界面上（带分享链接），
// 并允许分流规则按 tag 引用它：规则落在 fanout-route.json 里。
type adoptedInbound struct {
	Tag  string
	Type string
	Port int
	File string // 来源文件名
	Raw  map[string]any

	// ID 是界面用的编号，来自 nativeStore.Adopted
	ID int
}

// scanAdopted 读取配置目录里除 fanout 自己两个文件之外所有文件中的 inbounds。
//
// 任何文件读不了或解析不了都跳过（sing-box check 会报真正的错），
// 没有 tag 的入站没法写路由规则，也跳过。
func scanAdopted(dir string) []*adoptedInbound {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []*adoptedInbound
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") || isManagedFile(name) {
			continue
		}
		blob, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		var doc struct {
			Inbounds []map[string]any `json:"inbounds"`
		}
		if err := json.Unmarshal(blob, &doc); err != nil {
			log.Printf("跳过无法解析的 sing-box 配置 %s: %v", name, err)
			continue
		}
		for _, ib := range doc.Inbounds {
			tag := str(ib["tag"])
			if tag == "" {
				continue
			}
			out = append(out, &adoptedInbound{
				Tag:  tag,
				Type: str(ib["type"]),
				Port: num(ib["listen_port"]),
				File: name,
				Raw:  ib,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	return out
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func num(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case string:
		i, _ := strconv.Atoi(n)
		return i
	}
	return 0
}

func obj(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func strList(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case []any:
		var out []string
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// adoptableProtocols 是能为已有入站推出分享链接的协议。
var adoptableProtocols = map[string]bool{
	"vless": true, "vmess": true, "trojan": true, "anytls": true,
	"tuic": true, "hysteria2": true, "shadowsocks": true,
}

// toNative 把已有入站转成 nativeInbound 的形态，供生成分享链接。
// 协议不认识时返回 nil。
func (a *adoptedInbound) toNative() *nativeInbound {
	if !adoptableProtocols[a.Type] {
		return nil
	}
	ib := &nativeInbound{
		ID:       a.ID,
		Port:     a.Port,
		Protocol: a.Type,
		Network:  "tcp",
		Security: "none",
		Remark:   a.Tag,
		Enable:   true,
	}
	if quicProtocols[a.Type] {
		ib.Network = "udp"
	}

	if tr := obj(a.Raw["transport"]); tr != nil {
		switch t := str(tr["type"]); t {
		case "ws", "httpupgrade":
			ib.Network = t
			ib.Path = str(tr["path"])
			ib.Host = str(tr["host"])
			if ib.Host == "" {
				if h := obj(tr["headers"]); h != nil {
					if v := strList(h["Host"]); len(v) > 0 {
						ib.Host = v[0]
					}
				}
			}
		case "grpc":
			ib.Network = "grpc"
			ib.Path = str(tr["service_name"])
		default:
			// http / quic 等 fanout 不生成链接的传输
			if t != "" {
				ib.Network = t
			}
		}
	}

	if tls := obj(a.Raw["tls"]); tls != nil && tls["enabled"] == true {
		if r := obj(tls["reality"]); r != nil && r["enabled"] == true {
			hs := obj(r["handshake"])
			dest := str(hs["server"])
			if p := num(hs["server_port"]); p > 0 {
				dest += ":" + strconv.Itoa(p)
			}
			names := strList(tls["server_name"])
			if len(names) == 0 && hs != nil {
				names = []string{str(hs["server"])}
			}
			priv := str(r["private_key"])
			pub, _ := realityPublicKey(priv)
			ib.Security = "reality"
			ib.Reality = &realityConfig{
				Dest: dest, ServerNames: names, PrivateKey: priv, PublicKey: pub,
				ShortIDs: strList(r["short_id"]), Fingerprint: "chrome",
			}
		} else {
			ib.Security = "tls"
			conf := &tlsConfig{
				ServerName: str(tls["server_name"]),
				CertFile:   str(tls["certificate_path"]),
				KeyFile:    str(tls["key_path"]),
			}
			if conf.CertFile != "" {
				if self, sha, cn, err := certInfo(conf.CertFile); err == nil {
					conf.SelfSigned, conf.CertSha256 = self, sha
					if conf.ServerName == "" {
						conf.ServerName = cn
					}
				}
			}
			ib.TLS = conf
		}
	}

	if a.Type == "shadowsocks" {
		ib.Method = str(a.Raw["method"])
	}

	users, _ := a.Raw["users"].([]any)
	for i, u := range users {
		m := obj(u)
		if m == nil {
			continue
		}
		name := str(m["name"])
		if name == "" {
			name = "user-" + strconv.Itoa(i+1)
		}
		ib.Clients = append(ib.Clients, nativeClient{
			Email: name, ID: str(m["uuid"]), Password: str(m["password"]),
			Flow: str(m["flow"]), Enable: true,
		})
	}
	// 单用户 shadowsocks 只有顶层 password；2022 多用户时顶层是服务端密码
	if a.Type == "shadowsocks" {
		top := str(a.Raw["password"])
		if len(ib.Clients) == 0 && top != "" {
			ib.Clients = []nativeClient{{Email: "default", Password: top, Enable: true}}
		} else {
			ib.ServerPassword = top
		}
	}
	return ib
}

// scriptLinks 从第三方脚本导出的链接目录（只读）里找出端口对得上的分享链接。
//
// 那些链接是脚本自己生成的，域名、SNI、跳过验证等参数比 fanout 推出来的更准。
func scriptLinks(dir string, port int) []string {
	files, _ := filepath.Glob(filepath.Join(dir, "*.txt"))
	var out []string
	seen := map[string]bool{}
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if !strings.Contains(line, "://") || seen[line] {
				continue
			}
			if linkPort(line) == port {
				seen[line] = true
				out = append(out, line)
			}
		}
		fh.Close()
	}
	return out
}

// linkPort 取分享链接里的端口；vmess 的 base64 形式解析不了，返回 0。
func linkPort(link string) int {
	u, err := url.Parse(link)
	if err != nil {
		return 0
	}
	p, _ := strconv.Atoi(u.Port())
	return p
}
