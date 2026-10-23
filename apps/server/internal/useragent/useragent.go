package useragent

import (
	"strings"
)

// Info is what a user agent tells about its client.
type Info struct {
	// Browser is the client's name and version ("Chrome 120.0.0.0"), or
	// its name alone when it has no version; "" when unknown.
	Browser string
	// OS is the operating system ("Mac", "Windows", "iOS"); "" when
	// unknown.
	OS string
	// DeviceType is the device ("desktop", "smartphone", "tablet", "tv",
	// "console"); "desktop" when unknown, as lago defaults it.
	DeviceType string
}

// client is a browser or library rule: the user agent contains token,
// followed by the version.
type client struct {
	token string
	name  string
	// mobile is the name on a mobile device, "" for name.
	mobile string
}

// clients are tried in order: browsers built on Chrome or Safari announce
// those too, so they come first.
var clients = []client{
	{token: "EdgA/", name: "Microsoft Edge"},
	{token: "EdgiOS/", name: "Microsoft Edge"},
	{token: "Edg/", name: "Microsoft Edge"},
	{token: "Edge/", name: "Microsoft Edge"},
	{token: "OPR/", name: "Opera", mobile: "Opera Mobile"},
	{token: "OPiOS/", name: "Opera Touch"},
	{token: "SamsungBrowser/", name: "Samsung Browser"},
	{token: "YaBrowser/", name: "Yandex Browser"},
	{token: "Vivaldi/", name: "Vivaldi"},
	{token: "UCBrowser/", name: "UC Browser"},
	{token: "DuckDuckGo/", name: "DuckDuckGo Privacy Browser"},
	{token: "FxiOS/", name: "Firefox Mobile iOS"},
	{token: "CriOS/", name: "Chrome Mobile iOS"},
	{token: "Firefox/", name: "Firefox", mobile: "Firefox Mobile"},
	{token: "Chromium/", name: "Chromium"},
	{token: "HeadlessChrome/", name: "Headless Chrome"},
	{token: "Chrome/", name: "Chrome", mobile: "Chrome Mobile"},
	{token: "Trident/", name: "Internet Explorer"},
	{token: "MSIE ", name: "Internet Explorer"},
	{token: "PostmanRuntime/", name: "Postman Desktop"},
	{token: "Insomnia/", name: "Insomnia REST Client"},
	{token: "curl/", name: "curl"},
	{token: "Wget/", name: "Wget"},
	{token: "python-requests/", name: "Python Requests"},
	{token: "Go-http-client/", name: "Go-http-client"},
	{token: "okhttp/", name: "OkHttp"},
	{token: "axios/", name: "Axios"},
	{token: "node-fetch/", name: "Node Fetch"},
}

// Parse parses a User-Agent header. An empty one gives the zero Info.
func Parse(ua string) Info {
	ua = strings.TrimSpace(ua)
	if ua == "" {
		return Info{}
	}
	info := Info{OS: osName(ua)}
	info.DeviceType = deviceType(ua, info.OS)
	info.Browser = browser(ua, info.OS, info.DeviceType)
	return info
}

func osName(ua string) string {
	switch {
	case strings.Contains(ua, "Windows Phone"):
		return "Windows Phone"
	case strings.Contains(ua, "Windows"):
		return "Windows"
	case strings.Contains(ua, "iPad"):
		return "iPadOS"
	case strings.Contains(ua, "iPhone"), strings.Contains(ua, "iPod"):
		return "iOS"
	case strings.Contains(ua, "CrOS"):
		return "Chrome OS"
	case strings.Contains(ua, "Android"):
		return "Android"
	case strings.Contains(ua, "Macintosh"), strings.Contains(ua, "Mac OS X"):
		return "Mac"
	case strings.Contains(ua, "Ubuntu"):
		return "Ubuntu"
	case strings.Contains(ua, "Fedora"):
		return "Fedora"
	case strings.Contains(ua, "FreeBSD"):
		return "FreeBSD"
	case strings.Contains(ua, "Linux"), strings.Contains(ua, "X11"):
		return "GNU/Linux"
	}
	return ""
}

func deviceType(ua, os string) string {
	switch {
	case containsAny(ua, "SmartTV", "SMART-TV", "Smart-TV", "AppleTV", "CrKey", "HbbTV", "GoogleTV", "Tizen TV", "Web0S"):
		return "tv"
	case containsAny(ua, "PlayStation", "Xbox", "Nintendo"):
		return "console"
	case os == "iPadOS", strings.Contains(ua, "Tablet"):
		return "tablet"
	case os == "iOS", os == "Windows Phone":
		return "smartphone"
	case os == "Android":
		// Android tablets omit "Mobile" (Google's convention).
		if strings.Contains(ua, "Mobile") {
			return "smartphone"
		}
		return "tablet"
	case strings.Contains(ua, "Mobile"):
		return "smartphone"
	}
	return "desktop"
}

func browser(ua, os, device string) string {
	mobile := device == "smartphone" || device == "tablet"
	for _, c := range clients {
		i := strings.Index(ua, c.token)
		if i < 0 {
			continue
		}
		name := c.name
		if mobile && c.mobile != "" {
			name = c.mobile
		}
		if name == "Chrome Mobile" && strings.Contains(ua, "; wv)") {
			name = "Chrome Webview"
		}
		v := version(ua[i+len(c.token):])
		if c.token == "Trident/" {
			// Trident 7 is Internet Explorer 11.
			v = tridentVersion(ua, v)
		}
		return strings.TrimSpace(name + " " + v)
	}
	if strings.Contains(ua, "Safari/") || strings.Contains(ua, "AppleWebKit/") && (os == "iOS" || os == "iPadOS") {
		name := "Safari"
		if os == "iOS" || os == "iPadOS" {
			name = "Mobile Safari"
		}
		v := ""
		if i := strings.Index(ua, "Version/"); i >= 0 {
			v = version(ua[i+len("Version/"):])
		}
		return strings.TrimSpace(name + " " + v)
	}
	return ""
}

// version returns the leading version of s: digits and dots.
func version(s string) string {
	end := 0
	for end < len(s) && (s[end] >= '0' && s[end] <= '9' || s[end] == '.') {
		end++
	}
	return strings.TrimRight(s[:end], ".")
}

func tridentVersion(ua, trident string) string {
	if i := strings.Index(ua, "MSIE "); i >= 0 {
		return version(ua[i+len("MSIE "):])
	}
	if i := strings.Index(ua, "rv:"); i >= 0 {
		return version(ua[i+len("rv:"):])
	}
	return trident
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
