package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// callAPI 把请求打给面板的 mux，返回状态码与解析后的 JSON。
func callAPI(t *testing.T, mux *http.ServeMux, method, path, body string) (int, map[string]any) {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func newAdminMux() *http.ServeMux {
	mux := http.NewServeMux()
	registerAdminAPI(mux)
	return mux
}

func TestNodesAPIListsAndCreates(t *testing.T) {
	lp, _ := setupPanel(t)
	writeTestNode(t, lp, "vless-reality-1.json", vlessRealitySample)
	mux := newAdminMux()

	code, out := callAPI(t, mux, http.MethodGet, "/api/nodes", "")
	if code != 200 || out["error"] != nil {
		t.Fatalf("列出节点失败: %d %+v", code, out)
	}
	nodes, _ := out["nodes"].([]any)
	if len(nodes) != 1 {
		t.Fatalf("应该有 1 个节点: %+v", out)
	}
	kinds, _ := out["kinds"].([]any)
	// 14 种普通入站 + 「CF 临时隧道」伪类型（脚本的第 15 项）
	if len(kinds) != len(nodeKinds)+1 {
		t.Errorf("类型列表数量不对: %d", len(kinds))
	}

	code, out = callAPI(t, mux, http.MethodPost, "/api/nodes/create", `{"kind":"vless-ws"}`)
	if code != 200 || out["error"] != nil {
		t.Fatalf("建节点失败: %d %+v", code, out)
	}
	if !pathExists(filepath.Join(lp.ConfDir, "vless-ws-1.json")) {
		t.Error("配置文件没生成")
	}

	// 非法 JSON
	code, _ = callAPI(t, mux, http.MethodPost, "/api/nodes/create", `{"kind":`)
	if code != http.StatusBadRequest {
		t.Errorf("坏 JSON 应该 400，实际 %d", code)
	}
	// GET 到 POST 接口
	code, _ = callAPI(t, mux, http.MethodPost, "/api/nodes", "")
	if code != http.StatusMethodNotAllowed {
		t.Errorf("POST /api/nodes 应该 405，实际 %d", code)
	}
	// 未知类型
	_, out = callAPI(t, mux, http.MethodPost, "/api/nodes/create", `{"kind":"nope"}`)
	if out["error"] == nil {
		t.Error("未知类型应该返回 error")
	}
}

func TestNodeDetailConfigAndLinks(t *testing.T) {
	lp, _ := setupPanel(t)
	writeTestNode(t, lp, "vless-reality-1.json", vlessRealitySample)
	_ = os.WriteFile(filepath.Join(lp.URLDir, "vless-reality-1.txt"),
		[]byte("vless://u@1.2.3.4:23456?security=reality#t\n"), 0644)
	mux := newAdminMux()
	_, out := callAPI(t, mux, http.MethodGet, "/api/nodes/detail?file=vless-reality-1.json", "")
	if out["error"] != nil {
		t.Fatalf("详情失败: %+v", out)
	}
	if cfg, _ := out["config"].(string); !strings.Contains(cfg, "vless-reality-1") {
		t.Errorf("配置内容不对: %s", cfg)
	}
	_, out = callAPI(t, mux, http.MethodGet, "/api/nodes/detail?file=../evil.json", "")
	if out["error"] == nil {
		t.Error("非法文件名应该报错")
	}
}

func TestNginxAPI(t *testing.T) {
	lp, _ := setupPanel(t)
	mux := newAdminMux()
	code, out := callAPI(t, mux, http.MethodGet, "/api/nginx/status", "")
	if code != 200 || out["error"] != nil {
		t.Fatalf("状态接口失败: %+v", out)
	}
	code, out = callAPI(t, mux, http.MethodPost, "/api/nginx/proxy",
		`{"domain":"bad domain","listen_port":80,"target":"127.0.0.1:80"}`)
	if code != http.StatusBadRequest {
		t.Errorf("非法域名应该 400，实际 %d %+v", code, out)
	}
	code, out = callAPI(t, mux, http.MethodPost, "/api/nginx/proxy",
		`{"domain":"app.example.com","listen_port":8080,"target":"127.0.0.1:9393","websocket":true}`)
	if code != 200 || out["error"] != nil {
		t.Fatalf("新增反代失败: %d %+v", code, out)
	}
	if !pathExists(filepath.Join(lp.NginxConfDir, "app-example-com.conf")) {
		t.Errorf("配置文件没生成: %+v", out)
	}
	_, out = callAPI(t, mux, http.MethodGet, "/api/nginx/sites", "")
	if sites, _ := out["sites"].([]any); len(sites) != 1 {
		t.Errorf("站点列表不对: %+v", out)
	}
	// 受保护文件不能删
	_ = os.WriteFile(filepath.Join(lp.NginxConfDir, "singbox_sub.conf"), []byte("x"), 0644)
	_, out = callAPI(t, mux, http.MethodPost, "/api/nginx/delete", `{"name":"singbox_sub.conf"}`)
	if out["error"] == nil {
		t.Error("脚本的配置不该被删")
	}
}

func TestSubsAPI(t *testing.T) {
	lp, _ := setupPanel(t)
	writeTestNode(t, lp, "vless-ws-1.json", vlessWSSample)
	_ = os.WriteFile(filepath.Join(lp.URLDir, "vless-ws-1.txt"),
		[]byte("vless://u@203.0.113.9:23457?security=tls&sni=node.example.com&type=ws&path=/abcd-ws#t\n"), 0644)
	mux := newAdminMux()
	code, out := callAPI(t, mux, http.MethodPost, "/api/subs/add",
		`{"name":"eve","files":["vless-ws-1.json"]}`)
	if code != 200 || out["error"] != nil {
		t.Fatalf("加用户失败: %d %+v", code, out)
	}
	_, out = callAPI(t, mux, http.MethodGet, "/api/subs", "")
	subs, _ := out["subs"].([]any)
	if len(subs) != 1 {
		t.Fatalf("用户列表不对: %+v", out)
	}
	// 同名再加一次要报错
	_, out = callAPI(t, mux, http.MethodPost, "/api/subs/add",
		`{"name":"eve","files":["vless-ws-1.json"]}`)
	if out["error"] == nil {
		t.Error("重复用户应该报错")
	}
	_, out = callAPI(t, mux, http.MethodPost, "/api/subs/delete", `{"name":"eve"}`)
	if out["error"] != nil {
		t.Errorf("删用户失败: %+v", out)
	}
}

func TestSystemAPIAndOpLog(t *testing.T) {
	lp, _ := setupPanel(t)
	writeTestNode(t, lp, "vless-reality-1.json", vlessRealitySample)
	mux := newAdminMux()
	code, out := callAPI(t, mux, http.MethodGet, "/api/system", "")
	if code != 200 || out["overview"] == nil {
		t.Fatalf("系统接口失败: %+v", out)
	}
	if services, _ := out["services"].([]any); len(services) != 3 {
		t.Errorf("服务列表不对: %+v", out)
	}
	// 只允许操作三个服务
	code, out = callAPI(t, mux, http.MethodPost, "/api/system/service", `{"unit":"sshd","action":"stop"}`)
	if code != http.StatusBadRequest {
		t.Errorf("不该允许操作别的服务: %d %+v", code, out)
	}
	// 触发一条操作日志再读回来
	_, _ = callAPI(t, mux, http.MethodPost, "/api/firewall", `{"action":"sync"}`)
	_, out = callAPI(t, mux, http.MethodGet, "/api/system/oplog", "")
	ops, _ := out["ops"].([]any)
	if len(ops) == 0 {
		t.Errorf("操作日志应该有内容: %+v", out)
	}
}

func TestCFAPINotConfigured(t *testing.T) {
	lp, _ := setupPanel(t)
	mux := newAdminMux()
	_, out := callAPI(t, mux, http.MethodGet, "/api/cf", "")
	if out["configured"] != false {
		t.Errorf("一开始不该有凭证: %+v", out)
	}
	// 保存一个明显无效的 Token：太短应该当场拒绝，文件不能留下
	_, out = callAPI(t, mux, http.MethodPost, "/api/cf/cred",
		`{"action":"save","kind":"token","token":"short"}`)
	if out["error"] == nil {
		t.Error("太短的 Token 应该报错")
	}
	if pathExists(lp.CFTokenFile) {
		t.Error("无效 Token 不该落盘")
	}
}

func TestCertAPI(t *testing.T) {
	lp, _ := setupPanel(t)
	dir := filepath.Join(lp.CertDirs[0], "node.example.com")
	_ = os.MkdirAll(dir, 0755)
	certPEM, keyPEM := makeTestCert(t, "node.example.com")
	_ = os.WriteFile(filepath.Join(dir, "fullchain.pem"), certPEM, 0644)
	_ = os.WriteFile(filepath.Join(dir, "privkey.pem"), keyPEM, 0600)
	mux := newAdminMux()
	_, out := callAPI(t, mux, http.MethodGet, "/api/certs", "")
	certs, _ := out["certs"].([]any)
	if len(certs) != 1 {
		t.Fatalf("证书列表不对: %+v", out)
	}
	// 删一个被 nginx 引用的证书应该被拒
	_ = os.WriteFile(filepath.Join(lp.NginxConfDir, "uses.conf"),
		[]byte("ssl_certificate "+filepath.Join(dir, "fullchain.pem")+";\n"), 0644)
	_, out = callAPI(t, mux, http.MethodPost, "/api/certs/delete", `{"domain":"node.example.com"}`)
	if out["error"] == nil {
		t.Error("在用着的证书不该能删")
	}
}
