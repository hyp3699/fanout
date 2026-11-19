package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPSOnly(t *testing.T) {
	h := httpsOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	cases := []struct {
		hdr  map[string]string
		want int
	}{
		{nil, 403},
		{map[string]string{"X-Forwarded-Proto": "http"}, 403},
		{map[string]string{"X-Forwarded-Proto": "https"}, 204},
		{map[string]string{"X-Fanout-Local": "x"}, 204},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = "127.0.0.1:5555"
		for k, v := range c.hdr {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != c.want {
			t.Fatalf("%v: got %d want %d", c.hdr, w.Code, c.want)
		}
	}
	if got := (WebSettings{Port: 9}).listenAddrString(); got != "127.0.0.1:9" {
		t.Fatal(got)
	}
}
