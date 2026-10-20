package web

import (
	"errors"
	"net/http"

	"dashboard/internal/auth"
	"dashboard/internal/problem"
)

// The setup and login pages are the only ones you can see signed out (see auth.Guard). They're
// plain forms, outside the page frame. The Account page is a normal tab page.

type signInView struct {
	Setup    bool // creating the account, not logging in
	Code     string
	Username string
	Error    string
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
	case ui.Auth.ValidSession(auth.SessionFrom(r)):
		http.Redirect(w, r, "/", http.StatusSeeOther)
	default:
		ui.renderSignIn(w, http.StatusOK, signInView{})
	}
}

func (ui *UI) login(w http.ResponseWriter, r *http.Request) {
	username := formText(r, "username")
	session, err := ui.Auth.Login(username, r.FormValue("password"))
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
	Username string
	Sessions int
	HasToken bool
	NewToken string // shown once, right after it's made
	Error    string
	Saved    string
}

func (ui *UI) accountView() accountView {
	return accountView{Username: ui.Auth.Username(), Sessions: ui.Auth.SessionCount(), HasToken: ui.Auth.HasToken()}
}

func (ui *UI) accountPage(w http.ResponseWriter, r *http.Request) {
	ui.renderPage(w, "account", "Account", "account", ui.accountView())
}

func (ui *UI) logout(w http.ResponseWriter, r *http.Request) {
	ui.Auth.Logout(auth.SessionFrom(r))
	auth.ClearSessionCookie(w)
	w.Header().Set("HX-Redirect", "/login")
	nothing(w)
}

func (ui *UI) logoutOthers(w http.ResponseWriter, r *http.Request) {
	if err := ui.Auth.LogoutOthers(auth.SessionFrom(r)); err != nil {
		fail(w, err)
		return
	}
	announce(w, say("Logged out everywhere else"))
	ui.render(w, "account-card", ui.accountView())
}

func (ui *UI) changePassword(w http.ResponseWriter, r *http.Request) {
	view := ui.accountView()
	err := ui.Auth.ChangePassword(auth.SessionFrom(r), r.FormValue("current"), r.FormValue("password"), r.FormValue("confirm"))
	if err != nil {
		view.Error = err.Error()
		ui.render(w, "account-card", view)
		return
	}
	announce(w, say("Password changed"))
	view = ui.accountView()
	view.Saved = "Password changed. Every other browser was logged out."
	ui.render(w, "account-card", view)
}

func (ui *UI) makeToken(w http.ResponseWriter, r *http.Request) {
	token, err := ui.Auth.NewToken()
	if err != nil {
		fail(w, err)
		return
	}
	view := ui.accountView()
	view.NewToken = token
	ui.render(w, "account-card", view)
}

func (ui *UI) removeToken(w http.ResponseWriter, r *http.Request) {
	if err := ui.Auth.RemoveToken(); err != nil {
		fail(w, err)
		return
	}
	announce(w, say("Token removed: the bar plugin can't connect until it has a new one"))
	ui.render(w, "account-card", ui.accountView())
}
