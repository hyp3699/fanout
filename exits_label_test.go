package main

import "testing"

func TestRuleExitLabel(t *testing.T) {
	e := Exit{Label: "🇰🇷 韩国 211.231.7.182", Country: "🇰🇷 韩国", ExitIP: "211.199.236.171"}
	if got := ruleExitLabel(e); got != "🇰🇷 韩国 211.199.236.171" {
		t.Fatalf("got %q", got)
	}
	e.ExitIP = ""
	if got := ruleExitLabel(e); got != e.Label {
		t.Fatalf("got %q", got)
	}
}
