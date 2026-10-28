package myfitnesspal

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"dashboard/internal/jsonfile"
	"dashboard/internal/store"
)

const (
	SyncEvery    = 10 * time.Minute // background sync
	StaleAfter   = 5 * time.Minute  // a page asking for older data starts a sync
	fetchTimeout = 90 * time.Second
)

// Cache is today's synced diary (data/mfp.json). Earlier days the Today tab looks at are kept in memory
// (Syncer.Day). Data is a Day.
type Cache struct {
	Data      json.RawMessage `json:"data,omitempty"`
	FetchedAt float64         `json:"fetched_at,omitempty"` // unix seconds
	Error     *string         `json:"error"`
	FailedAt  float64         `json:"failed_at,omitempty"`
}

// day is the part of the cached answer the syncer itself reads.
type day struct {
	Day     string         `json:"day"`
	Weights []store.Weight `json:"weights"`
}

// Syncer keeps the cache fresh: in the background, on request, and after every change.
// One sync or change runs at a time.
type Syncer struct {
	client    Client
	store     *store.Store
	cachePath string
	mu        sync.Mutex
	busy      atomic.Bool

	pastMu   sync.Mutex
	past     map[string]Cache // earlier days, by YYYY-MM-DD
	fetching map[string]bool

	meals  kept[Food]      // saved meals
	recent kept[Recent]    // MyFitnessPal's recently logged foods
	known  map[string]Food // recent foods as looked up, by food id (used within recent.get)
}

func NewSyncer(client Client, s *store.Store, cachePath string) *Syncer {
	return &Syncer{client: client, store: s, cachePath: cachePath}
}

func (s *Syncer) Client() Client { return s.client }

// Busy reports whether a sync or change is running right now.
func (s *Syncer) Busy() bool { return s.busy.Load() }

// Cached returns the last saved state.
func (s *Syncer) Cached() Cache {
	var cache Cache
	jsonfile.Read(s.cachePath, &cache)
	return cache
}

// Status returns the cache and whether it's old enough to refresh.
func (s *Syncer) Status() (cache Cache, stale bool) {
	cache = s.Cached()
	if cache.Data == nil {
		return cache, true
	}
	var synced day
	json.Unmarshal(cache.Data, &synced)
	age := time.Since(time.Unix(int64(cache.FetchedAt), 0))
	return cache, synced.Day != today() || age > StaleAfter
}

// Sync fetches today's food, goals and weigh-ins. If a sync is already running, it waits for
// that one instead of starting another.
func (s *Syncer) Sync() Cache {
	if !s.mu.TryLock() {
		s.mu.Lock()
		s.mu.Unlock()
		return s.Cached()
	}
	defer s.unlock()
	s.busy.Store(true)
	data, err := s.client.Fetch(today())
	return s.save(data, err)
}

// SyncInBackground starts a sync unless one is running.
func (s *Syncer) SyncInBackground() {
	if !s.Busy() {
		go s.Sync()
	}
}

// Change runs a diary change (add, edit or delete a food, log a weigh-in) on date, then reads
// that day again, which becomes the cache (today's, or an earlier day's).
func (s *Syncer) Change(date string, op func(ctx context.Context) error) (Cache, error) {
	s.mu.Lock()
	defer s.unlock()
	s.busy.Store(true)
	if err := s.client.call(writeTimeout, op); err != nil {
		return s.Cached(), err
	}
	data, err := s.client.Fetch(date)
	if err != nil {
		return s.Cached(), err
	}
	return s.save(data, nil), nil
}

// RunUntil syncs now and then every SyncEvery, until stop is closed.
func (s *Syncer) RunUntil(stop <-chan struct{}) {
	ticker := time.NewTicker(SyncEvery)
	defer ticker.Stop()
	for {
		s.Sync()
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
	}
}

func (s *Syncer) unlock() {
	s.busy.Store(false)
	s.mu.Unlock()
}

// save stores a successful answer (and its weigh-ins), or records the error on the old cache.
// An answer for an earlier day goes to the in-memory cache for that day instead.
func (s *Syncer) save(data json.RawMessage, err error) Cache {
	now := float64(time.Now().UnixNano()) / 1e9
	if err == nil {
		var synced day
		json.Unmarshal(data, &synced)
		if mergeErr := s.store.MergeWeighIns(synced.Weights, today()); mergeErr != nil {
			log.Printf("saving MyFitnessPal weigh-ins: %v", mergeErr)
		}
		if synced.Day != "" && synced.Day != today() {
			cache := Cache{Data: data, FetchedAt: now}
			s.keepPast(synced.Day, cache)
			return cache
		}
	}
	cache := s.Cached()
	if err == nil {
		cache = Cache{Data: data, FetchedAt: now}
	} else {
		message := err.Error()
		cache.Error, cache.FailedAt = &message, now
	}
	if writeErr := jsonfile.Write(s.cachePath, cache); writeErr != nil {
		log.Printf("saving the MyFitnessPal cache: %v", writeErr)
	}
	return cache
}

// ---------- earlier days ----------

// pastDays is how many earlier days stay in memory.
const pastDays = 14

func (s *Syncer) keepPast(date string, cache Cache) {
	s.pastMu.Lock()
	defer s.pastMu.Unlock()
	if s.past == nil {
		s.past = map[string]Cache{}
	}
	s.past[date] = cache
	for len(s.past) > pastDays { // forget the one fetched longest ago
		oldest := ""
		for d, c := range s.past {
			if oldest == "" || c.FetchedAt < s.past[oldest].FetchedAt {
				oldest = d
			}
		}
		delete(s.past, oldest)
	}
}

// Day is the diary for date (YYYY-MM-DD): today's cache, or an earlier day's, fetched in the
// background when it's missing or old. loading reports a fetch is on its way.
func (s *Syncer) Day(date string) (cache Cache, loading bool) {
	if date == today() {
		cache, stale := s.Status()
		if stale {
			s.SyncInBackground()
		}
		return cache, stale || s.Busy()
	}
	s.pastMu.Lock()
	cache, known := s.past[date]
	fetching := s.fetching[date]
	// Wait StaleAfter after the last try, whether it worked or not.
	tried := time.Unix(int64(max(cache.FetchedAt, cache.FailedAt)), 0)
	stale := !known || time.Since(tried) > StaleAfter
	if stale && !fetching {
		if s.fetching == nil {
			s.fetching = map[string]bool{}
		}
		s.fetching[date] = true
		go s.FetchDay(date)
	}
	s.pastMu.Unlock()
	return cache, stale || fetching
}

// FetchDay fetches an earlier day now (after any run in progress).
func (s *Syncer) FetchDay(date string) Cache {
	if date == today() {
		return s.Sync()
	}
	defer func() {
		s.pastMu.Lock()
		delete(s.fetching, date)
		s.pastMu.Unlock()
	}()
	s.mu.Lock()
	defer s.unlock()
	s.busy.Store(true)
	data, err := s.client.Fetch(date)
	if err != nil {
		message := err.Error()
		s.pastMu.Lock()
		cache := s.past[date]
		s.pastMu.Unlock()
		cache.Error, cache.FailedAt = &message, float64(time.Now().UnixNano())/1e9
		s.keepPast(date, cache)
		return cache
	}
	return s.save(data, nil)
}

func today() string { return time.Now().Format("2006-01-02") }

// Fetch reads a day (YYYY-MM-DD) as the cache keeps it.
func (c Client) Fetch(date string) (json.RawMessage, error) {
	var day Day
	err := c.call(fetchTimeout, func(ctx context.Context) (err error) { day, err = c.fetch(ctx, date); return err })
	if err != nil {
		return nil, err
	}
	return json.Marshal(day)
}
