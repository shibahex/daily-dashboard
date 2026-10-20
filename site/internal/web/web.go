// Package web is the dashboard's pages. They're rendered on the server with html/template and
// updated with HTMX: every card and list has its own URL, and a change answers with the new
// HTML plus events (HX-Trigger) that tell other cards to reload themselves.
package web

import (
	"net/http"

	"dashboard/internal/assistant"
	"dashboard/internal/auth"
	"dashboard/internal/config"
	"dashboard/internal/workspace"
)

// UI holds what the pages read and change.
type UI struct {
	Auth       *auth.Auth
	Paths      config.Paths // the server's: templates and static files
	Workspaces *workspace.Manager
	Providers  assistant.Providers

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
	change("/account/password", ui.changePassword)
	change("/account/token", ui.makeToken)
	change("/account/token/remove", ui.removeToken)
	change("/account/myfitnesspal", ui.connectMyFitnessPal)
	change("/account/myfitnesspal/remove", ui.disconnectMyFitnessPal)
	change("/account/anki", ui.connectAnki)
	change("/account/anki/remove", ui.disconnectAnki)
	change("/account/keys", ui.saveAPIKey)

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

	// MCPs (chat)
	page("/mcps", ui.chatPage)
	page("/mcps/card", ui.chatCard)
	page("/mcps/stream", ui.chatStream)
	change("/mcps/messages", ui.sendMessage)
	change("/mcps/stop", ui.stopAnswer)
	change("/mcps/new", ui.newChat)
	change("/mcps/provider", ui.chooseProvider)
	change("/mcps/model", ui.chooseModel)
	change("/mcps/toggle", ui.toggleMCP)

	// Docs
	page("/docs", ui.docsPage)
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
