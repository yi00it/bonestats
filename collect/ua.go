// SPDX-License-Identifier: AGPL-3.0-only

package collect

import (
	"regexp"
	"strings"
)

// A deliberately small user-agent parser: we only need coarse browser and OS
// families, never versions or anything that helps fingerprint a visitor.

var botRe = regexp.MustCompile(`(?i)bot|crawl|spider|slurp|headless|lighthouse|pingdom|monitor|curl|wget|python|go-http|java/|okhttp|phantomjs|preview|facebookexternalhit|scrapy|inspectiontool|googleother|mediapartners|feedfetcher|pagespeed|gtmetrix|ptst/|externalagent|chatgpt-user|yeti/`)

func isBot(ua string) bool {
	// Cubot is a phone maker whose name is in its phones' user agents.
	return ua == "" || botRe.MatchString(strings.ReplaceAll(ua, "CUBOT", ""))
}

func parseBrowser(ua string) string {
	switch {
	// Apps that open links in their own browser.
	case strings.Contains(ua, "Instagram"):
		return "Instagram"
	case strings.Contains(ua, "FBAN/"), strings.Contains(ua, "FBAV/"), strings.Contains(ua, "FB_IAB/"):
		return "Facebook"
	case strings.Contains(ua, "musical_ly"), strings.Contains(ua, "BytedanceWebview"), strings.Contains(ua, "TikTok"):
		return "TikTok"
	case strings.Contains(ua, "DuckDuckGo/"), strings.Contains(ua, "Ddg/"):
		return "DuckDuckGo"
	case strings.Contains(ua, "Edg/"), strings.Contains(ua, "EdgA/"), strings.Contains(ua, "EdgiOS/"):
		return "Edge"
	case strings.Contains(ua, "OPR/"), strings.Contains(ua, "Opera"):
		return "Opera"
	case strings.Contains(ua, "SamsungBrowser/"):
		return "Samsung Internet"
	case strings.Contains(ua, "YaBrowser/"):
		return "Yandex"
	case strings.Contains(ua, "Vivaldi/"):
		return "Vivaldi"
	case strings.Contains(ua, "Firefox/"), strings.Contains(ua, "FxiOS/"):
		return "Firefox"
	case strings.Contains(ua, "Chrome/"), strings.Contains(ua, "CriOS/"):
		return "Chrome"
	case strings.Contains(ua, "Safari/"):
		return "Safari"
	default:
		return "Other"
	}
}

func parseOS(ua string) string {
	switch {
	case strings.Contains(ua, "Windows"):
		return "Windows"
	case strings.Contains(ua, "Android"):
		return "Android"
	case strings.Contains(ua, "iPhone"), strings.Contains(ua, "iPad"), strings.Contains(ua, "iPod"):
		return "iOS"
	case strings.Contains(ua, "CrOS"):
		return "ChromeOS"
	case strings.Contains(ua, "Mac OS X"), strings.Contains(ua, "Macintosh"):
		return "macOS"
	case strings.Contains(ua, "Linux"):
		return "Linux"
	default:
		return "Other"
	}
}

// deviceClass tells phones, tablets and desktops apart. The user agent
// decides where it can: the viewport width alone would call a phone held
// sideways or a narrow desktop window a tablet. An iPad asking for desktop
// sites calls itself a Mac, so a Mac with a touch screen is a tablet. Only
// for an unknown user agent does the width decide; neither the width nor
// the number of touch points is stored.
func deviceClass(ua string, width, touch int) string {
	switch {
	case strings.Contains(ua, "iPad"), strings.Contains(ua, "Tablet"),
		strings.Contains(ua, "Macintosh") && touch > 1,
		strings.Contains(ua, "Android") && !strings.Contains(ua, "Mobile"):
		return "Tablet"
	case strings.Contains(ua, "Mobi"), strings.Contains(ua, "iPhone"), strings.Contains(ua, "iPod"):
		return "Mobile"
	case parseOS(ua) != "Other":
		return "Desktop"
	case width <= 0:
		return ""
	case width < 576:
		return "Mobile"
	case width < 992:
		return "Tablet"
	default:
		return "Desktop"
	}
}

var (
	browserVersionRe = map[string]*regexp.Regexp{
		"Edge":             regexp.MustCompile(`Edg(?:A|iOS)?/(\d+)`),
		"Opera":            regexp.MustCompile(`OPR/(\d+)`),
		"Samsung Internet": regexp.MustCompile(`SamsungBrowser/(\d+)`),
		"Yandex":           regexp.MustCompile(`YaBrowser/(\d+)`),
		"Vivaldi":          regexp.MustCompile(`Vivaldi/(\d+)`),
		"Firefox":          regexp.MustCompile(`(?:Firefox|FxiOS)/(\d+)`),
		"Chrome":           regexp.MustCompile(`(?:Chrome|CriOS)/(\d+)`),
		"Safari":           regexp.MustCompile(`Version/(\d+)`),
	}
	androidRe = regexp.MustCompile(`Android (\d+)`)
	iosRe     = regexp.MustCompile(`OS (\d+)_`)
	safariRe  = regexp.MustCompile(`Version/(\d+)`)
	macRe     = regexp.MustCompile(`Mac OS X (\d+)[_.](\d+)`)
	windowsRe = regexp.MustCompile(`Windows NT (\d+\.\d+)`)
)

// browserVersion returns e.g. "Chrome 141" (major version only).
func browserVersion(ua, browser string) string {
	if re := browserVersionRe[browser]; re != nil {
		if m := re.FindStringSubmatch(ua); m != nil {
			return browser + " " + m[1]
		}
	}
	return ""
}

// osVersion returns e.g. "iOS 18" or "Windows 10/11".
func osVersion(ua, os string) string {
	switch os {
	case "Windows":
		if m := windowsRe.FindStringSubmatch(ua); m != nil {
			v := map[string]string{"10.0": "10/11", "6.3": "8.1", "6.2": "8", "6.1": "7"}[m[1]]
			if v != "" {
				return "Windows " + v
			}
		}
	case "Android":
		// Chrome says "Android 10; K" on every version of Android.
		if m := androidRe.FindStringSubmatch(ua); m != nil && !strings.Contains(ua, "Android 10; K)") {
			return "Android " + m[1]
		}
	case "iOS":
		// Since version 26, Safari reports iOS as 18 and its own version is
		// the version of iOS.
		if m := safariRe.FindStringSubmatch(ua); m != nil && len(m[1]) == 2 && m[1] >= "26" {
			return "iOS " + m[1]
		}
		if m := iosRe.FindStringSubmatch(ua); m != nil {
			return "iOS " + m[1]
		}
	case "macOS":
		// browsers freeze this at 10.15 on newer macOS versions
		if m := macRe.FindStringSubmatch(ua); m != nil {
			if m[1] == "10" && m[2] == "15" {
				return "macOS 10.15+"
			}
			return "macOS " + m[1] + "." + m[2]
		}
	}
	return ""
}

// screenBucket groups the viewport width into ranges; the exact width is
// never stored.
func screenBucket(w int) string {
	switch {
	case w <= 0:
		return ""
	case w < 576:
		return "< 576px"
	case w < 768:
		return "576–767px"
	case w < 992:
		return "768–991px"
	case w < 1280:
		return "992–1279px"
	case w < 1440:
		return "1280–1439px"
	case w < 1920:
		return "1440–1919px"
	default:
		return "≥ 1920px"
	}
}
