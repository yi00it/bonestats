package collect

import (
	"regexp"
	"strings"
)

// A deliberately small user-agent parser: we only need coarse browser and OS
// families, never versions or anything that helps fingerprint a visitor.

var botRe = regexp.MustCompile(`(?i)bot|crawl|spider|slurp|headless|lighthouse|pingdom|monitor|curl|wget|python|go-http|java/|okhttp|phantomjs|preview|facebookexternalhit|scrapy`)

func isBot(ua string) bool {
	return ua == "" || botRe.MatchString(ua)
}

func parseBrowser(ua string) string {
	switch {
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

// deviceFromWidth classifies by viewport width reported by the tracker; the
// width itself is not stored.
func deviceFromWidth(w int) string {
	switch {
	case w <= 0:
		return ""
	case w < 576:
		return "Mobile"
	case w < 992:
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
		if m := androidRe.FindStringSubmatch(ua); m != nil {
			return "Android " + m[1]
		}
	case "iOS":
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
