package myfitnesspal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"dashboard/internal/problem"
	"dashboard/internal/text"
)

const (
	siteURL   = "https://www.myfitnesspal.com/"
	apiURL    = "https://api.myfitnesspal.com/"
	userAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
)

// loginCookies sign a client in. MyFitnessPal renews them through Set-Cookie; renewed ones are
// saved back to cookies.json (as the MCP does), or the next start goes back to old ones.
var loginCookies = []string{"__Secure-next-auth.session-token", "remember_me", "_mfp_session"}

// errSignedOut means MyFitnessPal no longer accepts the saved login.
var errSignedOut error = problem.Error{Kind: problem.Unavailable, Message: "MyFitnessPal login expired: reconnect it on the Account page"}

// session is one signed-in MyFitnessPal login, kept between requests so a sync is only the
// requests it needs: the login cookies, the API token and the last diary page's form token.
type session struct {
	path      string // cookies.json
	envCookie string // MFP_COOKIE, for the server's own account

	signMu    sync.Mutex // one sign-in at a time
	mu        sync.Mutex
	loadedAt  time.Time // cookies.json's modification time when it was read
	cookies   map[string]string
	username  string
	token     string
	userID    string
	tokenEnd  time.Time
	renewed   time.Time            // when NextAuth's session was last renewed
	pages     map[string]diaryPage // the last diary page read for each day
	formToken string               // the last entry edit form's token

	http *http.Client
}

var (
	sessionsMu sync.Mutex
	sessions   = map[string]*session{}
)

// session is the shared login for this client's cookies.json.
func (c Client) session() *session {
	sessionsMu.Lock()
	defer sessionsMu.Unlock()
	path := c.CookiePath()
	s := sessions[path]
	if s == nil {
		s = &session{path: path, http: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}}}
		if c.ConfigHome == "" {
			s.envCookie = os.Getenv("MFP_COOKIE")
		}
		sessions[path] = s
	}
	return s
}

type savedLogin struct {
	Cookies  map[string]string `json:"cookies"`
	Username string            `json:"username,omitempty"`
}

// load reads cookies.json when it changed (a new Connect, or the MCP renewing the login) and
// forgets the login when it's gone (Disconnect, or the user's data locked).
func (s *session) load() error {
	info, err := os.Stat(s.path)
	if err != nil {
		if s.envCookie != "" && s.cookies == nil {
			s.cookies = parseCookieHeader(s.envCookie)
			s.loadedAt = time.Time{}
			return nil
		}
		if s.envCookie == "" {
			s.forget()
			return problem.Unavailablef("MyFitnessPal isn't connected yet. Connect it on the Account page.")
		}
		return nil
	}
	if s.cookies != nil && info.ModTime().Equal(s.loadedAt) {
		return nil
	}
	var saved savedLogin
	data, err := os.ReadFile(s.path)
	if err == nil {
		err = json.Unmarshal(data, &saved)
	}
	if err != nil || len(saved.Cookies) == 0 {
		s.forget()
		return problem.Unavailablef("MyFitnessPal's saved login is unreadable. Connect it again on the Account page.")
	}
	s.forget()
	s.cookies, s.username, s.loadedAt = saved.Cookies, saved.Username, info.ModTime()
	return nil
}

// reset forgets the login so the next request reads cookies.json again.
func (s *session) reset() {
	s.mu.Lock()
	s.forget()
	s.mu.Unlock()
}

func (s *session) forget() {
	s.cookies, s.token, s.tokenEnd, s.pages, s.formToken = nil, "", time.Time{}, nil, ""
}

// keepCookies takes the response's cookies and saves renewed login cookies into cookies.json,
// merged with what is there.
func (s *session) keepCookies(resp *http.Response) {
	changed := map[string]string{}
	for _, cookie := range resp.Cookies() {
		if cookie.MaxAge < 0 || cookie.Value == "" {
			if !slices.Contains(loginCookies, cookie.Name) {
				delete(s.cookies, cookie.Name)
			}
			continue
		}
		if s.cookies[cookie.Name] != cookie.Value && slices.Contains(loginCookies, cookie.Name) {
			changed[cookie.Name] = cookie.Value
		}
		s.cookies[cookie.Name] = cookie.Value
	}
	if len(changed) == 0 || s.envCookie != "" && s.loadedAt.IsZero() {
		return
	}
	var saved savedLogin
	if data, err := os.ReadFile(s.path); err == nil {
		json.Unmarshal(data, &saved)
	}
	if saved.Cookies == nil {
		saved.Cookies = map[string]string{}
	}
	for name, value := range changed {
		saved.Cookies[name] = value
	}
	data, _ := json.MarshalIndent(saved, "", "  ")
	if err := os.WriteFile(s.path, data, 0o600); err != nil {
		log.Printf("saving the renewed MyFitnessPal login: %v", err)
		return
	}
	if info, err := os.Stat(s.path); err == nil {
		s.loadedAt = info.ModTime()
	}
}

func parseCookieHeader(header string) map[string]string {
	header = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(header), "Cookie:"))
	cookies := map[string]string{}
	if !strings.Contains(header, "=") {
		cookies[loginCookies[0]] = header
		return cookies
	}
	for _, part := range strings.Split(header, ";") {
		if name, value, found := strings.Cut(part, "="); found {
			cookies[strings.TrimSpace(name)] = strings.TrimSpace(value)
		}
	}
	return cookies
}

// request is one call to MyFitnessPal: the web site (signed in with cookies) or its API (with
// the token too, since the web app's own scripts send both).
type request struct {
	method, url string
	form        url.Values // a form post
	json        any        // a JSON post
	headers     map[string]string
	api         bool // send the API token
}

type response struct {
	status int
	header http.Header
	body   []byte
}

// do sends a request, signing in first when the token is missing or old. A 401 renews the token
// and tries once more.
func (s *session) do(ctx context.Context, r request) (response, error) {
	s.mu.Lock()
	err := s.load()
	s.mu.Unlock()
	if err == nil && r.api {
		err = s.signIn(ctx, "")
	}
	if err != nil {
		return response{}, err
	}
	used := s.currentToken()
	resp, err := s.send(ctx, r)
	if err == nil && r.api && resp.status == http.StatusUnauthorized {
		if err = s.signIn(ctx, used); err == nil {
			resp, err = s.send(ctx, r)
		}
	}
	// A form sent back to the sign-in page wasn't taken either.
	if err == nil && resp.status >= 300 && resp.status < 400 && strings.Contains(resp.header.Get("Location"), "login") {
		return resp, errSignedOut
	}
	if err == nil && (resp.status == http.StatusUnauthorized || resp.status == http.StatusForbidden) {
		return resp, errSignedOut
	}
	return resp, err
}

func (s *session) send(ctx context.Context, r request) (response, error) {
	var body io.Reader
	contentType := ""
	switch {
	case r.form != nil:
		body, contentType = strings.NewReader(r.form.Encode()), "application/x-www-form-urlencoded; charset=UTF-8"
	case r.json != nil:
		data, _ := json.Marshal(r.json)
		body, contentType = strings.NewReader(string(data)), "application/json"
	}
	method := r.method
	if method == "" {
		method = http.MethodGet
	}
	req, err := http.NewRequestWithContext(ctx, method, r.url, body)
	if err != nil {
		return response{}, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	s.mu.Lock()
	for name, value := range s.cookies {
		req.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	if r.api {
		req.Header.Set("Authorization", "Bearer "+s.token)
		req.Header.Set("mfp-client-id", "mfp-main-js")
		req.Header.Set("mfp-user-id", s.userID)
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
	}
	s.mu.Unlock()
	for name, value := range r.headers {
		req.Header.Set(name, value)
	}
	resp, err := s.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return response{}, problem.Unavailablef("MyFitnessPal took too long to answer")
		}
		return response{}, fmt.Errorf("%w: %v", problem.Unavailablef("Couldn't reach MyFitnessPal"), err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	s.mu.Lock()
	if s.cookies != nil {
		s.keepCookies(resp)
	}
	s.mu.Unlock()
	return response{status: resp.StatusCode, header: resp.Header, body: data}, err
}

// signIn gets an API token from the web site's login (the call the web app starts with), and
// renews NextAuth's session now and then: only that endpoint issues a fresh session cookie.
// A token the API turned down (rejected) is replaced even if it looks current.
func (s *session) signIn(ctx context.Context, rejected string) error {
	s.signMu.Lock()
	defer s.signMu.Unlock()
	s.mu.Lock()
	current := s.token != "" && s.token != rejected && time.Now().Before(s.tokenEnd)
	s.mu.Unlock()
	if current {
		return nil
	}
	if time.Since(s.renewed) > time.Hour {
		s.send(ctx, request{url: siteURL + "api/auth/session", headers: map[string]string{"Accept": "application/json"}})
		s.renewed = time.Now()
	}
	resp, err := s.send(ctx, request{url: siteURL + "user/auth_token?refresh=true", headers: map[string]string{"Accept": "application/json"}})
	if err != nil {
		return err
	}
	var auth struct {
		AccessToken string `json:"access_token"`
		UserID      any    `json:"user_id"`
		ExpiresIn   int    `json:"expires_in"`
	}
	// Anything but JSON (the sign-in page) means the login is no longer accepted.
	if resp.status != http.StatusOK || !strings.HasPrefix(resp.header.Get("Content-Type"), "application/json") ||
		json.Unmarshal(resp.body, &auth) != nil || auth.AccessToken == "" {
		return errSignedOut
	}
	lifetime := 30 * time.Minute
	if auth.ExpiresIn > 0 {
		lifetime = min(time.Duration(auth.ExpiresIn)*time.Second-time.Minute, time.Hour)
	}
	s.mu.Lock()
	s.token, s.userID, s.tokenEnd = auth.AccessToken, fmt.Sprint(auth.UserID), time.Now().Add(lifetime)
	s.mu.Unlock()
	return nil
}

func (s *session) currentToken() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.token
}

// getJSON reads an API answer into out.
func (s *session) getJSON(ctx context.Context, path string, out any) error {
	resp, err := s.do(ctx, request{url: apiURL + path, api: true, headers: map[string]string{"Accept": "application/json"}})
	if err != nil {
		return err
	}
	if resp.status != http.StatusOK {
		return problem.Failedf(fmt.Sprintf("MyFitnessPal %s returned HTTP %d", endpoint(path), resp.status))
	}
	return json.Unmarshal(resp.body, out)
}

// getSiteJSON reads a JSON answer from the website (not the API).
func (s *session) getSiteJSON(ctx context.Context, path string, out any) error {
	resp, err := s.do(ctx, request{url: siteURL + path, api: true, headers: map[string]string{"Accept": "application/json"}})
	if err != nil {
		return err
	}
	if resp.status != http.StatusOK {
		return problem.Failedf(fmt.Sprintf("MyFitnessPal /%s returned HTTP %d", endpoint(path), resp.status))
	}
	return json.Unmarshal(resp.body, out)
}

// getPage reads a web page.
func (s *session) getPage(ctx context.Context, path string) ([]byte, error) {
	target, _ := url.Parse(siteURL + path)
	resp, err := s.do(ctx, request{url: target.String(), api: true})
	// Follow redirects (the diary moves to the user's own address), except to the sign-in page.
	for hops := 0; err == nil && hops < 5 && resp.status >= 300 && resp.status < 400; hops++ {
		next, parseErr := target.Parse(resp.header.Get("Location"))
		if parseErr != nil {
			break
		}
		if strings.Contains(next.Path, "login") || strings.Contains(next.Path, "signin") || next.Host != target.Host {
			return nil, errSignedOut
		}
		target = next
		resp, err = s.do(ctx, request{url: target.String(), api: true})
	}
	if err != nil {
		return nil, err
	}
	if resp.status != http.StatusOK {
		return nil, problem.Failedf(fmt.Sprintf("MyFitnessPal /%s returned HTTP %d", endpoint(path), resp.status))
	}
	return resp.body, nil
}

func endpoint(path string) string {
	path, _, _ = strings.Cut(path, "?")
	return text.Truncate(path, 40)
}
