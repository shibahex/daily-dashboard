package web

import (
	"errors"
	"net/http"

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
	Username string
	Admin    bool
	Sessions int
	HasToken bool
	NewToken string // shown once, right after it's made
	Error    string
	Saved    string
}

func (ui *UI) accountView(r *http.Request) accountView {
	user := ui.user(r)
	return accountView{Username: user.Username, Admin: user.IsAdmin(), Sessions: ui.Auth.SessionCount(user.ID), HasToken: ui.Auth.HasToken(user.ID)}
}

func (ui *UI) accountPage(w http.ResponseWriter, r *http.Request) {
	ui.renderPage(w, "account", "Account", "account", ui.accountView(r))
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
