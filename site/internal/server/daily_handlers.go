package server

import (
	"errors"
	"math"
	"net/http"
	"regexp"

	"dashboard/internal/store"
)

var isoDate = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// The plan, the daily checklist and weigh-ins.
func (s *Server) routeDaily(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/plan", s.getPlan)
	mux.HandleFunc("GET /plan.json", s.getPlan)
	mux.HandleFunc("POST /api/plan", handlePost(s.savePlan))
	mux.HandleFunc("GET /api/checklist", s.getChecklist)
	mux.HandleFunc("POST /api/checklist", handlePost(s.saveChecklistDay))
	mux.HandleFunc("GET /api/weights", s.getWeights)
	mux.HandleFunc("POST /api/weights", handlePost(s.logWeight))
}

func (s *Server) getPlan(w http.ResponseWriter, r *http.Request) {
	plan, err := s.Store.PlanJSON()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "couldn't read plan.json: "+err.Error())
		return
	}
	writeBytes(w, http.StatusOK, plan, "application/json")
}

// savePlan: {"plan": {...}} replaces plan.json.
func (s *Server) savePlan(w http.ResponseWriter, r *http.Request, body Body) {
	if _, isObject := body.Object("plan"); !isObject {
		writeError(w, http.StatusBadRequest, "plan is missing sections")
		return
	}
	if err := s.Store.SavePlan(body.Raw("plan")); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, store.ErrPlanMissingSections) {
			status = http.StatusBadRequest
		}
		writeError(w, status, err.Error())
		return
	}
	writeOK(w)
}

func (s *Server) getChecklist(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Store.Checklist())
}

// saveChecklistDay: {"date": "YYYY-MM-DD", "items": {"Weigh in": "done", ...}} replaces that day.
func (s *Server) saveChecklistDay(w http.ResponseWriter, r *http.Request, body Body) {
	day := body.String("date")
	items := map[string]any{} // no "items" clears the day
	if body.Has("items") && (!body.Decoded("items", &items) || items == nil) {
		writeError(w, http.StatusBadRequest, "bad checklist")
		return
	}
	if !isoDate.MatchString(day) {
		writeError(w, http.StatusBadRequest, "bad checklist")
		return
	}
	if err := s.Store.SaveChecklistDay(day, items); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOK(w)
}

func (s *Server) getWeights(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Store.Weights())
}

// logWeight: {"date": ..., "lb": 182.4} logs a weigh-in ("lb": null removes it). A new weigh-in
// is sent to MyFitnessPal too, so both have the same ones.
func (s *Server) logWeight(w http.ResponseWriter, r *http.Request, body Body) {
	day := body.String("date")
	var lb *float64
	if body.IsSet("lb") {
		value, isNumber := body.Number("lb")
		if !isNumber || value <= 50 || value >= 700 {
			writeError(w, http.StatusBadRequest, "bad weight")
			return
		}
		rounded := math.Round(value*10) / 10
		lb = &rounded
	}
	if !isoDate.MatchString(day) {
		writeError(w, http.StatusBadRequest, "bad weight")
		return
	}
	if err := s.Store.SetWeight(day, lb); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	warning := ""
	if lb != nil {
		if _, err := s.MyFitnessPal.Change(map[string]any{"op": "weight", "date": day, "lb": *lb}); err != nil {
			warning = "Saved here, but not in MyFitnessPal: " + err.Error()
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "warning": warning})
}
