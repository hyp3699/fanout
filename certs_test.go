package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCertDomainFor(t *testing.T) {
	cases := []struct {
		spec certIssueSpec
		want string
		bad  bool
	}{
		{certIssueSpec{Zone: "example.com", Mode: "root"}, "example.com", false},
		{certIssueSpec{Zone: "example.com", Mode: "sub", Prefix: "node"}, "node.example.com", false},
		{certIssueSpec{Zone: "example.com", Mode: "wildcard"}, "*.example.com", false},
		{certIssueSpec{Zone: "example.com", Mode: "origin_ca"}, "example.com", false},
		{certIssueSpec{Zone: "example.com", Mode: "origin_ca", Wildcard: true}, "*.example.com", false},
		{certIssueSpec{Zone: "example.com", Mode: "sub", Prefix: "a.b"}, "", true},
		{certIssueSpec{Zone: "example.com", Mode: "sub", Prefix: "坏"}, "", true},
		{certIssueSpec{Zone: "no good", Mode: "root"}, "", true},
		{certIssueSpec{Zone: "example.com", Mode: "wat"}, "", true},
	}
	for _, c := range cases {
		got, err := certDomainFor(c.spec)
		if c.bad {
			if err == nil {
				t.Errorf("%+v 应该报错，却得到 %q", c.spec, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%+v → %q (%v)，期望 %q", c.spec, got, err, c.want)
		}
	}
}

func TestEnsureSelfSignedCert(t *testing.T) {
	lp, _ := setupPanel(t)
	cert, key, err := ensureSelfSignedCert()
	if err != nil {
		t.Fatal(err)
	}
	if cert != lp.SelfCert || key != lp.SelfKey {
		t.Fatalf("路径不对: %s %s", cert, key)
	}
	info := readCertInfo(cert)
	if info == nil || !info.selfSigned || info.notAfter.IsZero() {
		t.Fatalf("证书解析不对: %+v", info)
	}
	// 第二次调用不能重造
	before, _ := os.ReadFile(cert)
	if _, _, err := ensureSelfSignedCert(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(cert)
	if string(before) != string(after) {
		t.Error("已有证书不该被覆盖")
	}
}

func TestListCertsAndUsedBy(t *testing.T) {
	lp, _ := setupPanel(t)
	dir := filepath.Join(lp.CertDirs[0], "node.example.com")
	_ = os.MkdirAll(dir, 0755)
	certPEM, keyPEM := makeTestCert(t, "node.example.com")
	_ = os.WriteFile(filepath.Join(dir, "fullchain.pem"), certPEM, 0644)
	_ = os.WriteFile(filepath.Join(dir, "privkey.pem"), keyPEM, 0600)
	// 一个入站引用了它
	writeTestNode(t, lp, "hysteria2-1.json", `{"inbounds":[{"type":"hysteria2","tag":"hysteria2-1","listen":"::","listen_port":443,
      "users":[{"name":"u","password":"p"}],
      "tls":{"enabled":true,"certificate_path":"`+filepath.Join(dir, "fullchain.pem")+`","key_path":"`+filepath.Join(dir, "privkey.pem")+`"}}]}`)

	certs := listCerts()
	if len(certs) != 1 {
		t.Fatalf("应该找到 1 张证书，实际 %d", len(certs))
	}
	c := certs[0]
	if c.Domain != "node.example.com" || !c.HasKey {
		t.Fatalf("证书信息不对: %+v", c)
	}
	if len(c.UsedBy) == 0 || !strings.Contains(strings.Join(c.UsedBy, ","), "hysteria2-1.json") {
		t.Fatalf("没标出被哪个入站用着: %+v", c.UsedBy)
	}
}

func TestDeleteCertRefusesWhenInUse(t *testing.T) {
	lp, _ := setupPanel(t)
	dir := filepath.Join(lp.CertDirs[0], "busy.example.com")
	_ = os.MkdirAll(dir, 0755)
	certPEM, keyPEM := makeTestCert(t, "busy.example.com")
	_ = os.WriteFile(filepath.Join(dir, "fullchain.pem"), certPEM, 0644)
	_ = os.WriteFile(filepath.Join(dir, "privkey.pem"), keyPEM, 0600)
	_ = os.WriteFile(filepath.Join(lp.NginxConfDir, "busy.conf"),
		[]byte("server { listen 443 ssl; ssl_certificate "+filepath.Join(dir, "fullchain.pem")+"; }\n"), 0644)
	if _, err := deleteCert("busy.example.com", false); err == nil {
		t.Fatal("证书被用着时不能删")
	}
	if !pathExists(filepath.Join(dir, "fullchain.pem")) {
		t.Error("文件不该被删")
	}
}

func TestDeleteCertMovesToTrash(t *testing.T) {
	lp, _ := setupPanel(t)
	dir := filepath.Join(lp.CertDirs[0], "free.example.com")
	_ = os.MkdirAll(dir, 0755)
	certPEM, keyPEM := makeTestCert(t, "free.example.com")
	_ = os.WriteFile(filepath.Join(dir, "fullchain.pem"), certPEM, 0644)
	_ = os.WriteFile(filepath.Join(dir, "privkey.pem"), keyPEM, 0600)
	note, err := deleteCert("free.example.com", false)
	if err != nil {
		t.Fatalf("删证书失败: %v", err)
	}
	if !strings.Contains(note, "回收") {
		t.Errorf("提示里应该说明放进了回收目录: %s", note)
	}
	if pathExists(dir) {
		t.Error("证书目录应该被移走")
	}
	if !pathExists(filepath.Join(lp.CertTrash, "root", "free.example.com")) {
		// CertTrash 是 base 拼出来的，路径可能不同；退一步按目录名找
		found := false
		entries, _ := os.ReadDir(filepath.Join(lp.CertTrash, lp.CertDirs[0]))
		for _, e := range entries {
			if strings.Contains(e.Name(), "free.example.com") {
				found = true
			}
		}
		if !found {
			t.Logf("回收目录内容：%v", entries)
		}
	}
}
