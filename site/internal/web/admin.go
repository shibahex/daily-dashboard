package web

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"dashboard/internal/auth"
	"dashboard/internal/problem"
)

// The Admin page: invite people, reset their passwords, turn accounts off, make admins, and
// choose what each person's AI agent may use. Only admins see it (auth.RequireAdmin); admins
// see accounts and settings, never anyone's food, notes, tasks or chats.

type adminView struct {
	Me             string // the signed-in admin's id
	Users          []auth.User
	Encrypted      map[string]bool // users whose data is encrypted on disk
	Invites        []auth.PendingInvite
	Default        auth.Policy
	ModelServers   []string // origins the browser AI may reach besides each person's own computer
	SkipModelCerts bool     // the model relay is on (modelrelay.go)
	SearchURL      string   // the SearXNG local models search with (webtools.go)
	Providers      []choice
	MCPs           []choice
	Link           string // an invite or reset link, shown once
	LinkFor        string
	Error          string
	Saved          string
}

func (ui *UI) adminView(r *http.Request) adminView {
	view := adminView{Me: ui.user(r).ID, Users: ui.Auth.Users(), Invites: ui.Auth.Invites(), Default: ui.Auth.DefaultPolicy(),
		ModelServers: ui.Auth.ModelServers(), SkipModelCerts: ui.Auth.SkipModelCerts(), SearchURL: ui.Auth.SearchURL()}
	view.Encrypted = map[string]bool{}
	for _, user := range view.Users {
		view.Encrypted[user.ID] = ui.Workspaces.Encrypted(user)
	}
	for _, id := range auth.KnownProviders {
		view.Providers = append(view.Providers, choice{ID: id, Label: providerLabels[id]})
	}
	for _, id := range auth.KnownMCPs {
		view.MCPs = append(view.MCPs, choice{ID: id, Label: mcpLabels[id]})
	}
	return view
}

var mcpLabels = map[string]string{"myfitnesspal": "MyFitnessPal", "anki": "Anki", "taskwarrior": "Taskwarrior"}

// providerLabels name the AIs: the browser runs the first two, the server the rest.
var providerLabels = map[string]string{"claude": "Claude", "local": "Local model (on their computer, or a model server below)",
	"claude-code": "Claude Code (on the server)", "codex": "Codex (on the server)", "gemini": "Gemini CLI (on the server)"}

func (ui *UI) adminPage(w http.ResponseWriter, r *http.Request) {
	ui.renderPage(r, w, "admin", "Admin", "admin", ui.adminView(r))
}

// renderAdmin answers an admin change: the card again, with what happened.
func (ui *UI) renderAdmin(w http.ResponseWriter, r *http.Request, saved string, err error, edit func(*adminView)) {
	view := ui.adminView(r)
	if err != nil {
		view.Error = problem.Public(err)
	} else {
		view.Saved = saved
		if saved != "" {
			announce(w, say(saved))
		}
		if edit != nil {
			edit(&view)
		}
	}
	ui.render(w, "admin-card", view)
}

// inviteLink is at the address the admin is using (the server only answers at allowed ones).
func (ui *UI) inviteLink(r *http.Request, code string) string {
	host := r.Host
	if name, _, err := net.SplitHostPort(r.Host); err == nil {
		host = name
	}
	scheme := "http"
	if ui.HTTPS && host != "localhost" && host != "127.0.0.1" {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/invite?code=" + url.QueryEscape(code)
}

func (ui *UI) invite(w http.ResponseWriter, r *http.Request) {
	username := formText(r, "username")
	code, err := ui.Auth.Invite(username, auth.Role(r.FormValue("role")))
	ui.renderAdmin(w, r, "Invite made for "+username, err, func(v *adminView) {
		v.Link, v.LinkFor = ui.inviteLink(r, code), username
	})
}

func (ui *UI) cancelInvite(w http.ResponseWriter, r *http.Request) {
	ui.renderAdmin(w, r, "Invite cancelled", ui.Auth.CancelInvite(r.FormValue("username")), nil)
}

// adminTarget is the account an admin change is about.
func (ui *UI) adminTarget(r *http.Request) (auth.User, error) {
	user, ok := ui.Auth.User(r.PathValue("id"))
	if !ok {
		return auth.User{}, problem.NotFoundf("No such user")
	}
	return user, nil
}

func (ui *UI) resetUserPassword(w http.ResponseWriter, r *http.Request) {
	target, err := ui.adminTarget(r)
	if err != nil {
		ui.renderAdmin(w, r, "", err, nil)
		return
	}
	code, err := ui.Auth.ResetPassword(target.ID)
	ui.renderAdmin(w, r, "Reset link made for "+target.Username, err, func(v *adminView) {
		v.Link, v.LinkFor = ui.inviteLink(r, code), target.Username
	})
}

func (ui *UI) setUserDisabled(disabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		target, err := ui.adminTarget(r)
		if err == nil {
			err = ui.Auth.SetDisabled(ui.user(r).ID, target.ID, disabled)
		}
		if err == nil && disabled {
			ui.Workspaces.Prune(target.ID)
			ui.Workspaces.Lock(target.ID)
		}
		saved := target.Username + " turned back on"
		if disabled {
			saved = target.Username + " turned off and logged out"
		}
		ui.renderAdmin(w, r, saved, err, nil)
	}
}

func (ui *UI) setUserRole(w http.ResponseWriter, r *http.Request) {
	target, err := ui.adminTarget(r)
	role := auth.Role(r.FormValue("role"))
	if err == nil {
		err = ui.Auth.SetRole(ui.user(r).ID, target.ID, role)
	}
	saved := target.Username + " is now a regular user"
	if role == auth.Admin {
		saved = target.Username + " is now an admin"
	}
	ui.renderAdmin(w, r, saved, err, nil)
}

// deleteUser removes the account and everything in its folder.
func (ui *UI) deleteUser(w http.ResponseWriter, r *http.Request) {
	target, err := ui.adminTarget(r)
	if err == nil && r.FormValue("confirm") != target.Username {
		err = problem.Invalidf("Type the username to confirm")
	}
	if err == nil {
		err = ui.Auth.DeleteUser(ui.user(r).ID, target.ID)
	}
	if err == nil {
		err = ui.Workspaces.Remove(target.ID)
	}
	ui.renderAdmin(w, r, target.Username+" and all their data deleted", err, nil)
}

// policyFrom reads the agent policy checkboxes.
func policyFrom(r *http.Request) auth.Policy {
	r.ParseForm()
	return auth.Policy{
		Providers: r.Form["providers"], MCPs: r.Form["mcps"],
		EditFiles: r.FormValue("edit_files") == "on",
		NoWeb:     r.FormValue("web") != "on",
	}.Clean()
}

func (ui *UI) setUserPolicy(w http.ResponseWriter, r *http.Request) {
	target, err := ui.adminTarget(r)
	if err == nil {
		err = ui.Auth.SetPolicy(target.ID, policyFrom(r))
	}
	ui.renderAdmin(w, r, "Saved what "+target.Username+"'s AI can use", err, nil)
}

func (ui *UI) setDefaultPolicy(w http.ResponseWriter, r *http.Request) {
	ui.renderAdmin(w, r, "Saved the settings for new users", ui.Auth.SetDefaultPolicy(policyFrom(r)), nil)
}

func expiresIn(t time.Time) string {
	days := int(time.Until(t).Hours() / 24)
	switch {
	case days >= 2:
		return fmt.Sprintf("expires in %d days", days)
	case time.Until(t) > time.Hour:
		hours := int(time.Until(t).Hours())
		return fmt.Sprintf("expires in %d %s", hours, plural(hours, "hour", "hours"))
	}
	return "expires soon"
}

// setModelServers saves the model servers on the network that browsers may reach, one per line.
func (ui *UI) setModelServers(w http.ResponseWriter, r *http.Request) {
	err := ui.Auth.SetModelServers(strings.Split(r.FormValue("model_servers"), "\n"))
	ui.renderAdmin(w, r, "Saved the model servers", err, nil)
}

// setSearch saves the SearXNG that local models search with ("" = none).
func (ui *UI) setSearch(w http.ResponseWriter, r *http.Request) {
	ui.renderAdmin(w, r, "Saved the web search", ui.Auth.SetSearchURL(r.FormValue("search_url")), nil)
}

// setSkipModelCerts turns the model relay on or off (modelrelay.go).
func (ui *UI) setSkipModelCerts(w http.ResponseWriter, r *http.Request) {
	skip := r.FormValue("skip") == "on"
	saved := "Certificate checks are back on"
	if skip {
		saved = "Certificate checks are off for the https model servers"
	}
	ui.renderAdmin(w, r, saved, ui.Auth.SetSkipModelCerts(skip), nil)
}
