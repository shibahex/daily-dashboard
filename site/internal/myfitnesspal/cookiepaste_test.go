package myfitnesspal

import (
	"strings"
	"testing"
)

func TestCookieFromPaste(t *testing.T) {
	want := "remember_me=abc123; _mfp_session=xyz"
	cases := map[string]string{
		"header value": "remember_me=abc123; _mfp_session=xyz",
		"header line":  "Cookie: remember_me=abc123; _mfp_session=xyz",
		"raw headers":  "GET /food/diary HTTP/2\nHost: www.myfitnesspal.com\nAccept: text/html\ncookie: remember_me=abc123; _mfp_session=xyz\nuser-agent: Example",
		"one line":     "Host: www.myfitnesspal.com Accept: text/html cookie: remember_me=abc123; _mfp_session=xyz",
		"chrome bash": `curl 'https://www.myfitnesspal.com/food/diary' \
  -H 'accept: text/html' \
  -b 'remember_me=abc123; _mfp_session=xyz' \
  -H 'user-agent: Example'`,
		"newlines stripped": `curl 'https://www.myfitnesspal.com/food/diary' \  -H 'accept: text/html' \  -b 'remember_me=abc123; _mfp_session=xyz'`,
		"firefox bash":      `curl 'https://www.myfitnesspal.com/food/diary' -H 'Accept: text/html' -H 'Cookie: remember_me=abc123; _mfp_session=xyz' -H 'Connection: keep-alive'`,
		"chrome cmd":        `curl ^"https://www.myfitnesspal.com/food/diary^" ^` + "\n" + `  -H ^"accept: text/html^" ^` + "\n" + `  -b ^"remember_me=abc123; _mfp_session=xyz^"`,
		"powershell": `$session = New-Object Microsoft.PowerShell.Commands.WebRequestSession
$session.Cookies.Add((New-Object System.Net.Cookie("remember_me", "abc123", "/", ".myfitnesspal.com")))
$session.Cookies.Add((New-Object System.Net.Cookie("_mfp_session", "xyz", "/", "www.myfitnesspal.com")))
Invoke-WebRequest -UseBasicParsing -Uri "https://www.myfitnesspal.com/food/diary" -WebSession $session`,
		"json export": `[{"domain":".myfitnesspal.com","name":"remember_me","value":"abc123"},{"domain":"www.myfitnesspal.com","name":"_mfp_session","value":"xyz"}]`,
		"cookies.txt": "# Netscape HTTP Cookie File\n.myfitnesspal.com\tTRUE\t/\tTRUE\t0\tremember_me\tabc123\nwww.myfitnesspal.com\tFALSE\t/\tTRUE\t0\t_mfp_session\txyz\n",
	}
	for name, paste := range cases {
		got, err := CookieFromPaste(paste)
		if err != nil || got != want {
			t.Errorf("%s: got %q, %v", name, got, err)
		}
	}
	refused := map[string]string{
		"empty":        "",
		"session only": "__Secure-next-auth.session-token=abc",
		"bare token":   "eyJhbGciOiJkaXIi",
		"other site":   `curl 'https://example.com/' -b 'remember_me=abc'`,
		"no cookies":   `curl 'https://www.myfitnesspal.com/food/diary' -H 'accept: text/html'`,
		"random text":  "hello\nworld",
	}
	for name, paste := range refused {
		if got, err := CookieFromPaste(paste); err == nil {
			t.Errorf("%s: took %q", name, got)
		}
	}
	long := "eyJfcmFpbHMiOnsibWVzc2FnZSI6IkJBaDdDRWtpRDNObGMzTnBiMjVmYVdRR09nWkZWRWtpUlRBME1qQmhNVE5qT0RZMk1qWT0iLCJleHAiOm51bGx9fQ%3D%3D--3f2a9c0d1e"
	for _, value := range []string{"123456789012345%3A0123456789abcdef0123456789abcdef", long, "eyJfcmFpbHMiOnsibWVzc2FnZSI6IkJBaDdDRWtpRDNObGMzTnBiMjVmYVdRR09nWkZWRWtpUlRBME1qQmhNVE5qT0RZMk1qWT0iLCJleHAiOm51bGx9fQ==--3f2a9c0d1e", "  " + long + "\n"} {
		if got, err := CookieFromPaste(value); err != nil || got != "remember_me="+strings.TrimSpace(value) {
			t.Errorf("a lone remember_me value: got %q, %v", got, err)
		}
	}
	if _, err := CookieFromPaste("__Secure-next-auth.session-token=abc"); err == nil || !strings.Contains(err.Error(), "remember_me") {
		t.Errorf("a paste without the login should say what's missing: %v", err)
	}
}
