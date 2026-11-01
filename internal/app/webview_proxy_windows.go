//go:build windows

package app

import (
	"fmt"
	"os"
	"strings"

	"vinyl/internal/spotify"
)

const webViewArguments = "WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS"

// configureWebViewProxy sets the proxy before MyGo creates the first WebView2
// environment. The saved setting applies to all WebView2 windows in this run.
func configureWebViewProxy(config spotify.ProxyConfig) error {
	args, err := webViewArgumentsFor(os.Getenv(webViewArguments), config)
	if err != nil {
		return err
	}
	return os.Setenv(webViewArguments, args)
}

func webViewArgumentsFor(existing string, config spotify.ProxyConfig) (string, error) {
	args := strings.Fields(existing)
	filtered := args[:0]
	for _, arg := range args {
		if arg == "--no-proxy-server" || strings.HasPrefix(arg, "--proxy-server=") || strings.HasPrefix(arg, "--proxy-pac-url=") || strings.HasPrefix(arg, "--proxy-bypass-list=") {
			continue
		}
		filtered = append(filtered, arg)
	}
	switch config.Mode {
	case "", spotify.ProxySystem:
	case spotify.ProxyDirect:
		filtered = append(filtered, "--no-proxy-server")
	case spotify.ProxyManual:
		u, err := spotify.ParseProxyAddress(config.Address)
		if err != nil {
			return "", err
		}
		filtered = append(filtered, "--proxy-server="+u.String(), "--proxy-bypass-list=localhost;127.0.0.1;[::1]")
	default:
		return "", fmt.Errorf("unsupported proxy mode %q", config.Mode)
	}
	return strings.Join(filtered, " "), nil
}
