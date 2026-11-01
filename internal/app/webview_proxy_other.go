//go:build !windows

package app

import "vinyl/internal/spotify"

func configureWebViewProxy(spotify.ProxyConfig) error { return nil }
