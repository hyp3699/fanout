package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 页面是四份文件拼起来的：占位符必须全部替换掉，标签页和关键容器都要在。
func TestIndexHTMLAssembled(t *testing.T) {
	if strings.Contains(indexHTML, "{{") {
		t.Fatal("indexHTML 里还有没替换掉的占位符")
	}
	for _, want := range []string{
		`data-tab="nodes"`, `data-tab="subs"`, `data-tab="infra"`, `data-tab="system"`,
		`id="nodes"`, `id="subs"`, `id="ngxsites"`, `id="certs"`, `id="sysinfo"`, `id="oplog"`,
		`id="nodecreate"`, `id="nodesheet"`, `id="subadd"`, `id="ngxsite"`, `id="certnew"`,
		`id="cfcred"`, `id="logbox"`, `id="users"`, `id="rules"`, `id="list"`,
		`{{`, // 只要出现一次就说明替换没干净，由上面的检查负责报错；这里避免编译器抱怨
	} {
		if want == "{{" {
			continue
		}
		if !strings.Contains(indexHTML, want) {
			t.Errorf("页面里缺 %s", want)
		}
	}
	// 原有脚本与新脚本都在
	if !strings.Contains(indexHTML, "function renderExits") {
		t.Error("原出口脚本没嵌进去")
	}
	if !strings.Contains(indexHTML, "function renderNodes") {
		t.Error("新节点脚本没嵌进去")
	}
	// 样式也在
	if !strings.Contains(indexHTML, ".tabs{") {
		t.Error("新增样式没嵌进去")
	}
}

func TestIndexHandler(t *testing.T) {
	rec := httptest.NewRecorder()
	handleIndex(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != 200 {
		t.Fatalf("首页状态码 %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type 不对: %s", ct)
	}
	if !strings.Contains(rec.Body.String(), "<title>fanout</title>") {
		t.Error("页面内容不对")
	}
	// 非根路径 404
	rec = httptest.NewRecorder()
	handleIndex(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("非根路径应该 404，实际 %d", rec.Code)
	}
}
