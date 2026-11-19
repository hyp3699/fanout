package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Auth 给管理界面加一层登录。
// 口令存在工作目录下，首次启动自动生成，避免公网上裸奔。
type Auth struct {
	dir      string
	username string
	password string
	// totpSecret 非空表示开了两步验证（base32，存 dir/totp）
	totpSecret string
	// totpPending 是「开启两步验证」时生成、还没用验证码确认的密钥
	totpPending string
	// localToken 写到 dir/localtoken（0600，只有 root 能读），
	// sing-box-08.sh 在本机用它调接口，不用知道口令也不受两步验证影响
	localToken string
	mu         sync.RWMutex
	sessions   map[string]time.Time
	// 按来源 IP 记录登录失败，挡低速凭据喷洒
	fails map[string]*loginFails
}

// loginFails 跟踪单个来源 IP 的连续失败。
type loginFails struct {
	count   int
	last    time.Time
	blocked time.Time
}

const sessionTTL = 12 * time.Hour

// 登录失败节流：同一 IP 连续错 loginMaxFails 次后，锁 loginBlockFor。
// 阈值给得宽松，正常用户偶尔输错不受影响；成功登录会清零。
const (
	loginMaxFails  = 8
	loginBlockFor  = 2 * time.Minute
	loginFailReset = 10 * time.Minute
)

// NewAuth 载入或生成访问口令与用户名。返回口令是否为本次新建。
func NewAuth(dir string) (*Auth, bool, error) {
	path := filepath.Join(dir, "password")
	created := false

	blob, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		pw, gerr := randomToken(9)
		if gerr != nil {
			return nil, false, gerr
		}
		if werr := os.WriteFile(path, []byte(pw+"\n"), 0600); werr != nil {
			return nil, false, fmt.Errorf("写口令文件失败: %w", werr)
		}
		blob = []byte(pw)
		created = true
	} else if err != nil {
		return nil, false, err
	}

	user := defaultUsername
	if ub, uerr := os.ReadFile(filepath.Join(dir, "username")); uerr == nil && strings.TrimSpace(string(ub)) != "" {
		user = strings.TrimSpace(string(ub))
	} else {
		_ = os.WriteFile(filepath.Join(dir, "username"), []byte(user+"\n"), 0600)
	}
	totp := ""
	if tb, terr := os.ReadFile(filepath.Join(dir, "totp")); terr == nil {
		totp = strings.TrimSpace(string(tb))
	}
	lt, err := randomToken(24)
	if err != nil {
		return nil, false, err
	}
	_ = os.WriteFile(filepath.Join(dir, "localtoken"), []byte(lt+"\n"), 0600)

	return &Auth{
		dir:        dir,
		username:   user,
		password:   strings.TrimSpace(string(blob)),
		totpSecret: totp,
		localToken: lt,
		sessions:   map[string]time.Time{},
		fails:      map[string]*loginFails{},
	}, created, nil
}

const defaultUsername = "admin"

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// check 比对口令，用恒定时间比较避免时序泄漏。
func (a *Auth) check(pw string) bool {
	a.mu.RLock()
	cur := a.password
	a.mu.RUnlock()
	want := sha256.Sum256([]byte(cur))
	got := sha256.Sum256([]byte(pw))
	return subtle.ConstantTimeCompare(want[:], got[:]) == 1
}

func eqConst(a, b string) bool {
	ha := sha256.Sum256([]byte(a))
	hb := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}

// checkLogin 用户名、口令、两步验证码（开了才要）全部对上才算通过。
// 三项都比较完再给结果，不从耗时上透露是哪一项错了。
func (a *Auth) checkLogin(user, pw, code string) bool {
	a.mu.RLock()
	wantUser, secret := a.username, a.totpSecret
	a.mu.RUnlock()
	okUser := eqConst(strings.TrimSpace(user), wantUser)
	okPw := a.check(pw)
	okCode := secret == "" || totpVerify(secret, code)
	return okUser && okPw && okCode
}

// Username 当前登录用户名。
func (a *Auth) Username() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.username
}

// TOTPEnabled 是否开了两步验证。
func (a *Auth) TOTPEnabled() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.totpSecret != ""
}

// SetUsername 改登录用户名：字母数字和 . _ - @，3-32 位。
func (a *Auth) SetUsername(u string) error {
	u = strings.TrimSpace(u)
	if len(u) < 3 || len(u) > 32 {
		return fmt.Errorf("用户名 3-32 位")
	}
	for _, c := range u {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._-@", c)) {
			return fmt.Errorf("用户名只能用字母、数字和 . _ - @")
		}
	}
	if err := writeFileAtomic(filepath.Join(a.dir, "username"), []byte(u+"\n"), 0600); err != nil {
		return fmt.Errorf("保存用户名失败: %w", err)
	}
	a.mu.Lock()
	a.username = u
	a.mu.Unlock()
	return nil
}

// RevokeOthers 踢掉除 keep 以外的所有会话（改口令/用户名/两步验证后用）。
func (a *Auth) RevokeOthers(keep string) {
	a.mu.Lock()
	for k := range a.sessions {
		if k != keep {
			delete(a.sessions, k)
		}
	}
	a.mu.Unlock()
}

// SessionCount 当前有效会话数。
func (a *Auth) SessionCount() int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	n := 0
	for _, exp := range a.sessions {
		if time.Now().Before(exp) {
			n++
		}
	}
	return n
}

// ---- 两步验证（TOTP，RFC 6238：30 秒一步、6 位、SHA1，兼容 Google Authenticator 等） ----

func totpCode(secret string, step int64) string {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		return ""
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	h := hmac.New(sha1.New, key)
	h.Write(msg[:])
	sum := h.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", v%1000000)
}

// totpVerify 允许前后各一步（±30 秒）的时钟误差。
func totpVerify(secret, code string) bool {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != 6 {
		return false
	}
	now := time.Now().Unix() / 30
	ok := false
	for d := int64(-1); d <= 1; d++ {
		if eqConst(totpCode(secret, now+d), code) {
			ok = true
		}
	}
	return ok
}

// TOTPBegin 生成一个待确认的密钥，返回密钥与 otpauth:// 地址（手机验证器扫码/点开即可添加）。
func (a *Auth) TOTPBegin(label string) (string, string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
	a.mu.Lock()
	a.totpPending = secret
	user := a.username
	a.mu.Unlock()
	if label == "" {
		label = "fanout"
	}
	uri := "otpauth://totp/" + url.PathEscape(label+":"+user) + "?secret=" + secret +
		"&issuer=" + url.QueryEscape(label) + "&digits=6&period=30"
	return secret, uri, nil
}

// TOTPConfirm 用验证码确认待开启的密钥，通过才落盘生效。
func (a *Auth) TOTPConfirm(code string) error {
	a.mu.RLock()
	pending := a.totpPending
	a.mu.RUnlock()
	if pending == "" {
		return fmt.Errorf("先点「开启两步验证」生成密钥")
	}
	if !totpVerify(pending, code) {
		return fmt.Errorf("验证码不对，检查手机时间是否准确")
	}
	if err := writeFileAtomic(filepath.Join(a.dir, "totp"), []byte(pending+"\n"), 0600); err != nil {
		return fmt.Errorf("保存失败: %w", err)
	}
	a.mu.Lock()
	a.totpSecret, a.totpPending = pending, ""
	a.mu.Unlock()
	return nil
}

// TOTPDisable 关两步验证，需要当前验证码。
func (a *Auth) TOTPDisable(code string) error {
	a.mu.RLock()
	secret := a.totpSecret
	a.mu.RUnlock()
	if secret == "" {
		return nil
	}
	if !totpVerify(secret, code) {
		return fmt.Errorf("验证码不对")
	}
	if err := os.Remove(filepath.Join(a.dir, "totp")); err != nil && !os.IsNotExist(err) {
		return err
	}
	a.mu.Lock()
	a.totpSecret = ""
	a.mu.Unlock()
	return nil
}

// SetPassword 改访问口令并落盘。空口令拒绝，避免误改成无密码裸奔。
// 改完不动已有会话：当前登录的浏览器不会被踢，新登录才用新口令。
func (a *Auth) SetPassword(pw string) error {
	pw = strings.TrimSpace(pw)
	if pw == "" {
		return fmt.Errorf("口令不能为空")
	}
	if len(pw) < 8 {
		return fmt.Errorf("口令至少 8 位")
	}
	path := filepath.Join(a.dir, "password")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(pw+"\n"), 0600); err != nil {
		return fmt.Errorf("写口令文件失败: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("保存口令失败: %w", err)
	}
	a.mu.Lock()
	a.password = pw
	a.mu.Unlock()
	return nil
}

// issue 发一个会话 token。
func (a *Auth) issue() (string, error) {
	tok, err := randomToken(16)
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	a.sessions[tok] = time.Now().Add(sessionTTL)
	// 顺手清掉过期会话
	for k, exp := range a.sessions {
		if time.Now().After(exp) {
			delete(a.sessions, k)
		}
	}
	a.mu.Unlock()
	return tok, nil
}

func (a *Auth) valid(tok string) bool {
	a.mu.RLock()
	exp, ok := a.sessions[tok]
	a.mu.RUnlock()
	return ok && time.Now().Before(exp)
}

const sessionCookie = "fanout_session"

// securityHeaders 给所有响应加上基础安全头：禁止被 iframe 嵌入（防点击劫持）、
// 禁止 MIME 嗅探、不外泄 Referer、限制资源只能来自本站。
func securityHeaders(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("X-Frame-Options", "DENY")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; "+
		"style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; "+
		"frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	if isHTTPS(r) {
		h.Set("Strict-Transport-Security", "max-age=31536000")
	}
	h.Set("Cache-Control", "no-store")
}

// isHTTPS 直连 TLS，或本机 nginx 反代时带了 X-Forwarded-Proto: https。
func isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return fromLoopback(r) && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func fromLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// sameOrigin 防 CSRF：会改东西的请求，浏览器带的 Origin（没有就看 Referer）必须是本站。
// 两个头都没有的（curl、脚本）放行，它们本来就带不上浏览器里的会话 cookie。
func sameOrigin(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	src := r.Header.Get("Origin")
	if src == "" {
		src = r.Header.Get("Referer")
	}
	if src == "" {
		return true
	}
	u, err := url.Parse(src)
	if err != nil || u.Host == "" {
		return false
	}
	host := r.Host
	if fh := r.Header.Get("X-Forwarded-Host"); fh != "" && fromLoopback(r) {
		host = fh
	}
	// 只比主机名：nginx 反代时 $host 不带端口，而浏览器的 Origin 带着非 443 端口
	h := host
	if hh, _, err := net.SplitHostPort(host); err == nil {
		h = hh
	}
	return strings.EqualFold(u.Hostname(), strings.Trim(h, "[]"))
}

// Wrap 保护一个 handler，未登录时 API 返回 401、页面跳登录。
func (a *Auth) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		securityHeaders(w, r)
		if !sameOrigin(r) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "跨站请求被拒绝"})
			return
		}
		if r.URL.Path == "/login" {
			a.handleLogin(w, r)
			return
		}
		if r.URL.Path == "/logout" {
			a.handleLogout(w, r)
			return
		}
		// 本机脚本：带 localtoken 且来自 127.0.0.1
		if lt := r.Header.Get("X-Fanout-Local"); lt != "" && fromLoopback(r) && eqConst(lt, a.localToken) {
			next.ServeHTTP(w, r)
			return
		}
		if c, err := r.Cookie(sessionCookie); err == nil && a.valid(c.Value) {
			next.ServeHTTP(w, r.WithContext(withSession(r.Context(), c.Value)))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "未登录"})
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(loginHTML))
	})
}

func (a *Auth) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		a.mu.Lock()
		delete(a.sessions, c.Value)
		a.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	writeJSON(w, http.StatusOK, map[string]string{"ok": "已退出"})
}

// blocked 判断某来源 IP 是否处于登录冷却期。
func (a *Auth) blocked(ip string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	f, ok := a.fails[ip]
	return ok && time.Now().Before(f.blocked)
}

// recordFail 记一次失败，达到阈值就进入冷却。
func (a *Auth) recordFail(ip string) {
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	f, ok := a.fails[ip]
	// 距上次失败太久就重新计数，避免长期累积误伤
	if !ok || (f.blocked.IsZero() && now.Sub(f.last) > loginFailReset) {
		f = &loginFails{}
		a.fails[ip] = f
	}
	f.count++
	f.last = now
	if f.count >= loginMaxFails {
		f.blocked = now.Add(loginBlockFor)
		f.count = 0
	}
	// 顺手清掉早已过期的记录，别让 map 无限增长
	for k, v := range a.fails {
		if now.Sub(v.last) > loginFailReset && now.After(v.blocked) {
			delete(a.fails, k)
		}
	}
}

// clearFails 登录成功后清掉该 IP 的失败记录。
func (a *Auth) clearFails(ip string) {
	a.mu.Lock()
	delete(a.fails, ip)
	a.mu.Unlock()
}

// clientIP 取来源 IP。直连时用 RemoteAddr；只有请求来自本机（nginx 反代面板域名）
// 时才采信 X-Real-IP / X-Forwarded-For，否则外面能随便伪造来绕过登录节流。
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if fromLoopback(r) {
		if v := strings.TrimSpace(r.Header.Get("X-Real-IP")); net.ParseIP(v) != nil {
			return v
		}
		if v := r.Header.Get("X-Forwarded-For"); v != "" {
			last := strings.TrimSpace(v[strings.LastIndex(v, ",")+1:])
			if net.ParseIP(last) != nil {
				return last
			}
		}
	}
	return host
}

func (a *Auth) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		// 登录页问一下要不要两步验证码
		if r.URL.Query().Get("q") == "totp" {
			writeJSON(w, http.StatusOK, map[string]bool{"totp": a.TOTPEnabled()})
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(loginHTML))
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "用 POST"})
		return
	}
	ip := clientIP(r)
	if a.blocked(ip) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "登录失败次数过多，请 2 分钟后再试"})
		return
	}
	user := r.FormValue("username")
	if !a.checkLogin(user, r.FormValue("password"), r.FormValue("code")) {
		a.recordFail(ip)
		recordOp("面板登录失败", ip+" 用户名 "+truncate(user, 32), fmt.Errorf("用户名、口令或验证码不对"))
		time.Sleep(600 * time.Millisecond) // 拖慢暴力猜测
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "用户名、口令或验证码不对"})
		return
	}
	a.clearFails(ip)
	tok, err := a.issue()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	recordOp("面板登录", ip, nil)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
	writeJSON(w, http.StatusOK, map[string]string{"ok": "已登录"})
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// ---- 会话 token 放进 context，设置接口据此只保留当前会话 ----

type sessionKey struct{}

func withSession(ctx context.Context, tok string) context.Context {
	return context.WithValue(ctx, sessionKey{}, tok)
}

func sessionFrom(r *http.Request) string {
	v, _ := r.Context().Value(sessionKey{}).(string)
	return v
}

const loginHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>fanout</title>
<style>
body{margin:0;height:100vh;display:flex;flex-direction:column;gap:16px;
  align-items:center;justify-content:center;
  background:#12151a;color:#dde3ec;
  font:13px/1.5 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}
form{background:#181c23;border:1px solid #262c36;border-radius:6px;
  padding:22px 24px;width:300px}
h1{font-size:13px;font-weight:600;margin:0 0 16px}
label{display:block;color:#8b95a5;font-size:11px;margin-bottom:6px}
input{width:100%;box-sizing:border-box;background:#0e1116;border:1px solid #262c36;
  color:#dde3ec;border-radius:4px;padding:7px 9px;font:inherit}
input:focus{outline:none;border-color:#4a9eda}
button{width:100%;margin-top:14px;background:#4a9eda;border:0;color:#0b0e12;
  font:inherit;font-weight:600;border-radius:4px;padding:8px;cursor:pointer}
[hidden]{display:none}
.err{color:#c25450;font-size:11px;margin-top:10px;min-height:14px}
</style>
</head>
<body>
<form id="f">
  <h1>fanout</h1>
  <label for="un">用户名</label>
  <input type="text" id="un" autofocus autocomplete="username" spellcheck="false" autocapitalize="off">
  <label for="pw" style="margin-top:12px">访问口令</label>
  <input type="password" id="pw" autocomplete="current-password">
  <div id="codewrap" hidden>
  <label for="code" style="margin-top:12px">两步验证码</label>
  <input type="text" id="code" inputmode="numeric" autocomplete="one-time-code" maxlength="6">
  </div>
  <button type="submit">进入</button>
  <div class="err" id="err"></div>
</form>
<script>
fetch('login?q=totp').then(r => r.json()).then(d => {
  if(d.totp) document.getElementById('codewrap').hidden = false;
}).catch(() => {});
document.getElementById('f').onsubmit = async e => {
  e.preventDefault();
  const body = new URLSearchParams({
    username: document.getElementById('un').value,
    password: document.getElementById('pw').value,
    code: document.getElementById('code').value
  });
  const r = await fetch('login', {method:'POST', body});
  if(r.ok){ location.reload(); return; }
  const d = await r.json().catch(()=>({}));
  document.getElementById('err').textContent = d.error || '登录失败';
};
</script>
</body>
</html>`
