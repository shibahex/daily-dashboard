package server

import (
	"fmt"
	"net/http"
	"time"

	"dashboard/internal/timewarrior"
)

func (s *Server) routeTime(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/time", s.listIntervals)
	mux.HandleFunc("POST /api/time/track", handlePost(s.trackTime))
	mux.HandleFunc("POST /api/time/modify", handlePost(s.modifyInterval))
	mux.HandleFunc("POST /api/time/delete", handlePost(s.deleteInterval))
}

// listIntervals returns this week's tracked time, the past year's (?range=year), or any range
// (?from=YYYY-MM-DD&to=YYYY-MM-DD, to included).
func (s *Server) listIntervals(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	rangeArgs := []string{":week"}
	switch {
	case query.Get("range") == "year":
		rangeArgs = []string{"from", time.Now().AddDate(0, 0, -371).Format("2006-01-02")}
	case isoDate.MatchString(query.Get("from")) && isoDate.MatchString(query.Get("to")):
		to, _ := time.ParseInLocation("2006-01-02", query.Get("to"), time.Local)
		rangeArgs = []string{"from", query.Get("from"), "to", to.AddDate(0, 0, 1).Format("2006-01-02")}
	}
	intervals, result := s.Time.Export(rangeArgs...)
	switch {
	case result.ExitCode == timewarrior.ExitNotInstalled:
		writeJSON(w, http.StatusOK, map[string]any{"available": false, "intervals": []any{}})
	case !result.OK():
		writeError(w, http.StatusInternalServerError, orDefault(result.Error(), "timew export failed"))
	default:
		writeJSON(w, http.StatusOK, map[string]any{"available": true, "intervals": intervals})
	}
}

// trackTime: {"start": "2026-10-20T14:00", "end": "...", "tags": [...]} logs a stretch of time.
func (s *Server) trackTime(w http.ResponseWriter, r *http.Request, body Body) {
	start, end := body.String("start"), body.String("end")
	if !localDateTime.MatchString(start) || !localDateTime.MatchString(end) || end <= start {
		writeError(w, http.StatusBadRequest, "end must be after start")
		return
	}
	tags, err := timewarrior.CleanTags(body.Strings("tags"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	result := s.Time.Run(append([]string{"track", start, "-", end}, tags...)...)
	writeOutcome(w, result.OK(), result.FirstErrorLine(""), http.StatusConflict)
}

// intervalToChange reads {"id", "start_was"} and checks the id still means that entry.
func (s *Server) intervalToChange(w http.ResponseWriter, body Body) (id int, ok bool) {
	id, isInt := body.Int("id")
	if !isInt || id < 1 || !s.Time.StillThere(id, body.String("start_was")) {
		writeError(w, http.StatusConflict, "that entry changed since the page loaded; refresh and try again")
		return 0, false
	}
	return id, true
}

func (s *Server) deleteInterval(w http.ResponseWriter, r *http.Request, body Body) {
	id, ok := s.intervalToChange(w, body)
	if !ok {
		return
	}
	result := s.Time.Run("delete", fmt.Sprintf("@%d", id))
	writeOutcome(w, result.OK(), result.Error(), http.StatusInternalServerError)
}

// modifyInterval: {"id", "start_was", "tags"?, "start"?, "end"?} changes an entry. Retagging
// and moving times don't shift ids, so @id stays valid across the steps.
func (s *Server) modifyInterval(w http.ResponseWriter, r *http.Request, body Body) {
	id, ok := s.intervalToChange(w, body)
	if !ok {
		return
	}
	ref := fmt.Sprintf("@%d", id)
	var steps [][]string
	if body.Has("tags") {
		tags, err := timewarrior.CleanTags(body.Strings("tags"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		steps = append(steps, append([]string{"retag", ref}, tags...))
	}
	for _, edge := range []string{"start", "end"} {
		if !body.Has(edge) {
			continue
		}
		when := body.String(edge)
		if !localDateTime.MatchString(when) {
			writeError(w, http.StatusBadRequest, "bad "+edge)
			return
		}
		steps = append(steps, []string{"modify", edge, ref, when})
	}
	for _, step := range steps {
		if result := s.Time.Run(step...); !result.OK() {
			writeOutcome(w, false, result.FirstErrorLine("timew error"), http.StatusConflict)
			return
		}
	}
	writeOK(w)
}
