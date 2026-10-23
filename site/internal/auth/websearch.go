package auth

import (
	"net/url"
	"strings"

	"dashboard/internal/problem"
)

// SearchURL is the SearXNG an admin set for the AI's web search ("" = none): the search for
// local models and the server's AIs that have none of their own. Claude in the browser uses
// Anthropic's.
func (a *Auth) SearchURL() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state.SearchURL
}

// SetSearchURL saves the SearXNG's address ("" turns the search off). It may be on the local
// network: an admin chose it, and only the server asks it.
func (a *Auth) SetSearchURL(typed string) error {
	typed = strings.TrimSpace(typed)
	if typed != "" {
		if !strings.Contains(typed, "://") {
			typed = "http://" + typed
		}
		parsed, err := url.Parse(typed)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
			parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return problem.Invalidf("That isn't an http:// or https:// address, like http://searxng:8080")
		}
		parsed.Path = strings.TrimSuffix(strings.TrimRight(parsed.Path, "/"), "/search")
		typed = parsed.String()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state.SearchURL = typed
	return a.save()
}
