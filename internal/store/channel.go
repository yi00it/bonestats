package store

import (
	"regexp"
	"strings"
)

// Channel groups a visit's traffic source into a marketing channel, using
// rules similar to Google Analytics' default channel grouping.
func Channel(referrerHost, utmSource, utmMedium string) string {
	src := strings.ToLower(utmSource)
	if src == "" {
		src = strings.ToLower(referrerHost)
	}
	med := strings.ToLower(utmMedium)
	switch {
	case src == "" && med == "":
		return "Direct"
	case med == "email" || med == "e-mail" || med == "e_mail" || med == "newsletter" ||
		strings.Contains(src, "email") || strings.Contains(src, "newsletter"):
		return "Email"
	case med == "affiliate" || med == "affiliates":
		return "Affiliates"
	case med == "display" || med == "banner" || med == "cpm" || med == "interstitial" || med == "expandable":
		return "Display"
	case paidMedium.MatchString(med):
		switch {
		case isSource(src, searchEngines):
			return "Paid Search"
		case isSource(src, socialNetworks), isSource(src, videoSites):
			return "Paid Social"
		}
		return "Paid Other"
	case med == "organic" || isSource(src, searchEngines):
		return "Organic Search"
	case med == "social" || med == "social-network" || med == "social-media" || med == "sm" || isSource(src, socialNetworks):
		return "Organic Social"
	case med == "video" || isSource(src, videoSites):
		return "Organic Video"
	case med == "referral" || referrerHost != "" || src != "":
		return "Referral"
	}
	return "Direct"
}

var paidMedium = regexp.MustCompile(`^(.*cp.*|ppc|retargeting|paid.*)$`)

var (
	searchEngines  = []string{"google", "bing", "duckduckgo", "yahoo", "yandex", "ecosia", "baidu", "qwant", "startpage", "search.brave.com", "kagi", "naver", "seznam", "ask", "aol", "perplexity", "chatgpt.com", "you.com"}
	socialNetworks = []string{"facebook", "fb", "instagram", "twitter", "x.com", "t.co", "linkedin", "lnkd.in", "reddit", "news.ycombinator.com", "mastodon", "mastodon.social", "threads.net", "bsky.app", "pinterest", "tiktok", "snapchat", "tumblr", "quora", "discord", "telegram", "whatsapp", "vk.com", "weibo", "lemmy"}
	videoSites     = []string{"youtube", "youtu.be", "vimeo", "twitch", "dailymotion"}
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
