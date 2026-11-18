package main

import (
	"net/http"
	"testing"
)

// 整套路由注册一遍：任何两条路径重名，ServeMux 会 panic，服务就起不来。
func TestRoutesNoConflict(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("路由注册冲突: %v", r)
		}
	}()
	mux := http.NewServeMux()
	registerCoreRoutes(mux, NewManager(4, t.TempDir()))
	registerSettingsRoutes(mux, nil, nil)
}
