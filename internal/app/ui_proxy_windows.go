//go:build windows

package app

import (
	"strings"

	"github.com/egoist/mygo/ui"

	"vinyl/internal/spotify"
)

func proxySettingsView(a *app, c *ui.Context, p palette, row func(string, string, func())) {
	s := &a.settings
	if !a.proxyDraftReady {
		a.proxyDraftReady = true
		a.proxyDraftMode = s.ProxyMode
		if a.proxyDraftMode == "" {
			a.proxyDraftMode = string(spotify.ProxySystem)
		}
		a.proxyDraftAddress = s.ProxyAddress
	}
	group := func(title string, rows func()) {
		ui.Text(c, title).FontSize(12).FontWeight(600).TextColor(p.muted).Margin(14, 4, 4, 4)
		ui.Column(c).Radius(12).Background(p.bg).Border(1, p.hairline).Dividers(1, p.hairline).Children(rows)
	}
	group("网络", func() {
		row("代理模式", "只影响 MySpotify；保存后退出程序并重新打开才生效", func() {
			labels := []string{"跟随系统", "直连", "手动 HTTP"}
			modes := []string{string(spotify.ProxySystem), string(spotify.ProxyDirect), string(spotify.ProxyManual)}
			i := 0
			for n, mode := range modes {
				if mode == a.proxyDraftMode {
					i = n
				}
			}
			if segmented(c.Key("proxy-mode"), &i, labels...) {
				a.proxyDraftMode = modes[i]
				a.proxyMessage = ""
			}
		})
		if a.proxyDraftMode == string(spotify.ProxyManual) {
			row("HTTP 代理地址", "例如 http://127.0.0.1:7890；不支持 SOCKS5 或账号密码", func() {
				ui.TextInput(c.Key("proxy-address"), &a.proxyDraftAddress).Placeholder("http://127.0.0.1:7890").Label("HTTP 代理地址").Width(190)
			})
		}
		row("保存代理设置", "当前运行不会切换网络，重启后新设置才会应用", func() {
			if ui.Button(c.Key("save-proxy"), "保存").Clicked() {
				config := spotify.ProxyConfig{Mode: spotify.ProxyMode(a.proxyDraftMode), Address: strings.TrimSpace(a.proxyDraftAddress)}
				if _, err := spotify.ProxyFunc(config); err != nil {
					a.proxyMessage = err.Error()
					return
				}
				s.ProxyMode, s.ProxyAddress = string(config.Mode), config.Address
				if err := s.save(); err != nil {
					a.proxyMessage = "保存失败：" + err.Error()
					return
				}
				a.proxyMessage = "已保存，请退出 MySpotify 后重新打开"
			}
		})
		if a.proxyMessage != "" {
			ui.Text(c, a.proxyMessage).FontSize(12).TextColor(p.muted).Padding(0, 16, 12, 16)
		}
	})
}
