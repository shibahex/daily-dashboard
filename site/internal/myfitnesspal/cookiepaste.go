package myfitnesspal

import (
	"encoding/json"
	"regexp"
	"strings"

	"dashboard/internal/problem"
)

// Connecting takes MyFitnessPal's login cookies from whatever the user copied out of their
// browser: the remember_me cookie's value on its own (all the sign-in needs: MyFitnessPal hands
// out fresh session cookies from it), a request copied as cURL (bash, Windows cmd or PowerShell), the request headers, the
// cookie header's value, or a cookie export (JSON or cookies.txt).

var (
	curlCookie    = regexp.MustCompile(`(?is)(?:^|\s)(?:-b|--cookie)\s+\$?(?:'([^']*)'|"([^"]*)")`)
	curlHeader    = regexp.MustCompile(`(?is)(?:-H|--header)\s+\$?(?:'\s*cookie\s*:\s*([^']*)'|"\s*cookie\s*:\s*([^"]*)")`)
	headerLine    = regexp.MustCompile(`(?im)^\s*cookie\s*:\s*(.+?)\s*$`)
	powerShell    = regexp.MustCompile(`(?i)New-Object\s+System\.Net\.Cookie\(\s*"([^"]*)"\s*,\s*"([^"]*)"`)
	cmdEscape     = regexp.MustCompile(`\^(.)`)
	cookieName    = regexp.MustCompile(`^[!#$%&'*+\-.^_` + "`" + `|~0-9A-Za-z]+$`)
	signInCookies = []string{"remember_me", "_mfp_session"} // what MyFitnessPal's pages accept; the session token alone isn't enough
)

// CookieFromPaste finds the MyFitnessPal cookie header in what the user pasted, or says, in plain
// words, what's wrong with it.
func CookieFromPaste(paste string) (string, error) {
	paste = strings.TrimSpace(paste)
	if paste == "" {
		return "", problem.Invalidf("Paste what you copied from MyFitnessPal first")
	}
	if len(paste) > 200_000 {
		return "", problem.Invalidf("That's too long to be one request. Copy just the first request in the Network list.")
	}
	lower := strings.ToLower(paste)
	if strings.Contains(lower, "curl") && strings.Contains(lower, "http") && !strings.Contains(lower, "myfitnesspal.com") {
		return "", problem.Invalidf("That request isn't from myfitnesspal.com. Copy a request from the MyFitnessPal tab (the first one in the Network list).")
	}
	if rememberMeValue(paste) {
		return "remember_me=" + paste, nil
	}
	pairs := cookiePairs(paste)
	if len(pairs) == 0 && strings.Contains(paste, "^") { // Windows cmd escapes quotes as ^"
		pairs = cookiePairs(cmdEscape.ReplaceAllString(paste, "$1"))
	}
	if len(pairs) == 0 {
		if strings.Contains(lower, "curl") {
			return "", problem.Invalidf("That request has no cookies. Make sure you're logged in to MyFitnessPal in that tab, reload it, and copy the first request again.")
		}
		return "", problem.Invalidf("That doesn't look like a MyFitnessPal cookie. In the developer tools open Application → Cookies → www.myfitnesspal.com, find remember_me and copy its Value.")
	}
	var parts []string
	login := false
	for _, pair := range pairs {
		parts = append(parts, pair[0]+"="+pair[1])
		for _, name := range signInCookies {
			login = login || pair[0] == name
		}
	}
	if !login {
		return "", problem.Invalidf("That's not the remember_me cookie, which is what keeps you signed in. Log in at myfitnesspal.com in a normal, not private, window, then copy remember_me's Value from Application → Cookies.")
	}
	return strings.Join(parts, "; "), nil
}

// rememberMeValue reports whether paste is a lone cookie value (copied from the browser's cookie
// list), not name=value pairs or a request: one word, and not a known cookie's name and value.
func rememberMeValue(paste string) bool {
	if strings.ContainsAny(paste, " \t\r\n;'\"") || len(paste) < 20 {
		return false
	}
	// An "=" after a cookie name (they're short) makes it a pair; one deep inside a value is
	// base64 padding ("…==--3f2a").
	name, _, hasName := strings.Cut(paste, "=")
	return !hasName || len(name) > 64
}

// cookiePairs reads name=value pairs from the first format that has any, in the order found.
func cookiePairs(paste string) [][2]string {
	if m := curlCookie.FindStringSubmatch(paste); m != nil {
		return headerPairs(m[1] + m[2]) // one of the two is empty: single or double quotes
	}
	if m := curlHeader.FindStringSubmatch(paste); m != nil {
		return headerPairs(m[1] + m[2])
	}
	if matches := powerShell.FindAllStringSubmatch(paste, -1); matches != nil {
		var pairs [][2]string
		for _, m := range matches {
			pairs = appendPair(pairs, m[1], m[2])
		}
		return pairs
	}
	if pairs := jsonPairs(paste); pairs != nil {
		return pairs
	}
	if pairs := cookiesTxtPairs(paste); pairs != nil {
		return pairs
	}
	if m := headerLine.FindStringSubmatch(paste); m != nil {
		return headerPairs(m[1])
	}
	// "cookie: …" pasted into a one-line box, after other headers.
	if i := strings.Index(strings.ToLower(paste), "cookie:"); i >= 0 {
		return headerPairs(paste[i+len("cookie:"):])
	}
	if !strings.ContainsAny(paste, "\r\n") {
		return headerPairs(paste)
	}
	return nil
}

// headerPairs reads a Cookie header's value: "a=1; b=2".
func headerPairs(header string) [][2]string {
	var pairs [][2]string
	for _, part := range strings.Split(header, ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok {
			pairs = appendPair(pairs, strings.TrimSpace(name), strings.TrimSpace(value))
		}
	}
	return pairs
}

// jsonPairs reads a cookie export: a list of {"name", "value"}.
func jsonPairs(paste string) [][2]string {
	if !strings.HasPrefix(paste, "[") {
		return nil
	}
	var cookies []struct{ Name, Value string }
	if json.Unmarshal([]byte(paste), &cookies) != nil {
		return nil
	}
	var pairs [][2]string
	for _, c := range cookies {
		pairs = appendPair(pairs, c.Name, c.Value)
	}
	return pairs
}

// cookiesTxtPairs reads a Netscape cookies.txt: domain, flag, path, secure, expiry, name, value.
func cookiesTxtPairs(paste string) [][2]string {
	var pairs [][2]string
	for _, line := range strings.Split(paste, "\n") {
		fields := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(fields) == 7 && strings.Contains(fields[0], "myfitnesspal.com") {
			pairs = appendPair(pairs, fields[5], fields[6])
		}
	}
	return pairs
}

// appendPair adds a cookie that can go in a header, quotes and spaces trimmed off its value.
func appendPair(pairs [][2]string, name, value string) [][2]string {
	value = strings.Trim(strings.TrimSpace(value), `"`)
	if !cookieName.MatchString(name) || value == "" || strings.ContainsAny(value, "; \t\r\n") {
		return pairs
	}
	return append(pairs, [2]string{name, value})
}
