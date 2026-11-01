//go:build windows

package app

import (
	"strings"
	"testing"

	"vinyl/internal/spotify"
)

func TestWebViewArgumentsForProxyModes(t *testing.T) {
	base := "--autoplay-policy=no-user-gesture-required --proxy-server=http://old:1 --proxy-pac-url=file:///old --proxy-bypass-list=old"
	cases := []struct {
		name string
		cfg  spotify.ProxyConfig
		want string
	}{
		{name: "system", cfg: spotify.ProxyConfig{Mode: spotify.ProxySystem}, want: "--autoplay-policy=no-user-gesture-required"},
		{name: "direct", cfg: spotify.ProxyConfig{Mode: spotify.ProxyDirect}, want: "--no-proxy-server"},
		{name: "manual", cfg: spotify.ProxyConfig{Mode: spotify.ProxyManual, Address: "http://127.0.0.1:7890"}, want: "--proxy-server=http://127.0.0.1:7890 --proxy-bypass-list=localhost;127.0.0.1;[::1]"},
	}
	for _, tc := range cases {
		got, err := webViewArgumentsFor(base, tc.cfg)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got != "--autoplay-policy=no-user-gesture-required "+tc.want && tc.name == "manual" {
			t.Errorf("%s: got %q", tc.name, got)
		}
		if tc.name != "manual" && !strings.Contains(got, tc.want) {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
		if strings.Contains(got, "old") {
			t.Errorf("%s kept an old proxy argument: %q", tc.name, got)
		}
	}
}
