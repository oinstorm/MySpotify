package spotify

import (
	"net/http"
	"testing"
)

func TestParseProxyAddress(t *testing.T) {
	for _, tc := range []struct {
		address string
		want    string
		valid   bool
	}{
		{address: "http://127.0.0.1:7890", want: "http://127.0.0.1:7890", valid: true},
		{address: " http://[::1]:8080 ", want: "http://[::1]:8080", valid: true},
		{address: "", valid: false},
		{address: "127.0.0.1:7890", valid: false},
		{address: "https://127.0.0.1:7890", valid: false},
		{address: "http://user:pass@127.0.0.1:7890", valid: false},
		{address: "http://127.0.0.1:7890/path", valid: false},
		{address: "http://127.0.0.1:0", valid: false},
		{address: "http://127.0.0.1:65536", valid: false},
	} {
		u, err := ParseProxyAddress(tc.address)
		if tc.valid {
			if err != nil || u.String() != tc.want {
				t.Errorf("ParseProxyAddress(%q) = %v, %v; want %q", tc.address, u, err, tc.want)
			}
		} else if err == nil {
			t.Errorf("ParseProxyAddress(%q) accepted %v", tc.address, u)
		}
	}
}

func TestProxyFuncModes(t *testing.T) {
	external, _ := http.NewRequest(http.MethodGet, "https://spotify.com", nil)
	local, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:1234", nil)
	for _, tc := range []struct {
		name string
		cfg  ProxyConfig
		want string
	}{
		{name: "direct", cfg: ProxyConfig{Mode: ProxyDirect}, want: ""},
		{name: "manual", cfg: ProxyConfig{Mode: ProxyManual, Address: "http://127.0.0.1:7890"}, want: "http://127.0.0.1:7890"},
		{name: "manual local bypass", cfg: ProxyConfig{Mode: ProxyManual, Address: "http://127.0.0.1:7890"}, want: ""},
	} {
		proxy, err := ProxyFunc(tc.cfg)
		if err != nil {
			t.Fatalf("ProxyFunc(%s): %v", tc.name, err)
		}
		req := external
		if tc.name == "manual local bypass" {
			req = local
		}
		u, err := proxy(req)
		if err != nil {
			t.Fatalf("proxy(%s): %v", tc.name, err)
		}
		got := ""
		if u != nil {
			got = u.String()
		}
		if got != tc.want {
			t.Errorf("proxy(%s) = %q, want %q", tc.name, got, tc.want)
		}
	}
}
