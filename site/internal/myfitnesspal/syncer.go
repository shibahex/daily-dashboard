package myfitnesspal

import (
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
	fetchTimeout = 150 * time.Second
	writeTimeout = 150 * time.Second
)

// Cache is today's synced diary (data/mfp.json). Data is mfp_fetch.py's answer, kept as is.
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
// One MyFitnessPal run happens at a time.
type Syncer struct {
	client    Client
	store     *store.Store
	cachePath string
	mu        sync.Mutex
	busy      atomic.Bool
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
	data, err := s.client.Run(map[string]any{"op": "fetch", "date": today()}, fetchTimeout)
	return s.save(data, err)
}

// SyncInBackground starts a sync unless one is running.
func (s *Syncer) SyncInBackground() {
	if !s.Busy() {
		go s.Sync()
	}
}

// Change runs a diary change (add, edit or delete a food, log a weigh-in). mfp_fetch.py answers
// with the refreshed day, which becomes the cache.
func (s *Syncer) Change(request map[string]any) (Cache, error) {
	s.mu.Lock()
	defer s.unlock()
	s.busy.Store(true)
	data, err := s.client.Run(request, writeTimeout)
	if err != nil {
		return s.Cached(), err
	}
	return s.save(data, nil), nil
}

// RunForever syncs every SyncEvery.
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
func (s *Syncer) save(data json.RawMessage, err error) Cache {
	now := float64(time.Now().UnixNano()) / 1e9
	cache := s.Cached()
	if err == nil {
		cache = Cache{Data: data, FetchedAt: now}
		var synced day
		json.Unmarshal(data, &synced)
		if mergeErr := s.store.MergeWeighIns(synced.Weights, today()); mergeErr != nil {
			log.Printf("saving MyFitnessPal weigh-ins: %v", mergeErr)
		}
	} else {
		message := err.Error()
		cache.Error, cache.FailedAt = &message, now
	}
	if writeErr := jsonfile.Write(s.cachePath, cache); writeErr != nil {
		log.Printf("saving the MyFitnessPal cache: %v", writeErr)
	}
	return cache
}

func today() string { return time.Now().Format("2006-01-02") }
