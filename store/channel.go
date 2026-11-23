// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Channel groups a visit's traffic source into a marketing channel, using
// rules similar to Google Analytics' default channel grouping. referrerHost
// is the referring host as the browser sent it, not its Source name.
func Channel(referrerHost, utmSource, utmMedium string) string {
	ref := strings.ToLower(referrerHost)
	src := strings.ToLower(utmSource)
	if src == "" {
		src = ref
	}
	med := strings.ToLower(utmMedium)
	switch {
	case src == "" && med == "":
		return "Direct"
	case med == "email" || med == "e-mail" || med == "e_mail" || med == "newsletter" ||
		strings.Contains(src, "email") || strings.Contains(src, "newsletter") || isMail(src):
		return "Email"
	case med == "affiliate" || med == "affiliates":
		return "Affiliates"
	case med == "display" || med == "banner" || med == "cpm" || med == "interstitial" || med == "expandable":
		return "Display"
	case paidMedium.MatchString(med):
		switch {
		case isSearch(src):
			return "Paid Search"
		case isSource(src, socialNetworks), isSource(src, videoSites):
			return "Paid Social"
		}
		return "Paid Other"
	case isSource(src, aiAssistants):
		return "AI"
	case med == "organic" || isSearch(src):
		return "Organic Search"
	case med == "social" || med == "social-network" || med == "social-media" || med == "sm" || isSource(src, socialNetworks):
		return "Organic Social"
	case med == "video" || isSource(src, videoSites):
		return "Organic Video"
	case med == "referral" || ref != "" || src != "":
		return "Referral"
	}
	return "Direct"
}

// ClickIDs maps the query parameter an ad network adds to the links in its
// ads to that network. A visit that arrives with one came from a paid ad,
// even when the link has no UTM tags.
var ClickIDs = map[string]string{
	"gclid": "google", "gbraid": "google", "wbraid": "google", "msclkid": "bing",
	"ttclid": "tiktok", "twclid": "twitter", "li_fat_id": "linkedin",
}

var paidMedium = regexp.MustCompile(`^(.*cp.*|ppc|retargeting|paid.*)$`)

var (
	searchEngines  = []string{"google", "bing", "duckduckgo", "yahoo", "yandex", "ya.ru", "ecosia", "baidu", "qwant", "startpage", "search.brave.com", "kagi", "naver", "seznam", "ask.com", "aol", "com.google.android.googlequicksearchbox"}
	socialNetworks = []string{"facebook", "fb", "instagram", "twitter", "x.com", "t.co", "linkedin", "lnkd.in", "reddit", "news.ycombinator.com", "mastodon", "mastodon.social", "threads.net", "threads.com", "bsky.app", "pinterest", "tiktok", "snapchat", "tumblr", "quora", "discord", "telegram", "t.me", "whatsapp", "vk.com", "weibo", "lemmy"}
	videoSites     = []string{"youtube", "youtu.be", "vimeo", "twitch", "dailymotion"}
	aiAssistants   = []string{"chatgpt.com", "chat.openai.com", "openai", "perplexity", "claude.ai", "gemini.google.com", "bard.google.com", "copilot.microsoft.com", "you.com", "deepseek", "chat.mistral.ai", "grok.com", "meta.ai", "phind", "poe.com"}

	// Subdomains of a search engine that are not its search.
	notSearch = []string{"news.", "docs.", "drive.", "accounts.", "translate.", "classroom.", "meet.", "calendar.", "sites.", "groups.", "play.", "support.", "colab.", "keep.", "chat.", "maps.", "finance.", "sports.", "360."}
	mailHosts = []string{"mail.", "webmail.", "email.", "outlook.", "com.google.android.gm", "com.microsoft.office.outlook", "com.yahoo.mobile.client.android.mail", "ru.yandex.mail"}
)

// isSource reports whether src (a host like "www.google.co.uk" or a utm_source
// like "google") belongs to one of the named sites.
func isSource(src string, names []string) bool {
	padded := "." + src + "."
	for _, n := range names {
		if strings.Contains(padded, "."+n+".") {
			return true
		}
	}
	return false
}

func isSearch(src string) bool {
	if !isSource(src, searchEngines) {
		return false
	}
	for _, p := range notSearch {
		if strings.HasPrefix(src, p) {
			return false
		}
	}
	return true
}

// isMail reports whether src is a webmail site or a mail app.
func isMail(src string) bool {
	for _, p := range mailHosts {
		if strings.HasPrefix(src, p) {
			return true
		}
	}
	return false
}

// siteNames and searchNames give the well-known sources one name, so that
// google.com, google.com.tr and the Google app are one row. Each entry is
// matched like isSource and the first match wins. A search engine's name
// only goes to its search, not to its mail or other products.
var (
	siteNames = [][2]string{
		{"com.google.android.gm", "Gmail"}, {"mail.google.com", "Gmail"},
		{"gemini.google.com", "Gemini"}, {"bard.google.com", "Gemini"}, {"news.google.com", "Google News"},
		{"chatgpt.com", "ChatGPT"}, {"chat.openai.com", "ChatGPT"}, {"perplexity", "Perplexity"}, {"claude.ai", "Claude"},
		{"copilot.microsoft.com", "Copilot"},
		{"facebook", "Facebook"}, {"fb", "Facebook"}, {"instagram", "Instagram"},
		{"twitter", "X"}, {"x.com", "X"}, {"t.co", "X"}, {"linkedin", "LinkedIn"}, {"lnkd.in", "LinkedIn"},
		{"reddit", "Reddit"}, {"news.ycombinator.com", "Hacker News"}, {"threads.net", "Threads"}, {"threads.com", "Threads"},
		{"bsky.app", "Bluesky"}, {"pinterest", "Pinterest"}, {"tiktok", "TikTok"}, {"telegram", "Telegram"}, {"t.me", "Telegram"},
		{"whatsapp", "WhatsApp"}, {"youtube", "YouTube"}, {"youtu.be", "YouTube"},
	}
	searchNames = [][2]string{
		{"google", "Google"}, {"com.google.android.googlequicksearchbox", "Google"},
		{"bing", "Bing"}, {"duckduckgo", "DuckDuckGo"}, {"yandex", "Yandex"}, {"ya.ru", "Yandex"}, {"yahoo", "Yahoo"},
		{"ecosia", "Ecosia"}, {"baidu", "Baidu"}, {"search.brave.com", "Brave"}, {"kagi", "Kagi"}, {"naver", "Naver"},
	}
)

// DirectSource is how the dashboard names a visit without a source.
const DirectSource = "Direct / None"

// Source names where a visit came from: the referring site, by its name if
// it is a well-known one, or else the UTM source. It is "" for a direct
// visit.
func Source(referrerHost, utmSource string) string {
	raw := referrerHost
	if raw == "" {
		raw = strings.TrimSpace(utmSource)
	}
	src := strings.ToLower(raw)
	for _, n := range siteNames {
		if isSource(src, []string{n[0]}) {
			return n[1]
		}
	}
	if isSearch(src) && !isMail(src) {
		for _, n := range searchNames {
			if isSource(src, []string{n[0]}) {
				return n[1]
			}
		}
	}
	return raw
}

// UnescapePath percent-decodes a path for storing and showing, so /yaz%C4%B1
// reads /yazı. A segment stays encoded when decoding it would change what
// the path means (an encoded /, ? or #) or isn't readable text.
func UnescapePath(p string) string {
	if !strings.Contains(p, "%") {
		return p
	}
	segs := strings.Split(p, "/")
	for i, s := range segs {
		d, err := url.PathUnescape(s)
		if err != nil || !utf8.ValidString(d) || strings.ContainsAny(d, "/?#%") || strings.ContainsFunc(d, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			continue
		}
		segs[i] = d
	}
	return strings.Join(segs, "/")
}
