package base

import "testing"

func TestProxyEffectiveMode(t *testing.T) {
	cases := []struct {
		name string
		cfg  *DownloaderProxyConfig
		want string
	}{
		{"nil", nil, ProxyModeOff},
		{"legacy enabled", &DownloaderProxyConfig{Enable: true}, ProxyModeAll},
		{"legacy disabled", &DownloaderProxyConfig{}, ProxyModeOff},
		{"explicit off beats enable", &DownloaderProxyConfig{Enable: true, Mode: ProxyModeOff}, ProxyModeOff},
		{"explicit partial", &DownloaderProxyConfig{Mode: ProxyModePartial}, ProxyModePartial},
		{"explicit all", &DownloaderProxyConfig{Mode: ProxyModeAll}, ProxyModeAll},
		{"unknown falls back", &DownloaderProxyConfig{Enable: true, Mode: "whatever"}, ProxyModeAll},
	}
	for _, c := range cases {
		if got := c.cfg.EffectiveMode(); got != c.want {
			t.Errorf("%s: EffectiveMode() = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestProxyMatchURL(t *testing.T) {
	cfg := &DownloaderProxyConfig{
		Keywords: []string{"  GitHub.com ", "", "pixiv"},
	}
	cases := map[string]bool{
		"https://github.com/a/b.zip": true,
		"https://GITHUB.COM/a/b.zip": true,
		// 关键字是子串匹配："github.com" 不会命中 githubusercontent.com，
		// 想连 raw 一起代理就把关键字写成 github
		"https://objects.githubusercontent.com/x": false,
		"https://www.pixiv.net/artworks":          true,
		"https://example.com/a.zip":               false,
		"":                                        false,
	}
	for url, want := range cases {
		if got := cfg.MatchURL(url); got != want {
			t.Errorf("MatchURL(%q) = %v, want %v", url, got, want)
		}
	}
	if (&DownloaderProxyConfig{}).MatchURL("https://github.com") {
		t.Error("empty keyword list must not match")
	}
	if (*DownloaderProxyConfig)(nil).MatchURL("https://github.com") {
		t.Error("nil config must not match")
	}
}

func TestProxyResolve(t *testing.T) {
	base := func(mode string, kws ...string) *DownloaderProxyConfig {
		return &DownloaderProxyConfig{
			Mode:     mode,
			Keywords: kws,
			Scheme:   "http",
			Host:     "127.0.0.1:7890",
		}
	}
	// off: never proxies
	if u, err := base(ProxyModeOff).Resolve("https://github.com/x"); err != nil || u != nil {
		t.Errorf("off: got %v, %v; want nil, nil", u, err)
	}
	// all: always proxies
	u, err := base(ProxyModeAll).Resolve("https://example.com/x")
	if err != nil || u == nil || u.Host != "127.0.0.1:7890" {
		t.Errorf("all: got %v, %v", u, err)
	}
	// partial: only matching URLs
	cfg := base(ProxyModePartial, "github.com")
	if u, _ := cfg.Resolve("https://example.com/x"); u != nil {
		t.Errorf("partial non-match should be direct, got %v", u)
	}
	u, err = cfg.Resolve("https://github.com/x")
	if err != nil || u == nil || u.Host != "127.0.0.1:7890" {
		t.Errorf("partial match should proxy, got %v, %v", u, err)
	}
	// partial with no keywords proxies nothing
	if u, _ := base(ProxyModePartial).Resolve("https://github.com/x"); u != nil {
		t.Errorf("partial without keywords should be direct, got %v", u)
	}
	// enabled but no host: nothing to proxy with
	if u, _ := (&DownloaderProxyConfig{Mode: ProxyModeAll}).Resolve("https://x/y"); u != nil {
		t.Errorf("all without host should be nil, got %v", u)
	}
}
