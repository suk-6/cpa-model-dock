package catalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/suk-6/cpa-model-dock/internal/hostabi"
	"github.com/suk-6/cpa-model-dock/internal/policy"
)

const cacheFilename = "catalog.json"
const maxCacheBytes = 16 << 20

// Deliberately separate from the host and public status DTOs. No credential
// identifiers, errors, runtime prefixes, or status messages are persisted.
type savedCatalog struct {
	Version    int            `json:"version"`
	LastGoodAt time.Time      `json:"last_good_at"`
	Models     []policy.Model `json:"models"`
}

// NewPersistent loads last-good nonempty registration facts from stateDir.
// Loaded facts are stale, not evidence of a successful refresh in this process.
// Missing/corrupt/unwritable caches never prevent live inventory recovery.
// Callers must stop the old service before replacing a writer for the same dir.
// An empty stateDir disables persistence, as with New.
func NewPersistent(caller hostabi.Caller, stateDir string) *Service {
	s := New(caller)
	if stateDir == "" {
		return s
	}
	s.cacheDir = stateDir
	cached, err := loadCatalog(filepath.Join(stateDir, cacheFilename))
	if errors.Is(err, os.ErrNotExist) {
		return s
	}
	if err != nil {
		initial := s.Snapshot()
		initial.Warning = "catalog_cache_unavailable"
		s.snapshot.Store(&initial)
		return s
	}
	initial := policy.Catalog{
		Models: cached.Models, Prefixes: []string{}, RefreshedAt: cached.LastGoodAt,
		LastGoodAt: cached.LastGoodAt, Stale: true, Warning: "using_persisted_catalog",
	}
	s.snapshot.Store(&initial)
	return s
}

func loadCatalog(path string) (savedCatalog, error) {
	f, err := os.Open(path)
	if err != nil {
		return savedCatalog{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxCacheBytes+1))
	if err != nil {
		return savedCatalog{}, err
	}
	if len(data) > maxCacheBytes {
		return savedCatalog{}, errors.New("catalog cache too large")
	}
	var saved savedCatalog
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&saved); err != nil {
		return savedCatalog{}, err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return savedCatalog{}, errors.New("invalid catalog cache")
	}
	if saved.Version != 1 || saved.LastGoodAt.IsZero() || len(saved.Models) == 0 {
		return savedCatalog{}, errors.New("invalid catalog cache")
	}
	seen := make(map[string]bool, len(saved.Models))
	for i := range saved.Models {
		m := &saved.Models[i]
		if m.ID == "" || strings.TrimSpace(m.ID) != m.ID || seen[m.ID] || m.CredentialCount < 0 || m.Routable != (m.CredentialCount > 0) {
			return savedCatalog{}, errors.New("invalid catalog cache model")
		}
		seen[m.ID] = true
		providers := make(map[string]bool, len(m.Providers))
		for _, provider := range m.Providers {
			if provider == "" || strings.TrimSpace(provider) != provider || providers[provider] {
				return savedCatalog{}, errors.New("invalid catalog cache provider")
			}
			providers[provider] = true
		}
		m.Providers = append([]string{}, m.Providers...)
		sort.Strings(m.Providers)
	}
	sort.Slice(saved.Models, func(i, j int) bool { return saved.Models[i].ID < saved.Models[j].ID })
	return saved, nil
}

// Refresh's gate serializes writes with refresh publication. Complete temp-file
// bytes are synced before rename; a failed write never truncates the old cache.
func (s *Service) persist(c policy.Catalog) error {
	data, err := json.Marshal(savedCatalog{Version: 1, LastGoodAt: c.LastGoodAt, Models: c.Models})
	if err != nil {
		return err
	}
	if len(data) > maxCacheBytes {
		return errors.New("catalog cache too large")
	}
	if err := os.MkdirAll(s.cacheDir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(s.cacheDir, ".catalog-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), filepath.Join(s.cacheDir, cacheFilename)); err != nil {
		return err
	}
	dir, err := os.Open(s.cacheDir)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
