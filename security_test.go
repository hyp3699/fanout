package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestLoginNeedsUsernamePasswordAndTOTP(t *testing.T) {
	dir := t.TempDir()
	a, _, err := NewAuth(dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = a.SetPassword("goodpassword")
	if a.Username() != defaultUsername {
		t.Fatalf("默认用户名应为 %s", defaultUsername)
	}
	if !a.checkLogin("admin", "goodpassword", "") {
		t.Fatal("正确用户名口令应通过")
	}
	if a.checkLogin("root", "goodpassword", "") {
		t.Fatal("用户名错应拒绝")
	}
	if err := a.SetUsername("ab"); err == nil {
		t.Fatal("过短用户名应拒绝")
	}
	secret, uri, err := a.TOTPBegin("fanout")
	if err != nil || !strings.HasPrefix(uri, "otpauth://totp/") {
		t.Fatalf("TOTPBegin: %v %s", err, uri)
	}
	code := totpCode(secret, time.Now().Unix()/30)
	if err := a.TOTPConfirm("000000x"); err == nil {
		t.Fatal("错误验证码不该开启")
	}
	if err := a.TOTPConfirm(code); err != nil {
		t.Fatal(err)
	}
	if a.checkLogin("admin", "goodpassword", "") {
		t.Fatal("开了两步验证后没验证码应拒绝")
	}
	if !a.checkLogin("admin", "goodpassword", totpCode(secret, time.Now().Unix()/30)) {
		t.Fatal("带正确验证码应通过")
	}
	// 重启后两步验证仍在
	b, _, _ := NewAuth(dir)
	if !b.TOTPEnabled() {
		t.Fatal("两步验证应落盘")
	}
}

func TestRFC6238Vector(t *testing.T) {
	// RFC 6238 SHA1 测试向量：密钥 "12345678901234567890"，T=59 → 94287082（取后 6 位 287082）
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	if got := totpCode(secret, 59/30); got != "287082" {
		t.Fatalf("TOTP 计算错: %s", got)
	}
}

func TestWrapRejectsCrossSiteAndSetsHeaders(t *testing.T) {
	a, _, _ := NewAuth(t.TempDir())
	_ = a.SetPassword("goodpassword")
	h := a.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))

	form := url.Values{"username": {"admin"}, "password": {"goodpassword"}}
	req := httptest.NewRequest(http.MethodPost, "http://panel.test/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://evil.test")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("跨站 POST 应 403，实际 %d", rr.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "http://panel.test/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://panel.test")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("同站登录应 200，实际 %d %s", rr.Code, rr.Body.String())
	}
	if rr.Header().Get("X-Frame-Options") != "DENY" || rr.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("缺安全响应头")
	}
	ck := rr.Result().Cookies()
	if len(ck) == 0 || ck[0].SameSite != http.SameSiteStrictMode || !ck[0].HttpOnly {
		t.Fatal("会话 cookie 应 HttpOnly + SameSite=Strict")
	}

	// 本机 localtoken
	req = httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/x", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	req.Header.Set("X-Fanout-Local", a.localToken)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Body.String() != "ok" {
		t.Fatal("本机 localtoken 应放行")
	}
	req.RemoteAddr = "8.8.8.8:5555"
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Body.String() == "ok" {
		t.Fatal("外部来源带 localtoken 不应放行")
	}
}

func TestClientIPOnlyTrustsLoopbackForwarded(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "1.2.3.4:1"
	r.Header.Set("X-Real-IP", "9.9.9.9")
	if clientIP(r) != "1.2.3.4" {
		t.Fatal("外部请求不应采信 X-Real-IP")
	}
	r.RemoteAddr = "127.0.0.1:1"
	if clientIP(r) != "9.9.9.9" {
		t.Fatal("本机反代应采信 X-Real-IP")
	}
}

func TestCFOnceIsNotSaved(t *testing.T) {
	setupPanel(t)
	o := cfOnce{CFToken: strings.Repeat("a", 40)}
	var seen *cfCreds
	_ = withCFOnce(o, func() error { seen = loadCFCreds(); return nil })
	if seen == nil || seen.Token != o.CFToken {
		t.Fatal("一次性凭证期间应使用它")
	}
	if loadCFCreds() != nil {
		t.Fatal("一次性凭证用完不应留下")
	}
	if pathExists(panelPaths().CFTokenFile) {
		t.Fatal("一次性凭证不应落盘")
	}
}

func TestProxyConfIPAndSSL(t *testing.T) {
	setupPanel(t)
	conf, err := buildProxyConf(proxySpec{Domain: "1.2.3.4", ListenPort: 8080, Target: "127.0.0.1:3000"})
	if err != nil || !strings.Contains(conf, "server_name 1.2.3.4;") {
		t.Fatalf("IP 反代: %v\n%s", err, conf)
	}
}

func TestSameOriginIgnoresPortBehindNginx(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "http://panel.example.com/api/x", nil)
	r.Header.Set("Origin", "https://panel.example.com:8443")
	if !sameOrigin(r) {
		t.Fatal("反代改了端口时同主机名应放行")
	}
	r.Header.Set("Origin", "https://evil.example.com")
	if sameOrigin(r) {
		t.Fatal("别的主机名应拒绝")
	}
}
