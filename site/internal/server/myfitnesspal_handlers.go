package server

import (
	"net/http"
	"regexp"
	"strconv"
	"time"

	"dashboard/internal/myfitnesspal"
	"dashboard/internal/text"
)

// MyFitnessPal's food, entry and serving ids are plain numbers.
var (
	mfpID     = regexp.MustCompile(`^\d{1,20}$`)
	mealIndex = regexp.MustCompile(`^\d{1,2}$`)
)

const mfpLookupTimeout = 90 * time.Second

func (s *Server) routeMyFitnessPal(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/mfp", s.todaysDiary)
	mux.HandleFunc("POST /api/mfp/refresh", handlePost(s.syncDiary))
	mux.HandleFunc("POST /api/mfp/search", handlePost(s.searchFoods))
	mux.HandleFunc("POST /api/mfp/entry", handlePost(s.diaryEntry))
	mux.HandleFunc("POST /api/mfp/add", handlePost(s.addFood))
	mux.HandleFunc("POST /api/mfp/edit", handlePost(s.editFood))
	mux.HandleFunc("POST /api/mfp/delete", handlePost(s.deleteFood))
}

// diaryResponse is the cached day plus whether a sync is running (and "ok" after a change).
type diaryResponse struct {
	myfitnesspal.Cache
	Syncing bool  `json:"syncing"`
	OK      *bool `json:"ok,omitempty"`
}

// todaysDiary answers from the cache straight away; an old cache starts a sync in the background.
func (s *Server) todaysDiary(w http.ResponseWriter, r *http.Request) {
	cache, stale := s.MyFitnessPal.Status()
	if stale {
		s.MyFitnessPal.SyncInBackground()
	}
	writeJSON(w, http.StatusOK, diaryResponse{Cache: cache, Syncing: stale || s.MyFitnessPal.Busy()})
}

func (s *Server) syncDiary(w http.ResponseWriter, r *http.Request, body Body) {
	writeJSON(w, http.StatusOK, diaryResponse{Cache: s.MyFitnessPal.Sync()})
}

// searchFoods: {"query"} searches MyFitnessPal's food database.
func (s *Server) searchFoods(w http.ResponseWriter, r *http.Request, body Body) {
	query, ok := myfitnesspal.SearchQuery(body.String("query"))
	if !ok {
		writeError(w, http.StatusBadRequest, "type what to search for")
		return
	}
	s.lookup(w, map[string]any{"op": "search", "query": query})
}

// diaryEntry: {"entry_id"} returns an entry's edit options (servings, meals).
func (s *Server) diaryEntry(w http.ResponseWriter, r *http.Request, body Body) {
	entryID := body.String("entry_id")
	if !mfpID.MatchString(entryID) {
		writeError(w, http.StatusBadRequest, "bad entry")
		return
	}
	s.lookup(w, map[string]any{"op": "entry", "entry_id": entryID})
}

func (s *Server) lookup(w http.ResponseWriter, request map[string]any) {
	answer, err := s.MyFitnessPal.Client().Run(request, mfpLookupTimeout)
	if err != nil {
		writeOutcome(w, false, err.Error(), http.StatusBadGateway)
		return
	}
	writeBytes(w, http.StatusOK, answer, "application/json")
}

// addFood: {"food_id", "weight_id", "quantity", "meal"} logs a food today.
func (s *Server) addFood(w http.ResponseWriter, r *http.Request, body Body) {
	request, ok := diaryChange(w, body, "add", "food_id", "weight_id")
	if !ok {
		return
	}
	meal := text.Collapse(body.String("meal"))
	if meal == "" || len([]rune(meal)) > 40 {
		writeError(w, http.StatusBadRequest, "pick a meal")
		return
	}
	request["meal"] = meal
	s.changeDiary(w, request)
}

// editFood: {"entry_id", "weight_id", "quantity", "meal_id"} changes an entry's amount,
// serving or meal.
func (s *Server) editFood(w http.ResponseWriter, r *http.Request, body Body) {
	request, ok := diaryChange(w, body, "edit", "entry_id", "weight_id")
	if !ok {
		return
	}
	if !mealIndex.MatchString(body.String("meal_id")) {
		writeError(w, http.StatusBadRequest, "pick a meal")
		return
	}
	request["meal_id"] = body.String("meal_id")
	s.changeDiary(w, request)
}

// deleteFood: {"entry_id"} removes an entry.
func (s *Server) deleteFood(w http.ResponseWriter, r *http.Request, body Body) {
	if request, ok := diaryChange(w, body, "delete", "entry_id"); ok {
		s.changeDiary(w, request)
	}
}

// diaryChange starts a change request for today with the given ids, checking them (and the
// quantity, for add and edit).
func diaryChange(w http.ResponseWriter, body Body, op string, idKeys ...string) (map[string]any, bool) {
	request := map[string]any{"op": op, "date": time.Now().Format("2006-01-02")}
	for _, key := range idKeys {
		id := body.String(key)
		if !mfpID.MatchString(id) {
			writeError(w, http.StatusBadRequest, "bad food or entry")
			return nil, false
		}
		request[key] = id
	}
	if op == "add" || op == "edit" {
		quantity := 1.0
		if body.Has("quantity") {
			parsed, err := strconv.ParseFloat(body.TrimmedString("quantity"), 64)
			if err != nil {
				parsed = -1
			}
			quantity = parsed
		}
		if quantity <= 0 || quantity > 1000 {
			writeError(w, http.StatusBadRequest, "amount must be more than 0")
			return nil, false
		}
		request["quantity"] = quantity
	}
	return request, true
}

func (s *Server) changeDiary(w http.ResponseWriter, request map[string]any) {
	cache, err := s.MyFitnessPal.Change(request)
	if err != nil {
		writeOutcome(w, false, err.Error(), http.StatusBadGateway)
		return
	}
	ok := true
	writeJSON(w, http.StatusOK, diaryResponse{Cache: cache, OK: &ok})
}
