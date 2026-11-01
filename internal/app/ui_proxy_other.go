//go:build !windows

package app

import "github.com/egoist/mygo/ui"

func proxySettingsView(*app, *ui.Context, palette, func(string, string, func())) {}
