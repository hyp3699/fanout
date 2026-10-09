package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
)

// nativeClient 是一个可连接的客户端凭据。tuic 同时用 ID 和 Password。
type nativeClient struct {
	Email    string `json:"email"`
	ID       string `json:"id"`       // vless/vmess/tuic 用 UUID
	Password string `json:"password"` // trojan/anytls/hysteria2/tuic/shadowsocks 用密码
	Enable   bool   `json:"enable"`
	// Flow 只对 VLESS 有意义，取值 "" 或 xtls-rprx-vision。
	Flow string `json:"flow,omitempty"`
}

// nativeInbound 是一个入站在 fanout 眼里的形态，只用来生成分享链接。
//
// 入站全部来自 sing-box 配置目录里已有的文件（见 singbox_adopt.go 的 toNative），
// fanout 不建入站。老版本 native.json 里存的自建入站也按这个结构解码，只为迁移。
type nativeInbound struct {
	ID       int    `json:"id"`
	Port     int    `json:"port"`
	Protocol string `json:"protocol"` // vless | vmess | trojan | anytls | tuic | hysteria2 | shadowsocks
	Network  string `json:"network"`  // tcp | ws | grpc | httpupgrade；tuic/hysteria2 为 udp
	Path     string `json:"path"`     // ws/httpupgrade 路径，grpc 用作 service_name
	Host     string `json:"host"`     // ws/httpupgrade 的 Host
	// Security 是传输层安全：none | tls | reality
	Security string         `json:"security"`
	TLS      *tlsConfig     `json:"tls,omitempty"`
	Reality  *realityConfig `json:"reality,omitempty"`
	Remark   string         `json:"remark"`
	Enable   bool           `json:"enable"`
	Clients  []nativeClient `json:"clients"`
	// BoundTo 只在解码老版本数据时有值：当年整条入站绑定的出口
	BoundTo string `json:"bound_to,omitempty"`

	Method         string `json:"-"` // shadowsocks 加密方式
	ServerPassword string `json:"-"` // shadowsocks 2022 多用户的服务端密码
}

// tlsConfig 是标准 TLS 的配置，从入站的 tls 段读出来。
type tlsConfig struct {
	ServerName string `json:"server_name"`
	CertFile   string `json:"cert_file"`
	KeyFile    string `json:"key_file"`
	// SelfSigned 记录证书是自签的，分享链接要带 insecure 与证书指纹
	SelfSigned bool `json:"self_signed"`
	// CertSha256 是证书的 SHA-256 指纹（十六进制）
	CertSha256 string `json:"cert_sha256,omitempty"`
}

// realityConfig 是 REALITY 的配置。PublicKey 由私钥推出，供分享链接用。
type realityConfig struct {
	Dest        string   `json:"dest"`
	ServerNames []string `json:"server_names"`
	PrivateKey  string   `json:"private_key"`
	PublicKey   string   `json:"public_key"`
	ShortIDs    []string `json:"short_ids"`
	Fingerprint string   `json:"fingerprint"`
}

func (n *nativeInbound) netOrTCP() string {
	if n.Network == "" {
		return "tcp"
	}
	return n.Network
}

func (n *nativeInbound) securityOrNone() string {
	if n.Security == "" {
		return "none"
	}
	return n.Security
}

// legacyInboundTag 是老版本 fanout 给自建入站起的 tag（fanout-in-<端口>-<传输>），
// 迁移时用它把老 ID 对上同名的现有入站，界面上的编号不变。
func legacyInboundTag(ib *nativeInbound) string {
	return fmt.Sprintf("fanout-in-%d-%s", ib.Port, ib.netOrTCP())
}

// nativeStore 是 fanout 这边的持久状态（工作目录下的 native.json）。
type nativeStore struct {
	NextID int `json:"next_id"`
	// Adopted 按 sing-box tag 记配置目录里现有入站的界面 ID，重启后编号不变。
	// 入站本身不归 fanout 管，这里只存 ID。
	Adopted map[string]*adoptedState `json:"adopted,omitempty"`
	// Rules 是有序的分流规则，按顺序写进 fanout-route.json
	Rules      []*RouteRule `json:"rules"`
	NextRuleID int          `json:"next_rule_id"`

	// LegacyInbounds 是老版本 fanout 自建的入站，只在加载时读出来做迁移，不再写回。
	LegacyInbounds []*nativeInbound `json:"inbounds,omitempty"`
}

// adoptedState 是一个现有入站在 fanout 这边的状态。
type adoptedState struct {
	ID int `json:"id"`
	// BoundTo 是老版本的"整条入站绑出口"，只在加载时读出来做迁移，不再写回
	BoundTo string `json:"bound_to,omitempty"`
}

func nativeStatePath(dir string) string { return filepath.Join(dir, "native.json") }

// loadNativeStore 读状态文件。返回值 migrated 表示读到了老版本的数据并已就地迁移，
// 调用方应当尽快落盘并重写一次 sing-box 配置。
func loadNativeStore(dir string) (st *nativeStore, migrated bool, err error) {
	blob, err := os.ReadFile(nativeStatePath(dir))
	if os.IsNotExist(err) {
		return &nativeStore{NextID: 1, NextRuleID: 1}, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	st = &nativeStore{}
	if err := json.Unmarshal(blob, st); err != nil {
		return nil, false, fmt.Errorf("解析 %s 失败: %w", nativeStatePath(dir), err)
	}
	if st.NextID < 1 {
		st.NextID = 1
	}
	if st.NextRuleID < 1 {
		st.NextRuleID = 1
	}
	for _, r := range st.Rules {
		if r.ID >= st.NextRuleID {
			st.NextRuleID = r.ID + 1
		}
	}
	return st, st.migrateLegacy(), nil
}

// migrateLegacy 把老版本的数据迁到新模型。
//
// 老版本有两样东西新版不要了：
//   - fanout 自建的入站：它们的 fanout-in-*.json 原样留在配置目录里，新版当作普通的
//     已有入站只读显示（沿用原来的界面 ID），不再由 fanout 改写或删除；
//   - "整条入站绑到出口"：新版只按规则分流，整条绑定一律丢弃并在日志里说明，
//     需要的话在界面「分流规则」里重建（可以勾「全部流量」）。
func (s *nativeStore) migrateLegacy() bool {
	migrated := false
	if s.Adopted == nil {
		s.Adopted = map[string]*adoptedState{}
	}
	for _, ib := range s.LegacyInbounds {
		migrated = true
		tag := legacyInboundTag(ib)
		if _, ok := s.Adopted[tag]; !ok && ib.ID > 0 {
			s.Adopted[tag] = &adoptedState{ID: ib.ID}
		}
		if ib.ID >= s.NextID {
			s.NextID = ib.ID + 1
		}
		msg := fmt.Sprintf("迁移: 老版本自建的入站 %s（%s 端口 %d）不再由 fanout 管理，配置目录里的 %s.json 原样保留、当作已有入站只读显示",
			tag, ib.Protocol, ib.Port, tag)
		if !ib.Enable {
			msg += "（它当时是停用状态，文件多半已不存在）"
		}
		if ib.BoundTo != "" {
			msg += fmt.Sprintf("；它原来整条绑到出口 %s，新版不再整条转发，已丢弃，需要的话到「分流规则」里重建", ib.BoundTo)
		}
		log.Print(msg)
	}
	s.LegacyInbounds = nil

	tags := make([]string, 0, len(s.Adopted))
	for tag := range s.Adopted {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	for _, tag := range tags {
		st := s.Adopted[tag]
		if st.BoundTo != "" {
			migrated = true
			log.Printf("迁移: 入站 %s 原来整条绑到出口 %s，新版不再整条转发，已丢弃该绑定；需要的话到「分流规则」里重建", tag, st.BoundTo)
			st.BoundTo = ""
		}
	}
	return migrated
}

func (s *nativeStore) save(dir string) error {
	blob, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := nativeStatePath(dir) + ".tmp"
	if err := os.WriteFile(tmp, blob, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, nativeStatePath(dir))
}

func (s *nativeStore) ruleByID(id int) (int, *RouteRule) {
	for i, r := range s.Rules {
		if r.ID == id {
			return i, r
		}
	}
	return -1, nil
}
