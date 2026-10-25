package main

import "testing"

func TestParseAgentTokens(t *testing.T) {
	got := parseAgentTokens("100=abc, 101 = def ,bad,=x,102=")
	if len(got) != 2 || got["100"] != "abc" || got["101"] != "def" {
		t.Fatalf("unexpected tokens: %#v", got)
	}
}
