package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// panelLayout 是管理面板要碰到的全部路径。
//
// 默认值与 sing-box-08.sh（以及 sb.sh）的布局逐项对齐：脚本建的东西网页能直接读懂、
// 网页写的东西脚本也能直接用。命令行 -singbox-bin / -singbox-conf 改了路径时，
// 这里跟着推导；单测用 panelLayoutOverride 指向临时目录。
type panelLayout struct {
	Bin        string // sing-box 可执行文件
	ConfDir    string // sing-box run -C 的配置目录
	URLDir     string // 每个入站的分享链接 *.txt
	SubFile    string // 脚本导出的总订阅（base64）
	MainConfig string // config.json（v2ray_api 流量统计）
	SelfCert   string // 自签证书 /etc/sing-box/cert.pem
	SelfKey    string // 自签私钥 /etc/sing-box/private.key

	UserManagerDir string // /etc/sing-box/user_manager
	LimitsDir      string // 流量限制
	TrafficState   string // 流量统计状态
	SpeedStore     string // 限速存储（与 fanout userlimit.go 共用）

	CertDirs  []string // 证书目录，按脚本顺序
	CertTrash string   // 删证书时移到这里的回收目录

	NginxConfDir  string // /etc/nginx/conf.d
	NginxUserDir  string // /etc/nginx/conf.d/singbox_users
	NginxSubConf  string // /etc/nginx/conf.d/singbox_sub.conf
	NginxTrashDir string // 删站点时的回收目录

	QuickDir      string // /etc/sing-box/quick（临时隧道日志与标记）
	QuickBin      string // /etc/sing-box/cloudflared
	QuickUnitDir  string // systemd 单元目录
	CFTokenFile   string // /etc/sing-box/token
	CFGlobalFile  string // 网页保存的 Global Key（脚本只放环境变量，这里落盘 0600）
	AcmeSh        string // acme.sh 路径
	FanoutDir     string // /var/lib/fanout
	SubServiceURL string // 订阅服务地址（脚本写死 127.0.0.1:18080）
}

// layoutOverride 只给单测用，置空即回到真实路径。
var layoutOverride *panelLayout

// panelPaths 返回当前生效的路径集合。
func panelPaths() panelLayout {
	if layoutOverride != nil {
		return *layoutOverride
	}
	base := filepath.Dir(singbox.Bin) // 通常是 /etc/sing-box
	if base == "." || base == "/" {
		base = "/etc/sing-box"
	}
	return panelLayout{
		Bin:            singbox.Bin,
		ConfDir:        singbox.ConfDir,
		URLDir:         filepath.Join(base, "url"),
		SubFile:        filepath.Join(base, "sub.txt"),
		MainConfig:     filepath.Join(singbox.ConfDir, "config.json"),
		SelfCert:       filepath.Join(base, "cert.pem"),
		SelfKey:        filepath.Join(base, "private.key"),
		UserManagerDir: filepath.Join(base, "user_manager"),
		LimitsDir:      filepath.Join(base, "user_manager", "limits"),
		TrafficState:   filepath.Join(base, "user_manager", "traffic", "state.json"),
		SpeedStore:     filepath.Join(base, "user_manager", "speed_limits.json"),
		CertDirs:       []string{"/root/cert", "/etc/nginx/cert"},
		CertTrash:      "/root/cert/.fanout-trash",
		NginxConfDir:   "/etc/nginx/conf.d",
		NginxUserDir:   "/etc/nginx/conf.d/singbox_users",
		NginxSubConf:   "/etc/nginx/conf.d/singbox_sub.conf",
		NginxTrashDir:  "/etc/nginx/conf.d/.fanout-trash",
		QuickDir:       filepath.Join(base, "quick"),
		QuickBin:       filepath.Join(base, "cloudflared"),
		QuickUnitDir:   "/etc/systemd/system",
		CFTokenFile:    filepath.Join(base, "token"),
		CFGlobalFile:   filepath.Join(base, "cf-global.json"),
		AcmeSh:         filepath.Join(homeDir(), ".acme.sh", "acme.sh"),
		FanoutDir:      fanoutWorkDir(),
		SubServiceURL:  "http://127.0.0.1:18080",
	}
}

// homeDir 返回 root 的家目录；acme.sh 装在 $HOME/.acme.sh。
func homeDir() string {
	if h := strings.TrimSpace(os.Getenv("HOME")); h != "" {
		return h
	}
	return "/root"
}

// fanoutWorkDir 是 fanout 自己的工作目录（口令、basepath、settings.json）。
func fanoutWorkDir() string {
	if v := strings.TrimSpace(os.Getenv("FANOUT_WORK_DIR")); v != "" {
		return v
	}
	return "/var/lib/fanout"
}

// ---- 执行系统命令 ----

// adminExec 执行 systemctl / nginx / nft / openssl 这类管理命令。
//
// 故意不走 inMainNetns：这些命令跟隧道无关，走 setns 反而在非 root 环境
// （单测）里直接失败。单测会把 adminExec 换掉。
var adminExec = func(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// adminExecInput 跟 adminExec 一样，但把 stdin 喂进去（acme.sh 之类）。
var adminExecInput = func(stdin string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// adminExecEnv 带额外环境变量执行，acme.sh 需要 CF_Token 之类。
var adminExecEnv = func(env []string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// cmdAvailable 判断命令装没装。做成变量是为了单测能假装装了 nft / nginx。
var cmdAvailable = func(name string) bool {
	if name == "" {
		return false
	}
	_, err := exec.LookPath(name)
	return err == nil
}

func haveCmd(name string) bool { return cmdAvailable(name) }

// trimOut 截断命令输出，避免把几 MB 的日志塞进界面。
func trimOut(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 4000 {
		return s[:4000] + "\n…"
	}
	return s
}

// ---- 校验 ----

var (
	reDomain = regexp.MustCompile(`^(?i)[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)
	// 订阅用户名：脚本用 [A-Za-z0-9._-]+，这里同样限制长度，顺便挡住 ".."
	reUserName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,48}$`)
	// Nginx 站点文件名：只允许 conf.d 里平铺的 *.conf
	reConfName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}\.conf$`)
	// 入站配置文件名：{类型}-{编号}.json
	reNodeFile = regexp.MustCompile(`^([a-z0-9-]{1,32})-([0-9]{1,3})\.json$`)
)

// validDomain 只接受普通域名（可选通配前缀 *），挡掉路径穿越和奇怪字符。
func validDomain(d string) bool {
	d = strings.TrimSpace(d)
	if d == "" || len(d) > 253 {
		return false
	}
	d = strings.TrimPrefix(d, "*.")
	if strings.ContainsAny(d, "/\\ \t\n:?&#{}[]") {
		return false
	}
	return reDomain.MatchString(d)
}

// validUserName 校验订阅用户名。
func validUserName(n string) bool {
	if !reUserName.MatchString(n) || n == "." || n == ".." {
		return false
	}
	return true
}

// validConfName 校验 Nginx 站点文件名。
func validConfName(n string) bool {
	return reConfName.MatchString(n) && !strings.Contains(n, "..")
}

// validNodeFile 校验 sing-box 入站配置文件名。
func validNodeFile(n string) bool {
	return reNodeFile.MatchString(n)
}

// splitNodeFile 拆出 {类型, 编号}。
func splitNodeFile(name string) (string, int, bool) {
	m := reNodeFile.FindStringSubmatch(name)
	if m == nil {
		return "", 0, false
	}
	n, err := strconv.Atoi(m[2])
	if err != nil {
		return "", 0, false
	}
	return m[1], n, true
}

// safeJoin 把用户给的名字拼到 base 下，并确认结果仍在 base 里。
func safeJoin(base, name string) (string, error) {
	if name == "" || strings.Contains(name, "/") || strings.Contains(name, "\\") ||
		name == "." || name == ".." {
		return "", fmt.Errorf("非法名称 %q", name)
	}
	p := filepath.Join(base, name)
	if filepath.Dir(p) != filepath.Clean(base) {
		return "", fmt.Errorf("非法路径 %q", name)
	}
	return p, nil
}

// validPort 检查端口范围。
func validPort(p int) bool { return p >= 1 && p <= 65535 }

// ---- 写盘 ----

// writeFileAtomic 先写临时文件再改名，避免写到一半被读走。
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp := path + ".fanout-tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// writeVerified 写文件，然后跑 verify 校验；失败就把原内容放回去。
//
// 这是改 sing-box / nginx 配置的统一入口：脚本允许写坏一两次，
// 网页没人盯着，写坏了必须自己退回去。
func writeVerified(path string, data []byte, mode os.FileMode, verify func() error) error {
	var old []byte
	had := false
	if blob, err := os.ReadFile(path); err == nil {
		old, had = blob, true
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := writeFileAtomic(path, data, mode); err != nil {
		return err
	}
	if verify == nil {
		return nil
	}
	if err := verify(); err != nil {
		if had {
			_ = writeFileAtomic(path, old, mode)
		} else {
			_ = os.Remove(path)
		}
		return err
	}
	return nil
}

// moveToTrash 把文件或目录挪进回收目录，而不是直接删掉。
// 删证书、删 Nginx 站点这种不可逆操作留一手，用户后悔还能找回来。
func moveToTrash(trashDir, path string) (string, error) {
	if err := os.MkdirAll(trashDir, 0700); err != nil {
		return "", err
	}
	dst := filepath.Join(trashDir, filepath.Base(path)+"."+nowStamp())
	if err := os.Rename(path, dst); err != nil {
		return "", err
	}
	return dst, nil
}

// timeNow 给时间戳留一个可替换的入口（测试里要固定文件名）。
var timeNow = time.Now

func nowStamp() string {
	return timeNow().Format("20060102-150405")
}
