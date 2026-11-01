package spotify

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type ProxyMode string

const (
	ProxySystem ProxyMode = "system"
	ProxyDirect ProxyMode = "direct"
	ProxyManual ProxyMode = "manual"
)

type ProxyConfig struct {
	Mode    ProxyMode
	Address string
}

// SystemProxy is the system proxy for clients outside this package.
func SystemProxy(req *http.Request) (*url.URL, error) { return systemProxy(req) }

// ProxyFunc validates a configuration and returns the proxy decision used by
// all of the app's outbound HTTP and WebSocket requests.
func ProxyFunc(config ProxyConfig) (func(*http.Request) (*url.URL, error), error) {
	switch config.Mode {
	case "", ProxySystem:
		return systemProxy, nil
	case ProxyDirect:
		return directProxy, nil
	case ProxyManual:
		u, err := ParseProxyAddress(config.Address)
		if err != nil {
			return nil, err
		}
		return func(req *http.Request) (*url.URL, error) {
			if isLoopback(req.URL.Hostname()) {
				return nil, nil
			}
			return u, nil
		}, nil
	default:
		return nil, fmt.Errorf("spotify: unsupported proxy mode %q", config.Mode)
	}
}

// ParseProxyAddress accepts the unauthenticated HTTP proxy form used by
// desktop proxy clients, such as http://127.0.0.1:7890.
func ParseProxyAddress(address string) (*url.URL, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return nil, errors.New("代理地址不能为空")
	}
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("代理地址必须是 http://主机:端口")
	}
	if u.Hostname() == "" {
		return nil, errors.New("代理地址缺少主机")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return nil, errors.New("代理地址端口必须是 1 到 65535")
	}
	if net.ParseIP(u.Hostname()) == nil && strings.ContainsAny(u.Hostname(), " /\\") {
		return nil, errors.New("代理地址主机无效")
	}
	u.Path = ""
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u, nil
}

func directProxy(*http.Request) (*url.URL, error) { return nil, nil }

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ConfigureProxy applies one proxy policy to the Spotify client and the
// default client used by the updater.
func ConfigureProxy(config ProxyConfig) error {
	proxy, err := ProxyFunc(config)
	if err != nil {
		return err
	}
	if t, ok := Client.Transport.(*http.Transport); ok {
		clone := t.Clone()
		clone.Proxy = proxy
		Client.Transport = clone
	}
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		clone := t.Clone()
		clone.Proxy = proxy
		http.DefaultTransport = clone
	}
	return nil
}
