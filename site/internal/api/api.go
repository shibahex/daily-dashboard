// Package api is the small JSON API the Noctalia bar plugin uses: today's checklist, tasks and
// tracked time, and the quick actions on them. Every change needs a JSON body and an
// X-Dashboard header, which other websites can't send.
package api

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"

	"dashboard/internal/httpx"
	"dashboard/internal/problem"
	"dashboard/internal/store"
	"dashboard/internal/tasks"
	"dashboard/internal/timewarrior"
)

type API struct {
	Store *store.Store
	Tasks *tasks.Service
	Time  timewarrior.Client
}

var isoDate = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

func (a *API) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/plan", a.plan)
	mux.HandleFunc("GET /api/checklist", a.checklist)
	mux.HandleFunc("POST /api/checklist", jsonPost(a.saveChecklistDay))
	mux.HandleFunc("GET /api/tasks", a.pendingTasks)
	mux.HandleFunc("GET /api/time", a.thisWeeksTime)
	for _, action := range []string{"done", "start", "stop"} {
		mux.HandleFunc("POST /api/tasks/"+action, jsonPost(a.taskAction(action)))
	}
	mux.HandleFunc("POST /api/tasks/done_at", jsonPost(a.completeAt))
	mux.HandleFunc("POST /api/tasks/skip", jsonPost(a.skip))
}

func (a *API) plan(w http.ResponseWriter, r *http.Request) {
	plan, err := a.Store.PlanJSON()
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteBytes(w, http.StatusOK, plan, "application/json")
}

func (a *API) checklist(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, a.Store.Checklist())
}

// saveChecklistDay: {"date": "YYYY-MM-DD", "items": {"Weigh in": "done", ...}} replaces that day.
func (a *API) saveChecklistDay(w http.ResponseWriter, r *http.Request, body request) {
	day, items := body.text("date"), map[string]any{}
	if raw, sent := body["items"]; sent && (json.Unmarshal(raw, &items) != nil || items == nil) {
		httpx.WriteError(w, http.StatusBadRequest, "bad checklist")
		return
	}
	if !isoDate.MatchString(day) {
		httpx.WriteError(w, http.StatusBadRequest, "bad checklist")
		return
	}
	respond(w, a.Store.SaveChecklistDay(day, items))
}

func (a *API) pendingTasks(w http.ResponseWriter, r *http.Request) {
	pending, err := a.Tasks.Pending()
	if err != nil {
		httpx.WriteError(w, problem.Status(err), err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, pending)
}

func (a *API) thisWeeksTime(w http.ResponseWriter, r *http.Request) {
	intervals, err := a.Time.IntervalsThisWeek()
	switch {
	case problem.KindOf(err) == problem.Unavailable:
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"available": false, "intervals": []any{}})
	case err != nil:
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
	default:
		if intervals == nil {
			intervals = []timewarrior.Interval{}
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"available": true, "intervals": intervals})
	}
}

// taskAction: {"uuid"} completes, starts or stops a task.
func (a *API) taskAction(action string) postHandler {
	return func(w http.ResponseWriter, r *http.Request, body request) {
		respond(w, a.Tasks.Do(body.text("uuid"), action))
	}
}

// completeAt: {"uuid", "end": "2026-10-20T14:30", "minutes": 30?}.
func (a *API) completeAt(w http.ResponseWriter, r *http.Request, body request) {
	end, validEnd := tasks.ParseLocalMinute(body.text("end"))
	minutes, validMinutes := body.minutes()
	if !validEnd {
		httpx.WriteError(w, http.StatusBadRequest, "bad request")
		return
	}
	if !validMinutes {
		httpx.WriteError(w, http.StatusBadRequest, "minutes must be between 1 and 1440")
		return
	}
	warning, err := a.Tasks.CompleteAt(body.text("uuid"), end, minutes)
	if err != nil {
		respond(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "warning": warning})
}

// skip: {"uuids", "note", "kind": "skip"|"dismiss"}.
func (a *API) skip(w http.ResponseWriter, r *http.Request, body request) {
	var uuids []string
	json.Unmarshal(body["uuids"], &uuids)
	respond(w, a.Tasks.Skip(uuids, body.text("note"), body.text("kind") == "dismiss"))
}

// respond sends {"ok": true} or the error as {"ok": false, "error"} with its status.
func respond(w http.ResponseWriter, err error) {
	if err != nil {
		httpx.WriteJSON(w, problem.Status(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------- request bodies ----------

const maxBodyBytes = 256_000

// request is a JSON object body, values decoded when asked for.
type request map[string]json.RawMessage

// text is a value as text ("" when missing or null).
func (b request) text(key string) string {
	var s string
	if json.Unmarshal(b[key], &s) == nil {
		return s
	}
	if raw := strings.TrimSpace(string(b[key])); raw != "null" {
		return raw
	}
	return ""
}

// minutes reads "minutes": missing, null, "" or 0 is none; otherwise a whole number 1-1440.
func (b request) minutes() (int, bool) {
	raw := strings.TrimSpace(string(b["minutes"]))
	if raw == "" || raw == "null" || raw == `""` || raw == "0" || raw == "0.0" {
		return 0, true
	}
	var n json.Number
	if json.Unmarshal(b["minutes"], &n) != nil {
		return 0, false
	}
	minutes, err := n.Int64()
	return int(minutes), err == nil && minutes > 0 && minutes <= 24*60
}

type postHandler func(http.ResponseWriter, *http.Request, request)

// jsonPost checks the headers only the dashboard and the plugin send, then decodes the body.
func jsonPost(handler postHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Dashboard") != "1" || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			httpx.WriteError(w, http.StatusForbidden, "missing dashboard headers")
			return
		}
		data, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
		if err != nil || len(data) > maxBodyBytes {
			httpx.WriteError(w, http.StatusRequestEntityTooLarge, "too large")
			return
		}
		var body request
		if len(strings.TrimSpace(string(data))) == 0 {
			body = request{}
		} else if json.Unmarshal(data, &body) != nil || body == nil {
			httpx.WriteError(w, http.StatusBadRequest, "expected a JSON object")
			return
		}
		handler(w, r, body)
	}
}
