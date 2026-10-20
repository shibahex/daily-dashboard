package web

import (
	"errors"
	"net/http"
	"strings"

	"dashboard/internal/anki"
	"dashboard/internal/assistant"
	"dashboard/internal/auth"
	"dashboard/internal/httpx"
	"dashboard/internal/problem"
)

// The setup, login and invite pages are the only ones you can see signed out (see auth.Guard).
// They're plain forms, outside the page frame. The Account page is a normal tab page.

type signInView struct {
	Setup    bool // creating the first account, not logging in
	Invite   bool // setting a password from an invite link
	Reset    bool // ...for an existing account
	Code     string
	Username string
	Error    string
}

// user is the signed-in user. Every page and change goes through auth.Guard, which sets it.
func (ui *UI) user(r *http.Request) auth.User {
	user, _ := auth.UserFrom(r)
	return user
}

func (ui *UI) setupPage(w http.ResponseWriter, r *http.Request) {
	if !ui.Auth.NeedsSetup() {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	ui.renderSignIn(w, http.StatusOK, signInView{Setup: true, Code: r.FormValue("code")})
}

func (ui *UI) createAccount(w http.ResponseWriter, r *http.Request) {
	username := formText(r, "username")
	session, err := ui.Auth.Setup(formText(r, "code"), username, r.FormValue("password"), r.FormValue("confirm"))
	if err != nil {
		ui.renderSignIn(w, problem.Status(err), signInView{Setup: true, Code: formText(r, "code"), Username: username, Error: err.Error()})
		return
	}
	auth.SetSessionCookie(w, session)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (ui *UI) loginPage(w http.ResponseWriter, r *http.Request) {
	switch {
	case ui.Auth.NeedsSetup():
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
	case ui.signedIn(r):
		http.Redirect(w, r, "/", http.StatusSeeOther)
	default:
		ui.renderSignIn(w, http.StatusOK, signInView{})
	}
}

func (ui *UI) login(w http.ResponseWriter, r *http.Request) {
	username := formText(r, "username")
	session, err := ui.Auth.Login(username, r.FormValue("password"), httpx.ClientIP(r))
	var tooMany auth.TooManyTries
	switch {
	case errors.As(err, &tooMany):
		w.Header().Set("Retry-After", "60")
		ui.renderSignIn(w, http.StatusTooManyRequests, signInView{Username: username, Error: err.Error()})
	case err != nil:
		ui.renderSignIn(w, http.StatusUnauthorized, signInView{Username: username, Error: err.Error()})
	default:
		auth.SetSessionCookie(w, session)
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

func (ui *UI) signedIn(r *http.Request) bool {
	_, ok := ui.Auth.Session(auth.SessionFrom(r))
	return ok
}

// invitePage is where someone sets their password from an admin's invite link.
func (ui *UI) invitePage(w http.ResponseWriter, r *http.Request) {
	code := r.FormValue("code")
	username, reset, ok := ui.Auth.InviteFor(code)
	view := signInView{Invite: true, Reset: reset, Code: code, Username: username}
	if !ok {
		view.Error = "This invite link is wrong or has expired. Ask your admin for a new one."
	}
	ui.renderSignIn(w, http.StatusOK, view)
}

func (ui *UI) acceptInvite(w http.ResponseWriter, r *http.Request) {
	code := formText(r, "code")
	session, err := ui.Auth.AcceptInvite(code, r.FormValue("password"), r.FormValue("confirm"))
	if err != nil {
		username, reset, _ := ui.Auth.InviteFor(code)
		ui.renderSignIn(w, problem.Status(err), signInView{Invite: true, Reset: reset, Code: code, Username: username, Error: err.Error()})
		return
	}
	auth.SetSessionCookie(w, session)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (ui *UI) renderSignIn(w http.ResponseWriter, status int, view signInView) {
	html, err := ui.renderToHTML("sign-in", view)
	if err != nil {
		http.Error(w, "couldn't show this page: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	w.Write([]byte(html))
}

// ---------- the Account page ----------

type accountView struct {
	Username    string
	Admin       bool
	Sessions    int
	HasToken    bool
	NewToken    string // shown once, right after it's made
	Error       string
	Saved       string
	Connections connectionsView
}

// connectionsView is the user's own MyFitnessPal, Anki and AI keys.
type connectionsView struct {
	Host         bool // uses the server computer's own logins
	MyFitnessPal bool // signed in
	Anki         anki.Status
	AnkiURL      string
	Keys         []keyChoice
	Error        string
	Saved        string
}

type keyChoice struct {
	Provider, Label, Where, Hint string
	Set                          bool
}

func (ui *UI) accountView(r *http.Request) accountView {
	user := ui.user(r)
	return accountView{Username: user.Username, Admin: user.IsAdmin(), Sessions: ui.Auth.SessionCount(user.ID),
		HasToken: ui.Auth.HasToken(user.ID), Connections: ui.connectionsView(r)}
}

func (ui *UI) connectionsView(r *http.Request) connectionsView {
	ws := ui.ws(r)
	ankiClient := ws.Anki()
	view := connectionsView{Host: ws.Host, MyFitnessPal: ws.MyFitnessPal.Client().Setup().SignedIn,
		Anki: ankiClient.Status(), AnkiURL: ankiClient.URL}
	for _, provider := range auth.KnownProviders {
		key := assistant.APIKeys[provider]
		view.Keys = append(view.Keys, keyChoice{Provider: provider, Label: key.Label, Where: key.Where, Hint: key.Hint, Set: ws.Secrets.Has(key.Name)})
	}
	return view
}

// renderConnections answers a change to a connection: the card again, with what happened.
func (ui *UI) renderConnections(w http.ResponseWriter, r *http.Request, saved string, err error) {
	view := ui.connectionsView(r)
	if err != nil {
		view.Error = err.Error()
	} else {
		view.Saved = saved
		announce(w, say(saved))
	}
	ui.render(w, "connections-card", view)
}

func (ui *UI) connectMyFitnessPal(w http.ResponseWriter, r *http.Request) {
	ws := ui.ws(r)
	err := ws.MyFitnessPal.Client().Connect(r.FormValue("token"))
	if err == nil {
		go ws.MyFitnessPal.Sync()
	}
	ui.renderConnections(w, r, "MyFitnessPal connected", err)
}

func (ui *UI) disconnectMyFitnessPal(w http.ResponseWriter, r *http.Request) {
	ui.renderConnections(w, r, "MyFitnessPal disconnected", ui.ws(r).MyFitnessPal.Client().Disconnect())
}

func (ui *UI) connectAnki(w http.ResponseWriter, r *http.Request) {
	err := ui.Workspaces.ConnectAnki(ui.user(r), formText(r, "url"), strings.TrimSpace(r.FormValue("key")))
	ui.renderConnections(w, r, "Anki connected", err)
}

func (ui *UI) disconnectAnki(w http.ResponseWriter, r *http.Request) {
	ui.renderConnections(w, r, "Anki disconnected", ui.Workspaces.DisconnectAnki(ui.user(r)))
}

// saveAPIKey stores (or, with an empty key, removes) the user's key for one AI provider.
func (ui *UI) saveAPIKey(w http.ResponseWriter, r *http.Request) {
	key, known := assistant.APIKeys[r.FormValue("provider")]
	value := strings.TrimSpace(r.FormValue("key"))
	switch {
	case !known:
		ui.renderConnections(w, r, "", problem.Invalidf("Pick a provider"))
	case len(value) > 500 || strings.ContainsAny(value, " \t\r\n"):
		ui.renderConnections(w, r, "", problem.Invalidf("That doesn't look like an API key"))
	case value == "":
		ui.renderConnections(w, r, key.Label+" key removed", ui.ws(r).Secrets.Set(key.Name, ""))
	default:
		ui.renderConnections(w, r, key.Label+" key saved", ui.ws(r).Secrets.Set(key.Name, value))
	}
}

func (ui *UI) accountPage(w http.ResponseWriter, r *http.Request) {
	ui.renderPage(r, w, "account", "Account", "account", ui.accountView(r))
}

func (ui *UI) logout(w http.ResponseWriter, r *http.Request) {
	ui.Auth.Logout(auth.SessionFrom(r))
	auth.ClearSessionCookie(w)
	w.Header().Set("HX-Redirect", "/login")
	nothing(w)
}

func (ui *UI) logoutOthers(w http.ResponseWriter, r *http.Request) {
	if err := ui.Auth.LogoutOthers(ui.user(r).ID, auth.SessionFrom(r)); err != nil {
		fail(w, err)
		return
	}
	announce(w, say("Logged out everywhere else"))
	ui.render(w, "account-card", ui.accountView(r))
}

func (ui *UI) changePassword(w http.ResponseWriter, r *http.Request) {
	view := ui.accountView(r)
	err := ui.Auth.ChangePassword(ui.user(r).ID, auth.SessionFrom(r), r.FormValue("current"), r.FormValue("password"), r.FormValue("confirm"))
	if err != nil {
		view.Error = err.Error()
		ui.render(w, "account-card", view)
		return
	}
	announce(w, say("Password changed"))
	view = ui.accountView(r)
	view.Saved = "Password changed. Every other browser was logged out."
	ui.render(w, "account-card", view)
}

func (ui *UI) makeToken(w http.ResponseWriter, r *http.Request) {
	token, err := ui.Auth.NewToken(ui.user(r).ID)
	if err != nil {
		fail(w, err)
		return
	}
	view := ui.accountView(r)
	view.NewToken = token
	ui.render(w, "account-card", view)
}

func (ui *UI) removeToken(w http.ResponseWriter, r *http.Request) {
	if err := ui.Auth.RemoveToken(ui.user(r).ID); err != nil {
		fail(w, err)
		return
	}
	announce(w, say("Token removed: the bar plugin can't connect until it has a new one"))
	ui.render(w, "account-card", ui.accountView(r))
}
