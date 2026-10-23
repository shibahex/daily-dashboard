package web

import (
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"

	"dashboard/internal/anki"
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
	Keeping  bool // ...whose encrypted data needs the recovery code to keep
	Locked   bool // sent back here to unlock the data (see UnlockData)
	Upgrade  bool // an account from before proof sign-ins: send the real password once more
	Code     string
	Username string
	Error    string

	ShowRecovery  bool   // show the new recovery code the browser made (only it has it)
	NotesRecovery string // a reset: the notes key wrapped with the recovery code, for the browser
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

// The sign-in pages never send the real password: their script (web/js/keys.js) turns it into a
// proof for the server and, separately, the key to the user's notes, which stays in the browser.
// Signing in, it also offers a new recovery code's proof and a new notes key (wrapped with the
// password and that code), used only when the account doesn't have them yet.

// offered is what a sign-in form brings for the user's keys.
type offered struct {
	proof, recovery string
	notesKey        auth.NotesKey
}

func offeredKeys(r *http.Request) (offered, error) {
	o := offered{proof: r.FormValue("proof"), recovery: r.FormValue("recovery"),
		notesKey: auth.NotesKey{Password: r.FormValue("notes_password"), Recovery: r.FormValue("notes_recovery")}}
	if o.proof == "" {
		return o, problem.Invalidf("Signing in needs JavaScript (it keeps your password in your browser). Turn it on and reload.")
	}
	return o, nil
}

func (ui *UI) createAccount(w http.ResponseWriter, r *http.Request) {
	username := formText(r, "username")
	keys, err := offeredKeys(r)
	session := ""
	if err == nil {
		session, err = ui.Auth.Setup(formText(r, "code"), username, keys.proof, keys.proof)
	}
	if err != nil {
		ui.renderSignIn(w, problem.Status(err), signInView{Setup: true, Code: formText(r, "code"), Username: username, Error: problem.Public(err)})
		return
	}
	ui.signedInWith(w, r, session, func(user auth.User) (bool, error) { return ui.useKeys(user, keys, session) })
}

func (ui *UI) loginPage(w http.ResponseWriter, r *http.Request) {
	switch {
	case ui.Auth.NeedsSetup():
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
	case ui.signedIn(r):
		http.Redirect(w, r, "/", http.StatusSeeOther)
	default:
		ui.renderSignIn(w, http.StatusOK, signInView{Locked: r.FormValue("locked") != ""})
	}
}

func (ui *UI) login(w http.ResponseWriter, r *http.Request) {
	username := formText(r, "username")
	keys, err := offeredKeys(r)
	session := ""
	if err == nil {
		if old := r.FormValue("password"); old != "" {
			session, err = ui.Auth.Upgrade(username, old, keys.proof, httpx.ClientIP(r))
		} else {
			session, err = ui.Auth.Login(username, keys.proof, httpx.ClientIP(r))
		}
	}
	var tooMany auth.TooManyTries
	switch {
	case errors.As(err, &tooMany):
		w.Header().Set("Retry-After", "60")
		ui.renderSignIn(w, http.StatusTooManyRequests, signInView{Username: username, Error: problem.Public(err)})
	case err == auth.ErrUpgrade:
		ui.renderSignIn(w, http.StatusOK, signInView{Username: username, Upgrade: true, Error: problem.Public(err)})
	case err != nil:
		ui.renderSignIn(w, http.StatusUnauthorized, signInView{Username: username, Error: problem.Public(err)})
	default:
		ui.signedInWith(w, r, session, func(user auth.User) (bool, error) { return ui.useKeys(user, keys, session) })
	}
}

// useKeys unlocks (or first encrypts) the user's data with the proof and, if they have none
// yet, keeps the notes key the browser offered. It reports whether the offered recovery code is
// now theirs (so the browser shows it).
func (ui *UI) useKeys(user auth.User, keys offered, session string) (bool, error) {
	created, err := ui.Workspaces.SignIn(user, keys.proof, keys.recovery, session)
	if err != nil || user.NotesKey != nil {
		return created, err
	}
	if err := ui.Auth.SetNotesKey(user.ID, keys.notesKey, true); err != nil {
		return created, err
	}
	if !created {
		// Encrypted before notes were: the offered code becomes the one for both.
		err = ui.Workspaces.SetRecovery(user, keys.recovery)
	}
	return true, err
}

// signedInWith finishes a sign-in: open unlocks the user's data and keys, then it sets the
// session cookie and goes to the dashboard, or to the page showing the new recovery code.
func (ui *UI) signedInWith(w http.ResponseWriter, r *http.Request, session string, open func(auth.User) (bool, error)) {
	user, _ := ui.Auth.Session(session)
	newCode, err := open(user)
	if err != nil {
		ui.Auth.Logout(session)
		ui.renderSignIn(w, problem.Status(err), signInView{Username: user.Username, Error: problem.Public(err)})
		return
	}
	auth.SetSessionCookie(w, session)
	ui.Auth.SetSessionAgent(session, browserName(r.UserAgent()))
	if newCode {
		ui.renderSignIn(w, http.StatusOK, signInView{Username: user.Username, ShowRecovery: true})
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (ui *UI) signedIn(r *http.Request) bool {
	_, ok := ui.Auth.Session(auth.SessionFrom(r))
	return ok
}

// invitePage is where someone sets their password from an admin's invite link.
func (ui *UI) invitePage(w http.ResponseWriter, r *http.Request) {
	code := r.FormValue("code")
	ui.renderSignIn(w, http.StatusOK, ui.inviteView(code, ""))
}

// inviteView is the invite page for code, with an error. A reset of an account with encrypted
// data asks for the recovery code, the only way to keep that data; the page gets the notes key
// wrapped with that code, for the browser to open and wrap again with the new password.
func (ui *UI) inviteView(code, message string) signInView {
	username, reset, ok := ui.Auth.InviteFor(code)
	view := signInView{Invite: true, Reset: reset, Code: code, Username: username, Error: message}
	if !ok && message == "" {
		view.Error = "This invite link is wrong or has expired. Ask your admin for a new one."
	}
	if user, found := ui.resetUser(code); found {
		view.Keeping = ui.Workspaces.HasEncryptedData(user)
		if user.NotesKey != nil {
			view.NotesRecovery = user.NotesKey.Recovery
		}
	}
	return view
}

// resetUser is the account a reset link is for.
func (ui *UI) resetUser(code string) (auth.User, bool) {
	username, reset, ok := ui.Auth.InviteFor(code)
	if !ok || !reset {
		return auth.User{}, false
	}
	for _, user := range ui.Auth.Users() {
		if user.Username == username {
			return user, true
		}
	}
	return auth.User{}, false
}

func (ui *UI) acceptInvite(w http.ResponseWriter, r *http.Request) {
	code := formText(r, "code")
	keys, err := offeredKeys(r)
	if err != nil {
		ui.renderSignIn(w, problem.Status(err), ui.inviteView(code, problem.Public(err)))
		return
	}
	oldRecovery := r.FormValue("old_recovery") // the proof of the recovery code they typed
	// A reset: the data is kept with the recovery code, or deliberately given up. Checked before
	// the password changes, so a mistyped code changes nothing.
	resetting, isReset := ui.resetUser(code)
	keeping := false
	if isReset && ui.Workspaces.HasEncryptedData(resetting) {
		switch {
		case oldRecovery != "":
			if err := ui.Workspaces.CheckRecovery(resetting, oldRecovery); err != nil {
				ui.renderSignIn(w, problem.Status(err), ui.inviteView(code, problem.Public(err)))
				return
			}
			keeping = true
		case r.FormValue("start_over") == "":
			ui.renderSignIn(w, http.StatusBadRequest, ui.inviteView(code,
				"Enter your recovery code to keep your data, or tick the box to start over without it."))
			return
		}
	}
	session, err := ui.Auth.AcceptInvite(code, keys.proof, keys.proof)
	if err != nil {
		ui.renderSignIn(w, problem.Status(err), ui.inviteView(code, problem.Public(err)))
		return
	}
	ui.signedInWith(w, r, session, func(user auth.User) (bool, error) {
		if !isReset {
			return ui.useKeys(user, keys, session)
		}
		startedOver, err := ui.Workspaces.Reset(user, keys.proof, session, oldRecovery, keys.recovery)
		switch {
		case err != nil:
			return false, err
		case keeping && user.NotesKey != nil:
			// The browser opened the notes key with the old code and wrapped it with the new password.
			return false, ui.Auth.SetNotesKeyWrap(user.ID, "password", keys.notesKey.Password)
		case startedOver || user.NotesKey == nil:
			return true, ui.Auth.SetNotesKey(user.ID, keys.notesKey, false)
		}
		return false, nil
	})
}

func (ui *UI) renderSignIn(w http.ResponseWriter, status int, view signInView) {
	html, err := ui.renderToHTML("sign-in", view)
	if err != nil {
		log.Printf("rendering sign-in: %v", err)
		http.Error(w, "couldn't show this page", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	w.Write([]byte(html))
}

// ---------- the Account page ----------

type accountView struct {
	Tab          string // accountTabs: which part of the page shows
	Username     string
	Admin        bool
	Sessions     int
	Devices      []auth.Device
	HasToken     bool
	NewToken     string // shown once, right after it's made
	Encrypted    bool   // the data is encrypted on the server's disk (unless they chose plain files)
	Sealed       bool   // the notes are encrypted end to end, in the browser
	PlainStorage bool   // the user chose not to encrypt their other data on disk
	NewRecovery  bool   // the browser just made a new recovery code: it shows it
	Error        string
	Saved        string
	Connections  connectionsView
}

// connectionsView is the user's own MyFitnessPal, Anki and AI keys.
type connectionsView struct {
	Tab           string
	Admin         bool
	Host          bool     // uses the server computer's own logins
	ServerAI      bool     // the user lets the server run an AI for them
	ServerAIs     []string // the AIs on the server they may pick
	ServerAllowed bool     // their admin allows at least one AI on the server
	ModelServers  []string // model servers on the network their browser may reach (an admin lists them)
	Relayed       []string // those the server relays to without checking certificates
	MyFitnessPal  bool     // signed in
	Anki          anki.Status
	AnkiURL       string
	Error         string
	Saved         string
}

func (ui *UI) accountView(r *http.Request) accountView {
	user := ui.user(r)
	view := accountView{Tab: accountTab(r), Username: user.Username, Admin: user.IsAdmin(), Sessions: ui.Auth.SessionCount(user.ID),
		Devices:  ui.Auth.Devices(user.ID, auth.SessionFrom(r)),
		HasToken: ui.Auth.HasToken(user.ID), Connections: ui.connectionsView(r), Encrypted: ui.Workspaces.Encrypted(user), Sealed: sealedNotes(user), PlainStorage: user.PlainStorage}
	return view
}

func (ui *UI) connectionsView(r *http.Request) connectionsView {
	user, ws := ui.user(r), ui.ws(r)
	ankiClient := ws.Anki()
	view := connectionsView{Tab: accountTab(r), Admin: user.IsAdmin(), Host: ws.Host, MyFitnessPal: ws.MyFitnessPal.Client().Setup().SignedIn,
		Anki: ankiClient.Status(), AnkiURL: ankiClient.URL, ServerAI: user.ServerAI, ServerAIs: serverAILabels(user),
		ModelServers: ui.Auth.ModelServers(), Relayed: ui.Auth.RelayedModelServers()}
	for _, id := range auth.ServerProviders {
		view.ServerAllowed = view.ServerAllowed || user.Policy.AllowsProvider(id)
	}
	return view
}

// accountTabs are the Account page's tabs, by their ?tab= value ("" is the first).
var accountTabs = []struct{ ID, Label string }{
	{"", "Account"}, {"privacy", "Privacy"}, {"connections", "Connections"}, {"ai", "AI"},
}

// accountTab is the tab a request is for: the page's ?tab=, or for a change made on the page,
// the tab of the page it came from.
func accountTab(r *http.Request) string {
	tab := r.URL.Query().Get("tab")
	if tab == "" {
		if from, err := url.Parse(r.Header.Get("HX-Current-URL")); err == nil {
			tab = from.Query().Get("tab")
		}
	}
	for _, t := range accountTabs {
		if t.ID == tab {
			return tab
		}
	}
	return ""
}

// renderConnections answers a change to a connection: the card again, with what happened.
func (ui *UI) renderConnections(w http.ResponseWriter, r *http.Request, saved string, err error) {
	view := ui.connectionsView(r)
	if err != nil {
		view.Error = problem.Public(err)
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

func (ui *UI) accountPage(w http.ResponseWriter, r *http.Request) {
	ui.renderPage(r, w, "account", "Account", "account", ui.accountView(r))
}

func (ui *UI) logout(w http.ResponseWriter, r *http.Request) {
	ui.Auth.Logout(auth.SessionFrom(r))
	ui.Workspaces.Prune(ui.user(r).ID) // locks the data if no other browser or token is left
	auth.ClearSessionCookie(w)
	w.Header().Set("HX-Redirect", "/login")
	nothing(w)
}

// endDevice logs one of the user's other browsers out.
func (ui *UI) endDevice(w http.ResponseWriter, r *http.Request) {
	if err := ui.Auth.EndDevice(ui.user(r).ID, r.FormValue("id")); err != nil {
		fail(w, err)
		return
	}
	ui.Workspaces.Prune(ui.user(r).ID) // its key copy goes too
	announce(w, say("That browser is logged out"))
	ui.render(w, "account-card", ui.accountView(r))
}

// browserName is a short name for a browser from its User-Agent ("Firefox on Linux").
func browserName(agent string) string {
	name := "A browser"
	for _, b := range []struct{ token, name string }{
		{"Edg/", "Edge"}, {"OPR/", "Opera"}, {"Firefox/", "Firefox"}, {"Chrome/", "Chrome"}, {"Safari/", "Safari"},
	} {
		if strings.Contains(agent, b.token) {
			name = b.name
			break
		}
	}
	for _, os := range []struct{ token, name string }{
		{"Android", "Android"}, {"iPhone", "iPhone"}, {"iPad", "iPad"}, {"Windows", "Windows"},
		{"Mac OS X", "macOS"}, {"CrOS", "ChromeOS"}, {"Linux", "Linux"},
	} {
		if strings.Contains(agent, os.token) {
			return name + " on " + os.name
		}
	}
	return name
}

func (ui *UI) logoutOthers(w http.ResponseWriter, r *http.Request) {
	if err := ui.Auth.LogoutOthers(ui.user(r).ID, auth.SessionFrom(r)); err != nil {
		fail(w, err)
		return
	}
	ui.Workspaces.Prune(ui.user(r).ID)
	announce(w, say("Logged out everywhere else"))
	ui.render(w, "account-card", ui.accountView(r))
}

func (ui *UI) changePassword(w http.ResponseWriter, r *http.Request) {
	view := ui.accountView(r)
	user := ui.user(r)
	// Proofs, from the page's script; with sealed notes, also the notes key wrapped with the new
	// password (the browser opened it with the old one).
	current, proof, notesWrap := r.FormValue("current"), r.FormValue("password"), r.FormValue("notes_password")
	var err error
	switch {
	case current == "" || proof == "":
		err = problem.Invalidf("Changing your password needs JavaScript. Turn it on and reload.")
	case sealedNotes(user) && notesWrap == "":
		err = problem.Invalidf("Your browser couldn't open your notes key with your current password. Check it and try again.")
	default:
		err = ui.Auth.ChangePassword(user.ID, auth.SessionFrom(r), current, proof, proof)
	}
	if err == nil {
		// The data key and the notes key follow the new password (the old one no longer opens them).
		err = ui.Workspaces.ChangePassword(user, proof)
		if err == nil && sealedNotes(user) {
			err = ui.Auth.SetNotesKeyWrap(user.ID, "password", notesWrap)
		}
		if err != nil {
			err = problem.Unavailablef("Your password changed, but your data still opens with the old one: " + problem.Public(err) +
				". Change it again, or use your recovery code if a sign-in fails.")
		}
	}
	if err != nil {
		view.Error = problem.Public(err)
		ui.render(w, "account-card", view)
		return
	}
	ui.Workspaces.Prune(user.ID)
	announce(w, say("Password changed"))
	view = ui.accountView(r)
	view.Saved = "Password changed. Every other browser was logged out."
	ui.render(w, "account-card", view)
}

func (ui *UI) makeToken(w http.ResponseWriter, r *http.Request) {
	token, err := ui.Auth.NewToken(ui.user(r).ID)
	if err == nil {
		err = ui.Workspaces.SetToken(ui.user(r), token)
	}
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
	ui.Workspaces.Prune(ui.user(r).ID)
	announce(w, say("Token removed: the bar plugin can't connect until it has a new one"))
	ui.render(w, "account-card", ui.accountView(r))
}

// newRecoveryCode replaces the recovery code (the old one stops working) and shows the new one.
func (ui *UI) newRecoveryCode(w http.ResponseWriter, r *http.Request) {
	// The browser made the code: it sends its proof and the notes key wrapped with it.
	user := ui.user(r)
	recovery, notesWrap := r.FormValue("recovery"), r.FormValue("notes_recovery")
	var err error
	switch {
	case recovery == "" || (sealedNotes(user) && notesWrap == ""):
		err = problem.Invalidf("Making a recovery code needs your notes unlocked in this browser: open the Docs tab first")
	case ui.Workspaces.Encrypted(user):
		err = ui.Workspaces.SetRecovery(user, recovery)
	}
	if err == nil && sealedNotes(user) {
		err = ui.Auth.SetNotesKeyWrap(user.ID, "recovery", notesWrap)
	}
	if err != nil {
		fail(w, err)
		return
	}
	view := ui.accountView(r)
	view.NewRecovery = true
	ui.render(w, "account-card", view)
}

// stopEncrypting keeps the user's data (not their notes) as plain files on the server's disk.
func (ui *UI) stopEncrypting(w http.ResponseWriter, r *http.Request) {
	user := ui.user(r)
	err := ui.Workspaces.Decrypt(user)
	if err == nil {
		err = ui.Auth.SetPlainStorage(user.ID, true)
	}
	if err != nil {
		fail(w, err)
		return
	}
	announce(w, say("Your data is now kept as plain files on the server"))
	view := ui.accountView(r)
	view.Encrypted, view.PlainStorage = false, true
	ui.render(w, "account-card", view)
}

// startEncrypting encrypts the user's data on the server's disk again: the browser sends the
// password's proof, a new recovery code's proof and the notes key wrapped with that code.
func (ui *UI) startEncrypting(w http.ResponseWriter, r *http.Request) {
	user := ui.user(r)
	proof, recovery, notesWrap := r.FormValue("proof"), r.FormValue("recovery"), r.FormValue("notes_recovery")
	var err error
	switch {
	case proof == "" || recovery == "" || notesWrap == "":
		err = problem.Invalidf("Encrypting needs JavaScript. Turn it on and reload.")
	case !ui.Auth.CheckPassword(user.ID, proof):
		err = problem.Invalidf("That password is wrong")
	default:
		err = ui.Workspaces.EncryptNow(user, proof, recovery, auth.SessionFrom(r))
	}
	if err == nil {
		err = ui.Auth.SetNotesKeyWrap(user.ID, "recovery", notesWrap)
	}
	if err == nil {
		err = ui.Auth.SetPlainStorage(user.ID, false)
	}
	if err != nil {
		fail(w, err)
		return
	}
	announce(w, say("Your data is encrypted on the server's disk again"))
	view := ui.accountView(r)
	view.Encrypted, view.PlainStorage, view.NewRecovery = true, false, true
	ui.render(w, "account-card", view)
}
