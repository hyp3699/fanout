package main

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// nftables 操作。与 sing-box-08.sh 的 allow_port / close_port 完全同一套：
// 放行规则写在 inet filter script_input 链，命令带 comment "ScriptManaged"，
// 持久化用 nft list ruleset > /etc/nftables.conf。

// nftChain 取一条链的完整输出；链不存在时返回空串。
func nftChain(chain string) string {
	if !haveCmd("nft") {
		return ""
	}
	out, err := adminExec("nft", "list", "chain", chain)
	if err != nil {
		return ""
	}
	return out
}

// nftPortOpen 判断某个端口在链里是否已经 accept。
// 用词界匹配，删/查 80 时不会误中 8080。
func nftPortOpen(chain string, port int, proto string) bool {
	if chain == "" || port <= 0 {
		return false
	}
	pat := regexp.MustCompile(`(^|[[:space:]])` + proto + ` dport ` + strconv.Itoa(port) + `([[:space:],;]|$)`)
	return pat.MatchString(chain)
}

// ensureNftBase 建好 inet filter 表和 script_input 链（脚本 ensure_nft_env 的最小版）。
// 顺序与脚本一致：input 链已有跳转就不重复插。
func ensureNftBase() error {
	if _, err := adminExec("nft", "add", "table", "inet", "filter"); err != nil {
		// 表已存在也会返回非零，用 list 确认一次
		if nftChain("inet filter input") == "" {
			return fmt.Errorf("创建 nft 表失败: %s", trimOut(err.Error()))
		}
	}
	if nftChain("inet filter input") == "" {
		if _, err := adminExec("nft", "add", "chain", "inet", "filter", "input",
			"{ type filter hook input priority 0; policy accept; }"); err != nil {
			return fmt.Errorf("创建 input 链失败")
		}
	}
	if nftChain("inet filter script_input") == "" {
		if _, err := adminExec("nft", "add", "chain", "inet", "filter", "script_input"); err != nil {
			return fmt.Errorf("创建 script_input 链失败")
		}
	}
	if !strings.Contains(nftChain("inet filter input"), "script_input") {
		_, _ = adminExec("nft", "add", "rule", "inet", "filter", "input", "jump", "script_input")
	}
	return nil
}

// allowPort 放行一个端口（与脚本 allow_port 行为一致）。
func allowPort(port int, proto string) error {
	if !validPort(port) {
		return fmt.Errorf("端口无效: %d", port)
	}
	if proto != "tcp" && proto != "udp" {
		return fmt.Errorf("协议无效: %s", proto)
	}
	if !haveCmd("nft") {
		return fmt.Errorf("没有 nft 命令，无法放行端口")
	}
	if err := ensureNftBase(); err != nil {
		return err
	}
	if nftPortOpen(nftChain("inet filter script_input"), port, proto) {
		return nil
	}
	out, err := adminExec("nft", "add", "rule", "inet", "filter", "script_input",
		proto, "dport", strconv.Itoa(port), "accept", "comment", "ScriptManaged")
	if err != nil {
		return fmt.Errorf("nft 放行失败: %s", trimOut(out))
	}
	_ = saveNftRules()
	return nil
}

// purgePortRules 删掉某个端口的放行规则（script_input + input 两条链都查）。
func purgePortRules(port int) {
	if !haveCmd("nft") || port <= 0 {
		return
	}
	word := regexp.MustCompile(`\b(dport|sport) ` + strconv.Itoa(port) + `\b`)
	for _, chain := range []string{"script_input", "input"} {
		out, err := adminExec("nft", "-a", "list", "chain", "inet", "filter", chain)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(out, "\n") {
			if !word.MatchString(line) {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) == 0 || fields[len(fields)-1] == "" {
				continue
			}
			handle := fields[len(fields)-1]
			if _, err := strconv.Atoi(handle); err != nil {
				continue
			}
			_, _ = adminExec("nft", "delete", "rule", "inet", "filter", chain, "handle", handle)
		}
	}
	_ = saveNftRules()
}

// saveNftRules 把当前规则集写进 /etc/nftables.conf，重启后还在。
func saveNftRules() error {
	if !haveCmd("nft") {
		return fmt.Errorf("没有 nft 命令")
	}
	out, err := adminExec("nft", "list", "ruleset")
	if err != nil || strings.TrimSpace(out) == "" {
		return fmt.Errorf("读取 nft 规则失败")
	}
	// 跳过 fail2ban 自己的表，跟脚本 save_nft_rules 一样
	var kept []string
	skip := false
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "table inet f2b-table") {
			skip = true
			continue
		}
		if strings.HasPrefix(line, "table ") {
			skip = false
		}
		if !skip {
			kept = append(kept, line)
		}
	}
	body := "flush ruleset\n" + strings.Join(kept, "\n") + "\n"
	tmp := "/etc/nftables.conf.tmp"
	if err := writeFileAtomic(tmp, []byte(body), 0644); err != nil {
		return err
	}
	if _, err := adminExec("nft", "-c", "-f", tmp); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("nftables.conf 校验失败，未替换")
	}
	blob, err := os.ReadFile(tmp)
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	_ = os.Remove(tmp)
	return writeFileAtomic("/etc/nftables.conf", blob, 0644)
}

// firewallPolicy 返回 input 链的默认策略（accept / drop）。
func firewallPolicy() string {
	chain := nftChain("inet filter input")
	i := strings.LastIndex(chain, "policy ")
	if i < 0 {
		return ""
	}
	rest := strings.TrimSpace(chain[i+len("policy "):])
	rest = strings.NewReplacer(";", " ", "}", " ", "{", " ").Replace(rest)
	for _, f := range strings.Fields(rest) {
		return strings.TrimSpace(f)
	}
	return ""
}
