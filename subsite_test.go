package main

import (
	"strings"
	"testing"
)

func TestAddSubInclude(t *testing.T) {
	conf := "# x\nserver {\n    listen 80;\n    server_name a.com;\n}\nserver {\n    listen 443 ssl;\n    listen [::]:443 ssl;\n    server_name a.com;\n    location / {\n        proxy_pass http://127.0.0.1:9;\n    }\n}\n"
	out, ok := addSubInclude(conf, "a.com", "443", "/u/*.conf")
	if !ok || strings.Count(out, "include /u/*.conf;") != 1 {
		t.Fatalf("应只在 443 块补一行:\n%s", out)
	}
	if i, j := strings.Index(out, "listen 443"), strings.Index(out, "include"); j < i {
		t.Fatalf("include 进了错的块:\n%s", out)
	}
	if _, ok := addSubInclude(out, "a.com", "443", "/u/*.conf"); ok {
		t.Fatal("已经有了不该再加")
	}
	if _, ok := addSubInclude(conf, "b.com", "443", "/u/*.conf"); ok {
		t.Fatal("别的域名不该动")
	}
}
