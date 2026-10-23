// Package websearch is the AI's web search for models that have none of their own (a local
// model, say): searching with the admin's SearXNG, and reading a page as plain text. Claude in
// the browser uses Anthropic's own search instead.
package websearch

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"dashboard/internal/problem"
)

const (
	maxResults   = 8
	maxPageBytes = 3 << 20
	maxPageText  = 40_000 // characters of a page the AI gets
)

// Result is one search result.
type Result struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Content string `json:"content"`
}

// Search asks the SearXNG at base (its JSON API must be on: "json" in search.formats).
func Search(ctx context.Context, client *http.Client, base, query string) ([]Result, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, problem.Invalidf("Search for something")
	}
	if base == "" {
		return nil, problem.Unavailablef("Web search isn't set up: an admin adds a SearXNG on the Admin page")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/search?format=json&q="+url.QueryEscape(query), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", problem.Unavailablef("The web search (SearXNG) didn't answer"), err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusForbidden {
		return nil, problem.Unavailablef("SearXNG refused: turn on its JSON format (search.formats: [html, json] in its settings.yml)")
	}
	if response.StatusCode != http.StatusOK {
		return nil, problem.Unavailablef(fmt.Sprintf("SearXNG answered %d", response.StatusCode))
	}
	var answer struct {
		Results []Result `json:"results"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&answer); err != nil {
		return nil, fmt.Errorf("%w: %v", problem.Failedf("SearXNG's answer wasn't JSON"), err)
	}
	if len(answer.Results) > maxResults {
		answer.Results = answer.Results[:maxResults]
	}
	return answer.Results, nil
}

// Format is search results as text for the AI.
func Format(results []Result) string {
	if len(results) == 0 {
		return "No results."
	}
	var b strings.Builder
	for i, r := range results {
		fmt.Fprintf(&b, "%d. %s\n%s\n%s\n\n", i+1, strings.TrimSpace(r.Title), r.URL, strings.TrimSpace(r.Content))
	}
	return strings.TrimSpace(b.String())
}

// Fetch reads a public web page as text. client must only reach the public internet
// (egress.Proxy.Client).
func Fetch(ctx context.Context, client *http.Client, address string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(address))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return "", problem.Invalidf("That isn't an http:// or https:// address")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return "", problem.Invalidf("That isn't an http:// or https:// address")
	}
	request.Header.Set("User-Agent", "Mozilla/5.0 (compatible; daily-dashboard)")
	request.Header.Set("Accept", "text/html,text/plain;q=0.9,*/*;q=0.5")
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("%w: %v", problem.Unavailablef("Couldn't open "+parsed.Host+" (only public web pages can be read)"), err)
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 {
		return "", problem.Failedf(fmt.Sprintf("%s answered %d", parsed.Host, response.StatusCode))
	}
	kind := response.Header.Get("Content-Type")
	if kind != "" && !strings.HasPrefix(kind, "text/") && !strings.Contains(kind, "html") &&
		!strings.Contains(kind, "json") && !strings.Contains(kind, "xml") {
		return "", problem.Invalidf("That page isn't text (" + kind + ")")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxPageBytes))
	if err != nil {
		return "", fmt.Errorf("%w: %v", problem.Failedf("Couldn't read "+parsed.Host), err)
	}
	if !utf8.Valid(body) {
		body = []byte(strings.ToValidUTF8(string(body), ""))
	}
	text := string(body)
	if kind == "" || strings.Contains(kind, "html") {
		text = Text(text)
	}
	if len(text) > maxPageText {
		cut := maxPageText
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut] + "\n\n[The page goes on; this is its first part.]"
	}
	return "Page: " + response.Request.URL.String() + "\n\n" + text, nil
}

var (
	hiddenParts = regexp.MustCompile(`(?is)<(script|style|noscript|svg|template|head)\b.*?</(script|style|noscript|svg|template|head)\s*>|<!--.*?-->`)
	breaks      = regexp.MustCompile(`(?i)<(br|/p|/div|/li|/h[1-6]|/tr|/section|/article|/header|/footer|/ul|/ol|/table|/blockquote)\b[^>]*>`)
	items       = regexp.MustCompile(`(?i)<li\b[^>]*>`)
	tags        = regexp.MustCompile(`(?s)<[^>]*>`)
	spaces      = regexp.MustCompile(`[ \t\r\f\v\x{a0}]+`)
	blankLines  = regexp.MustCompile(`\n\s*\n+`)
)

// Text is an HTML page's readable text: no scripts or styles, one line per block.
func Text(page string) string {
	page = hiddenParts.ReplaceAllString(page, " ")
	page = breaks.ReplaceAllString(page, "\n")
	page = items.ReplaceAllString(page, "\n- ")
	page = tags.ReplaceAllString(page, " ")
	page = html.UnescapeString(page)
	page = spaces.ReplaceAllString(page, " ")
	lines := strings.Split(page, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSpace(line)
	}
	return strings.TrimSpace(blankLines.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}
