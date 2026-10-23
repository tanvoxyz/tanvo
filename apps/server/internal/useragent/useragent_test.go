package useragent

import "testing"

func TestParse(t *testing.T) {
	t.Parallel()
	tests := []struct {
		ua   string
		want Info
	}{
		{"", Info{}},
		{
			"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
			Info{Browser: "Chrome 120.0.0.0", OS: "Mac", DeviceType: "desktop"},
		},
		{
			"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36 Edg/120.0.2210.91",
			Info{Browser: "Microsoft Edge 120.0.2210.91", OS: "Windows", DeviceType: "desktop"},
		},
		{
			"Mozilla/5.0 (X11; Ubuntu; Linux x86_64; rv:121.0) Gecko/20100101 Firefox/121.0",
			Info{Browser: "Firefox 121.0", OS: "Ubuntu", DeviceType: "desktop"},
		},
		{
			"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/119.0.0.0 Safari/537.36",
			Info{Browser: "Chrome 119.0.0.0", OS: "GNU/Linux", DeviceType: "desktop"},
		},
		{
			"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.2 Safari/605.1.15",
			Info{Browser: "Safari 17.2", OS: "Mac", DeviceType: "desktop"},
		},
		{
			"Mozilla/5.0 (iPhone; CPU iPhone OS 17_2 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.2 Mobile/15E148 Safari/604.1",
			Info{Browser: "Mobile Safari 17.2", OS: "iOS", DeviceType: "smartphone"},
		},
		{
			"Mozilla/5.0 (iPad; CPU OS 17_2 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/120.0.6099.119 Mobile/15E148 Safari/604.1",
			Info{Browser: "Chrome Mobile iOS 120.0.6099.119", OS: "iPadOS", DeviceType: "tablet"},
		},
		{
			"Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.6099.144 Mobile Safari/537.36",
			Info{Browser: "Chrome Mobile 120.0.6099.144", OS: "Android", DeviceType: "smartphone"},
		},
		{
			"Mozilla/5.0 (Linux; Android 13; SM-X700) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
			Info{Browser: "Chrome Mobile 120.0.0.0", OS: "Android", DeviceType: "tablet"},
		},
		{
			"Mozilla/5.0 (Linux; Android 14; SAMSUNG SM-S918B) AppleWebKit/537.36 (KHTML, like Gecko) SamsungBrowser/23.0 Chrome/115.0.0.0 Mobile Safari/537.36",
			Info{Browser: "Samsung Browser 23.0", OS: "Android", DeviceType: "smartphone"},
		},
		{
			"Mozilla/5.0 (Windows NT 6.1; Trident/7.0; rv:11.0) like Gecko",
			Info{Browser: "Internet Explorer 11.0", OS: "Windows", DeviceType: "desktop"},
		},
		{
			"Mozilla/5.0 (X11; CrOS x86_64 14541.0.0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
			Info{Browser: "Chrome 120.0.0.0", OS: "Chrome OS", DeviceType: "desktop"},
		},
		{"curl/8.4.0", Info{Browser: "curl 8.4.0", DeviceType: "desktop"}},
		{"PostmanRuntime/7.36.0", Info{Browser: "Postman Desktop 7.36.0", DeviceType: "desktop"}},
		{"something unknown", Info{DeviceType: "desktop"}},
	}
	for _, tt := range tests {
		if got := Parse(tt.ua); got != tt.want {
			t.Errorf("Parse(%q) got = %+v, want = %+v", tt.ua, got, tt.want)
		}
	}
}
