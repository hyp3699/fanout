package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// cfMock 是一个最小的 Cloudflare API 假服务。
type cfMock struct {
	mu       sync.Mutex
	zones    []map[string]any
	dns      []map[string]any
	origin   []any
	tunnels  []map[string]any
	ingress  []any
	sslMode  string
	lastAuth string
	failNext bool
	requests []string
}

func newCFMock() *cfMock {
	return &cfMock{
		zones: []map[string]any{
			{"id": "zone-example", "name": "example.com", "status": "active", "account": map[string]any{"id": "acc-1"}},
			{"id": "zone-example-org", "name": "example.org", "status": "active", "account": map[string]any{"id": "acc-1"}},
		},
	}
}

func (m *cfMock) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
	m.lastAuth = r.Header.Get("Authorization") + r.Header.Get("X-Auth-Email")
	body, _ := io.ReadAll(r.Body)
	ok := func(result any) {
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": result})
	}
	fail := func(msg string) {
		_ = json.NewEncoder(w).Encode(map[string]any{"success": false,
			"errors": []any{map[string]any{"message": msg}}})
	}
	path := r.URL.Path
	switch {
	case path == "/zones":
		ok(m.zones)
	case strings.HasSuffix(path, "/rulesets/phases/http_request_origin/entrypoint"):
		if r.Method == http.MethodGet {
			if m.origin == nil {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"success":false,"errors":[{"message":"not found"}]}`))
				return
			}
			ok(map[string]any{"rules": m.origin})
			return
		}
		var payload struct {
			Rules []any `json:"rules"`
		}
		_ = json.Unmarshal(body, &payload)
		m.origin = payload.Rules
		ok(map[string]any{"rules": m.origin})
	case strings.HasSuffix(path, "/settings/ssl"):
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		m.sslMode = str(payload["value"])
		ok(map[string]any{"value": m.sslMode})
	case strings.Contains(path, "/dns_records"):
		m.dnsHandler(w, r, body, ok, fail)
	case path == "/accounts/acc-1/cfd_tunnel":
		if r.Method == http.MethodGet {
			ok(m.tunnels)
			return
		}
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		t := map[string]any{"id": "tunnel-1", "name": str(payload["name"]), "status": "inactive"}
		m.tunnels = append(m.tunnels, t)
		ok(t)
	case strings.HasSuffix(path, "/token"):
		ok("TUNNEL-TOKEN")
	case strings.HasSuffix(path, "/configurations"):
		if r.Method == http.MethodGet {
			ok(map[string]any{"config": map[string]any{"ingress": m.ingress}})
			return
		}
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		cfg, _ := payload["config"].(map[string]any)
		m.ingress, _ = cfg["ingress"].([]any)
		ok(payload)
	case path == "/certificates":
		if m.failNext {
			fail("no permission")
			return
		}
		ok(map[string]any{"certificate": "-----BEGIN CERTIFICATE-----\nX\n-----END CERTIFICATE-----\n",
			"private_key": "-----BEGIN PRIVATE KEY-----\nY\n-----END PRIVATE KEY-----\n"})
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"message":"unknown endpoint"}]}`))
	}
}

func (m *cfMock) dnsHandler(w http.ResponseWriter, r *http.Request, body []byte, ok func(any), fail func(string)) {
	name := r.URL.Query().Get("name")
	switch r.Method {
	case http.MethodGet:
		var out []any
		for _, rec := range m.dns {
			if name == "" || str(rec["name"]) == name {
				out = append(out, rec)
			}
		}
		ok(out)
	case http.MethodPost, http.MethodPut:
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		payload["id"] = str(payload["name"]) + "-rec"
		// PUT 到 /dns_records/<id>
		for i, rec := range m.dns {
			if str(rec["name"]) == str(payload["name"]) {
				m.dns[i] = payload
				ok(payload)
				return
			}
		}
		if strings.HasSuffix(r.URL.Path, "/dns_records") == false {
			ok(payload)
			return
		}
		m.dns = append(m.dns, payload)
		ok(payload)
	case http.MethodDelete:
		id := filepath.Base(r.URL.Path)
		for i, rec := range m.dns {
			if str(rec["id"]) == id {
				m.dns = append(m.dns[:i], m.dns[i+1:]...)
				break
			}
		}
		ok(map[string]any{"id": id})
	default:
		fail("method not allowed")
	}
}

func setupCF(t *testing.T) (*cfMock, *httptest.Server) {
	t.Helper()
	_, _ = setupPanel(t)
	mock := newCFMock()
	srv := httptest.NewServer(mock)
	cfBaseURL = srv.URL
	t.Cleanup(srv.Close)
	return mock, srv
}

func TestCFCredsSaveLoadMask(t *testing.T) {
	lp, _ := setupPanel(t)
	if c := loadCFCreds(); c != nil {
		t.Fatal("一开始不该有凭证")
	}
	if err := saveCFCreds(&cfCreds{Kind: "token", Token: "abcdefghijklmnopqrstuvwxyz123456"}); err != nil {
		t.Fatal(err)
	}
	c := loadCFCreds()
	if c == nil || c.Kind != "token" {
		t.Fatalf("没读回 Token: %+v", c)
	}
	st, _ := os.Stat(lp.CFTokenFile)
	if st.Mode().Perm() != 0600 {
		t.Errorf("Token 文件权限应该是 0600，实际 %v", st.Mode().Perm())
	}
	if strings.Contains(c.masked(), c.Token) {
		t.Error("脱敏后不该出现完整 Token")
	}
	// 换成 Global Key 应该把 Token 文件清掉
	if err := saveCFCreds(&cfCreds{Kind: "global", Email: "me@example.com", Key: "0123456789abcdef0123456789abcdef"}); err != nil {
		t.Fatal(err)
	}
	if pathExists(lp.CFTokenFile) {
		t.Error("存 Global Key 后 Token 文件应该删掉")
	}
	c = loadCFCreds()
	if c.Kind != "global" || c.Email != "me@example.com" {
		t.Fatalf("Global Key 没读回来: %+v", c)
	}
	st, _ = os.Stat(lp.CFGlobalFile)
	if st.Mode().Perm() != 0600 {
		t.Errorf("Global Key 文件权限应该是 0600")
	}
	clearCFCreds()
	if loadCFCreds() != nil {
		t.Error("清除后不该还有凭证")
	}
}

func TestCFZonesAndFind(t *testing.T) {
	_, _ = setupCF(t)
	if err := saveCFCreds(&cfCreds{Kind: "token", Token: "abcdefghijklmnopqrstuvwxyz123456"}); err != nil {
		t.Fatal(err)
	}
	zones, err := cfZones()
	if err != nil || len(zones) != 2 {
		t.Fatalf("域名列表不对: %v %+v", err, zones)
	}
	zone, err := cfFindZone("node.example.com")
	if err != nil || zone.ID != "zone-example" {
		t.Fatalf("Zone 匹配不对: %v %+v", err, zone)
	}
	if _, err := cfFindZone("other.net"); err == nil {
		t.Error("不存在的域名应该报错")
	}
	if err := cfVerify(); err != nil {
		t.Errorf("凭证应该有效: %v", err)
	}
}

func TestCFDNSLifecycle(t *testing.T) {
	mock, _ := setupCF(t)
	if err := saveCFCreds(&cfCreds{Kind: "token", Token: "abcdefghijklmnopqrstuvwxyz123456"}); err != nil {
		t.Fatal(err)
	}
	if err := cfUpsertDNS("zone-example", "node.example.com", "203.0.113.9", true); err != nil {
		t.Fatalf("加 DNS 失败: %v", err)
	}
	if len(mock.dns) != 1 || str(mock.dns[0]["type"]) != "A" {
		t.Fatalf("DNS 记录不对: %+v", mock.dns)
	}
	// 再调一次应该是 PUT 覆盖，而不是再加一条
	if err := cfUpsertDNS("zone-example", "node.example.com", "203.0.113.10", true); err != nil {
		t.Fatalf("改 DNS 失败: %v", err)
	}
	if len(mock.dns) != 1 || str(mock.dns[0]["content"]) != "203.0.113.10" {
		t.Fatalf("应该覆盖而不是新增: %+v", mock.dns)
	}
	// 同名 MX 不该被删
	mock.dns = append(mock.dns, map[string]any{"id": "mx-1", "type": "MX", "name": "node.example.com", "content": "mail"})
	if err := cfDeleteDNS("zone-example", "node.example.com"); err != nil {
		t.Fatal(err)
	}
	if len(mock.dns) != 1 || str(mock.dns[0]["type"]) != "MX" {
		t.Fatalf("只该删 A/AAAA/CNAME: %+v", mock.dns)
	}
	// IPv6 用 AAAA
	if err := cfUpsertDNS("zone-example", "v6.example.com", "2001:db8::1", true); err != nil {
		t.Fatal(err)
	}
	last := mock.dns[len(mock.dns)-1]
	if str(last["type"]) != "AAAA" {
		t.Errorf("IPv6 应该写 AAAA: %+v", last)
	}
}

func TestCFOriginRules(t *testing.T) {
	mock, _ := setupCF(t)
	if err := saveCFCreds(&cfCreds{Kind: "token", Token: "abcdefghijklmnopqrstuvwxyz123456"}); err != nil {
		t.Fatal(err)
	}
	rules, err := cfGetOriginRules("zone-example")
	if err != nil || len(rules) > 0 {
		t.Fatalf("一开始没有回源规则，应返回空而不是报错: %v %+v", err, rules)
	}
	// 手工加一条别人的规则，确认不会被我们覆盖
	mock.origin = []any{map[string]any{"description": "手写的", "expression": `(http.host eq "keep.example.com")`}}
	if err := cfSetOriginPort("zone-example", "node.example.com", 2053); err != nil {
		t.Fatalf("设置回源端口失败: %v", err)
	}
	if len(mock.origin) != 2 {
		t.Fatalf("应该保留别人的规则并加一条新的: %+v", mock.origin)
	}
	// 同一个域名再来一次应该替换
	if err := cfSetOriginPort("zone-example", "node.example.com", 8443); err != nil {
		t.Fatal(err)
	}
	count := 0
	var port float64
	for _, r := range mock.origin {
		o := obj(r)
		if strings.HasSuffix(str(o["description"]), "_node.example.com") {
			count++
			params := obj(o["action_parameters"])
			port = obj(params["origin"])["port"].(float64)
		}
	}
	if count != 1 || int(port) != 8443 {
		t.Fatalf("同域名规则应该被替换: count=%d port=%v", count, port)
	}
	if err := cfRemoveOriginPort("zone-example", "node.example.com"); err != nil {
		t.Fatal(err)
	}
	if len(mock.origin) != 1 {
		t.Fatalf("删掉自己的规则后应该只剩下别人的: %+v", mock.origin)
	}
}

func TestCFDeleteCDN(t *testing.T) {
	mock, _ := setupCF(t)
	if err := saveCFCreds(&cfCreds{Kind: "token", Token: "abcdefghijklmnopqrstuvwxyz123456"}); err != nil {
		t.Fatal(err)
	}
	mock.origin = []any{map[string]any{
		"description": "Auto_Script:VLESS_WSTLS_CDN_cdn.example.com",
		"expression":  `(http.host eq "cdn.example.com")`,
	}}
	mock.dns = []map[string]any{{"id": "cdn-rec", "type": "A", "name": "cdn.example.com", "content": "203.0.113.9"}}
	if err := cfDeleteCDN("cdn.example.com"); err != nil {
		t.Fatalf("删 CDN 失败: %v", err)
	}
	if len(mock.dns) != 0 {
		t.Errorf("DNS 应该删掉: %+v", mock.dns)
	}
	if len(mock.origin) != 0 {
		t.Errorf("回源规则应该删掉: %+v", mock.origin)
	}
}

func TestCFSetupTunnel(t *testing.T) {
	mock, _ := setupCF(t)
	lp, _ := setupPanel(t)
	if err := saveCFCreds(&cfCreds{Kind: "token", Token: "abcdefghijklmnopqrstuvwxyz123456"}); err != nil {
		t.Fatal(err)
	}
	tun, host, err := cfSetupTunnel([]tunnelRoute{{Path: "/ws", Port: 23456}}, "example.com", "node")
	if err != nil {
		t.Fatalf("配隧道失败: %v", err)
	}
	if host != "node.example.com" {
		t.Fatalf("隧道域名不对: %s", host)
	}
	if tun.ID != "tunnel-1" {
		t.Errorf("应该用新建的 Tunnel: %+v", tun)
	}
	// cloudflared 入站写进去了
	blob, err := os.ReadFile(filepath.Join(lp.ConfDir, "cloudflared.json"))
	if err != nil {
		t.Fatalf("cloudflared.json 没写: %v", err)
	}
	var doc map[string]any
	_ = json.Unmarshal(blob, &doc)
	ib := firstInbound(doc)
	if str(ib["type"]) != "cloudflared" || str(ib["token"]) != "TUNNEL-TOKEN" {
		t.Fatalf("cloudflared 入站不对: %s", blob)
	}
	// ingress 里最后一条是 404
	if len(mock.ingress) != 2 {
		t.Fatalf("ingress 条数不对: %+v", mock.ingress)
	}
	last := obj(mock.ingress[len(mock.ingress)-1])
	if str(last["service"]) != "http_status:404" {
		t.Errorf("最后一条应该是 404 兜底: %+v", last)
	}
	first := obj(mock.ingress[0])
	if str(first["service"]) != "http://127.0.0.1:23456" || str(first["path"]) != "/ws" {
		t.Errorf("路由不对: %+v", first)
	}
	// CNAME 指向 tunnel
	found := false
	for _, rec := range mock.dns {
		if str(rec["type"]) == "CNAME" && str(rec["content"]) == "tunnel-1.cfargotunnel.com" {
			found = true
		}
	}
	if !found {
		t.Errorf("没建 Tunnel CNAME: %+v", mock.dns)
	}
	// 重复路由要拒绝
	if _, _, err := cfSetupTunnel([]tunnelRoute{{Path: "/ws", Port: 23456}}, "example.com", "node"); err == nil {
		t.Error("重复路由应该报错")
	}
}

func TestCFOriginCA(t *testing.T) {
	_, _ = setupCF(t)
	if err := saveCFCreds(&cfCreds{Kind: "token", Token: "abcdefghijklmnopqrstuvwxyz123456"}); err != nil {
		t.Fatal(err)
	}
	cert, key, err := cfOriginCA("origin.example.com")
	if err != nil {
		t.Fatalf("申请 Origin CA 失败: %v", err)
	}
	if !strings.Contains(cert, "BEGIN CERTIFICATE") || !strings.Contains(key, "BEGIN PRIVATE KEY") {
		t.Error("返回的证书不对")
	}
	if _, _, err := cfOriginCA("bad domain"); err == nil {
		t.Error("非法域名应该报错")
	}
}

func TestCFErrorMessage(t *testing.T) {
	_, _ = setupCF(t)
	if err := saveCFCreds(&cfCreds{Kind: "token", Token: "abcdefghijklmnopqrstuvwxyz123456"}); err != nil {
		t.Fatal(err)
	}
	// 未知接口返回 success:false，错误信息要带出来
	_, err := cfCall("GET", "/nope", nil)
	if err == nil || !strings.Contains(err.Error(), "unknown endpoint") {
		t.Fatalf("Cloudflare 的错误信息没带出来: %v", err)
	}
}

func TestCFRequiresCreds(t *testing.T) {
	_, _ = setupPanel(t)
	if _, err := cfZones(); err == nil {
		t.Fatal("没凭证时应该报错")
	}
	if _, err := cfCall("GET", "/zones", nil); err == nil {
		t.Fatal("没凭证时 cfCall 应该报错")
	}
}
