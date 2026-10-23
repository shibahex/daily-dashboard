// Package web is the dashboard's pages. They're rendered on the server with html/template and
// updated with HTMX: every card and list has its own URL, and a change answers with the new
// HTML plus events (HX-Trigger) that tell other cards to reload themselves.
package web

import (
	"errors"
	"net/http"

	"dashboard/internal/auth"
	"dashboard/internal/config"
	"dashboard/internal/egress"
	"dashboard/internal/httpx"
	"dashboard/internal/problem"
	"dashboard/internal/sandbox"
	"dashboard/internal/workspace"
)

// UI holds what the pages read and change.
type UI struct {
	Auth       *auth.Auth
	Paths      config.Paths // the server's: templates and static files
	Workspaces *workspace.Manager
	BaseURL    string // the address people use, for invite links

	// The sandbox the AI's MCP tools run in (SandboxErr says why there's none: then the tools
	// refuse to start), the MCP programs, and their only way to the network.
	Sandbox    sandbox.Tools
	SandboxErr error
	Programs   *sandbox.Programs
	Egress     *egress.Proxy

	templates *templateSet
}

// ws is the signed-in user's workspace: their own plan, notes, tasks, food, Anki and chat.
// Every page and change reads and writes through it, never another user's.
func (ui *UI) ws(r *http.Request) *workspace.Workspace { return ui.Workspaces.For(ui.user(r)) }

// Routes registers every page, card and form. Changes are POSTs that must come from HTMX
// (it sends HX-Request, which another website's form can't).
func (ui *UI) Routes(mux *http.ServeMux) {
	ui.templates = newTemplateSet(ui.Paths.Web)
	page := func(pattern string, handler http.HandlerFunc) { mux.HandleFunc("GET "+pattern, handler) }
	change := func(pattern string, handler http.HandlerFunc) { mux.HandleFunc("POST "+pattern, fromHTMX(handler)) }

	mux.Handle("GET /static/", http.StripPrefix("/static/", staticFiles(ui.Paths.Web)))

	// Account: setup and login are plain forms (signed out, outside the page frame).
	page("/setup", ui.setupPage)
	mux.HandleFunc("POST /setup", ui.createAccount)
	page("/login", ui.loginPage)
	mux.HandleFunc("POST /login", ui.login)
	page("/invite", ui.invitePage)
	mux.HandleFunc("POST /invite", ui.acceptInvite)
	page("/account", ui.accountPage)
	change("/account/logout", ui.logout)
	change("/account/logout-others", ui.logoutOthers)
	change("/account/devices/end", ui.endDevice)
	change("/account/password", ui.changePassword)
	change("/account/token", ui.makeToken)
	change("/account/token/remove", ui.removeToken)
	change("/account/recovery", ui.newRecoveryCode)
	change("/account/storage/plain", ui.stopEncrypting)
	change("/account/storage/encrypt", ui.startEncrypting)
	change("/account/myfitnesspal", ui.connectMyFitnessPal)
	change("/account/myfitnesspal/remove", ui.disconnectMyFitnessPal)
	change("/account/anki", ui.connectAnki)
	change("/account/anki/remove", ui.disconnectAnki)
	page("/account/export", ui.exportData)

	// Admin: accounts and what each person's AI may use. Admins only.
	admin := func(pattern string, handler http.HandlerFunc) { change(pattern, auth.RequireAdmin(handler)) }
	page("/admin", auth.RequireAdmin(ui.adminPage))
	admin("/admin/invite", ui.invite)
	admin("/admin/invites/cancel", ui.cancelInvite)
	admin("/admin/default-policy", ui.setDefaultPolicy)
	admin("/admin/model-servers", ui.setModelServers)
	admin("/admin/model-servers/skip-certs", ui.setSkipModelCerts)
	admin("/admin/users/{id}/reset", ui.resetUserPassword)
	admin("/admin/users/{id}/disable", ui.setUserDisabled(true))
	admin("/admin/users/{id}/enable", ui.setUserDisabled(false))
	admin("/admin/users/{id}/role", ui.setUserRole)
	admin("/admin/users/{id}/delete", ui.deleteUser)
	admin("/admin/users/{id}/policy", ui.setUserPolicy)

	// Today
	page("/{$}", ui.todayPage)
	page("/streak", ui.streakBadge)
	page("/cards/session", ui.sessionCard)
	change("/cards/session", ui.saveSession)
	page("/cards/checklist", ui.checklistCard)
	change("/cards/checklist", ui.saveChecklistItems)
	change("/checklist/set", ui.setChecklistItem)
	page("/cards/weight", ui.weightCard)
	change("/cards/weight", ui.saveWeightPlan)
	change("/weight", ui.logWeight)
	page("/cards/goals", ui.goalsCard)
	change("/cards/goals", ui.saveGoals)
	page("/cards/week", ui.weekCard)
	change("/cards/week", ui.saveWeek)
	change("/cards/week/sessions", ui.addSession)
	page("/cards/notes", ui.notesCard)
	change("/cards/notes", ui.saveNotes)
	page("/cards/tasks", ui.dueTasksCard)
	page("/cards/time", ui.timeCard)
	page("/cards/anki", ui.ankiTodayCard)
	change("/layout", ui.saveLayout)
	change("/layout/reset", ui.resetLayout)

	// Nutrition (MyFitnessPal)
	change("/nutrition/sync", ui.syncNutrition)
	page("/food/search", ui.foodSearchForm)
	change("/food/search", ui.searchFood)
	change("/food/pick", ui.pickFood)
	change("/food/preview", ui.previewFood)
	change("/food/add", ui.addFood)
	page("/food/{entry}/edit", ui.editFoodForm)
	change("/food/{entry}", ui.saveFood)
	change("/food/{entry}/delete", ui.deleteFood)
	page("/panel/close", ui.closePanel)

	// Tasks
	page("/tasks", ui.tasksPage)
	page("/tasks/list", ui.taskList)
	change("/tasks", ui.addTask)
	change("/tasks/undo", ui.undoTaskChange)
	page("/tasks/{uuid}/editor", ui.taskEditor)
	change("/tasks/{uuid}", ui.saveTask)
	change("/tasks/{uuid}/notes", ui.addTaskNote)
	change("/tasks/{uuid}/notes/remove", ui.removeTaskNote)
	change("/tasks/{uuid}/start", ui.startTask)
	change("/tasks/{uuid}/stop", ui.stopTask)
	change("/tasks/{uuid}/reopen", ui.reopenTask)
	page("/tasks/{uuid}/done", ui.donePanel)
	change("/tasks/{uuid}/done", ui.completeTask)
	page("/tasks/{uuid}/done-on", ui.doneOnPanel)
	change("/tasks/{uuid}/done-on", ui.completeTaskOn)
	page("/tasks/{uuid}/skip", ui.skipPanel)
	change("/tasks/skip", ui.skipTasks)
	page("/tasks/{uuid}/catch-up", ui.catchUpPanel)
	change("/tasks/{uuid}/catch-up/done", ui.catchUpDone)
	change("/tasks/{uuid}/catch-up/skip", ui.catchUpSkip)
	change("/tasks/catch-up/skip-rest", ui.catchUpSkipRest)
	change("/tasks/catch-up/close", ui.catchUpClose)
	change("/tasks/{uuid}/repeat", ui.repeatForm)
	change("/tasks/{uuid}/schedule", ui.saveSchedule)
	change("/tasks/{uuid}/schedule/stop", ui.stopSchedule)
	page("/reasons/edit", ui.reasonForm)
	page("/reasons", ui.reasonButton)
	change("/reasons", ui.saveReason)

	// Lifting
	page("/lifting", ui.liftingPage)
	page("/lifting/workout", ui.workoutCard)
	change("/lifting/workout", ui.changeWorkout)
	change("/lifting/start", ui.startWorkout)
	page("/lifting/finish", ui.finishPanel)
	change("/lifting/finish", ui.finishWorkout)
	change("/lifting/discard", ui.discardWorkout)
	page("/lifting/routines", ui.routinesCard)
	page("/lifting/routines/{id}/edit", ui.routineEditor)
	change("/lifting/routines/{id}", ui.saveRoutine)
	change("/lifting/routines/{id}/delete", ui.deleteRoutine)
	page("/lifting/progress", ui.progressCard)
	page("/lifting/history", ui.liftingHistoryCard)
	page("/lifting/workouts/{id}/edit", ui.workoutEditor)
	change("/lifting/workouts/{id}", ui.saveWorkout)
	change("/lifting/workouts/{id}/delete", ui.deleteWorkout)
	change("/lifting/unit", ui.setLiftingUnit)
	page("/lifting/export.csv", ui.exportLifting)
	page("/lifting/one-rep-max", ui.oneRepMax)
	page("/cards/strength", ui.strengthCard)
	change("/cards/strength", ui.saveStrength)

	// History
	page("/history", ui.historyPage)
	page("/history/activity", ui.activityCard)
	page("/history/skips", ui.skipsCard)
	page("/history/time-log", ui.timeLogCard)
	change("/time", ui.trackTime)
	page("/time/{id}/edit", ui.timeEntryEditor)
	change("/time/{id}", ui.saveTimeEntry)
	change("/time/{id}/delete", ui.deleteTimeEntry)

	// Anki
	page("/anki", ui.ankiPage)
	page("/anki/body", ui.ankiBodyPart)
	page("/anki/decks", ui.ankiDecks)
	change("/anki/open", ui.openAnki)
	change("/anki/sync", ui.syncAnki)
	change("/anki/study", ui.studyDeck)
	page("/anki/add-form", ui.ankiAddFormPart)
	change("/anki/notes", ui.addAnkiNote)
	page("/anki/notes", ui.ankiNotes)
	page("/anki/notes/{id}/edit", ui.ankiNoteEditor)
	change("/anki/notes/{id}", ui.saveAnkiNote)
	change("/anki/notes/{id}/suspend", ui.toggleAnkiSuspended)
	change("/anki/notes/{id}/delete", ui.deleteAnkiNote)
	change("/anki/notes/{id}/browse", ui.browseAnkiNote)

	// MCPs: the AI runs in the browser (browserai.go)
	page("/mcps", ui.chatPage)

	// Docs
	page("/docs", ui.docsPage)
	// Sealed notes (sealed.go): the browser reads and writes them.
	page("/notes/key", ui.notesKey)
	page("/notes/data", ui.sealedData("notes"))
	change("/notes/data", ui.saveSealedData("notes"))
	// The AI in the browser (browserai.go) and its sealed chat.
	page("/assistant/setup", ui.assistantSetup)
	page("/assistant/tools", ui.assistantTools)
	change("/assistant/tool", ui.assistantTool)
	change("/assistant/settings", ui.assistantSettings)
	change("/assistant/run", ui.assistantRun)
	change("/assistant/models", ui.assistantModels)
	page("/assistant/model-relay", ui.modelRelay)
	change("/assistant/model-relay", ui.modelRelay)
	change("/account/server-ai", ui.setServerAI)
	page("/assistant/chat", ui.sealedData("chat"))
	change("/assistant/chat", ui.saveSealedData("chat"))
	page("/assistant/keys", ui.sealedData("settings"))
	change("/assistant/keys", ui.saveSealedData("settings"))
}

// fromHTMX refuses changes that didn't come from the dashboard's own page.
func fromHTMX(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("HX-Request") != "true" {
			http.Error(w, "changes must come from the dashboard page", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// UnlockData makes sure a signed-in request's data is open before any page uses it (see
// workspace.Manager.Unlock). When the session can't open it (one from before the data was
// encrypted, or after the user's data was reset), the browser is sent to sign in again, which
// unlocks it with the password; a plugin token that can't is refused.
func (ui *UI) UnlockData(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, signedIn := auth.UserFrom(r)
		if !signedIn {
			next.ServeHTTP(w, r)
			return
		}
		kind, secret := auth.CredentialFrom(r)
		err := ui.Workspaces.Unlock(user, kind, secret)
		if kind == auth.BySession && (!user.ProofLogin || user.NotesKey == nil) {
			// From before proof sign-ins (signing in again moves the account over, auth.Upgrade),
			// or before the account had a notes key (the next sign-in makes it).
			err = workspace.ErrSignInAgain
		}
		switch {
		case err == nil:
			next.ServeHTTP(w, r)
		case kind == auth.ByToken:
			httpx.WriteError(w, http.StatusUnauthorized, "this token can't open your data: make a new one on the Account page")
		case errors.Is(err, workspace.ErrSignInAgain):
			ui.Auth.Logout(secret)
			auth.ClearSessionCookie(w)
			if r.Header.Get("HX-Request") == "true" {
				w.Header().Set("HX-Redirect", "/login?locked=1")
				http.Error(w, problem.Public(err), http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/login?locked=1", http.StatusSeeOther)
		default:
			http.Error(w, "Your data couldn't be opened: "+problem.Public(err), problem.Status(err))
		}
	})
}
