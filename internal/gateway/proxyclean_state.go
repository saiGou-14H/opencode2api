package gateway

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// Only stable node IDs are persisted; source data and endpoint addresses are
// deliberately absent. Store writes commit before scheduling flags change.
type proxyCleanDisabledStore struct {
	mu   sync.RWMutex
	path string
	ids  map[string]bool
}

// The basename is constant and never derived from a URL, node ID or HTTP input.
func proxyCleanStatePath(configPath string) (string, error) {
	if configPath == "" {
		return "", nil
	}
	absolute, err := filepath.Abs(configPath)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(absolute), "proxyclean-disabled.json"), nil
}
func validProxyCleanStateID(id string) bool {
	decoded, err := hex.DecodeString(id)
	return err == nil && len(decoded) == 8 && len(id) == 16
}

func loadProxyCleanDisabled(path string) (*proxyCleanDisabledStore, error) {
	s := &proxyCleanDisabledStore{path: path, ids: map[string]bool{}}
	if path == "" {
		return s, nil
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("proxyclean_state_path_invalid")
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("proxyclean_state_path_invalid")
	}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 1<<20 {
		return nil, errors.New("proxyclean_state_too_large")
	}
	var saved struct {
		Disabled []string `json:"disabled"`
	}
	if err = json.Unmarshal(data, &saved); err != nil {
		return nil, errors.New("proxyclean_state_invalid")
	}
	if len(saved.Disabled) > 10000 {
		return nil, errors.New("proxyclean_state_too_large")
	}
	for _, id := range saved.Disabled {
		if !validProxyCleanStateID(id) {
			return nil, errors.New("proxyclean_state_invalid")
		}
		s.ids[id] = true
	}
	return s, nil
}
func (s *proxyCleanDisabledStore) disabled(id string) bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ids[id]
}
func (s *proxyCleanDisabledStore) set(id string, disabled bool) error {
	if !validProxyCleanStateID(id) {
		return errors.New("proxyclean_state_invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ids[id] == disabled {
		return nil
	}
	ids := make([]string, 0, len(s.ids)+1)
	for existing := range s.ids {
		if existing != id {
			ids = append(ids, existing)
		}
	}
	if disabled {
		ids = append(ids, id)
	}
	if len(ids) > 10000 {
		return errors.New("proxyclean_state_full")
	}
	if s.path != "" {
		data, err := json.Marshal(struct {
			Disabled []string `json:"disabled"`
		}{ids})
		if err != nil {
			return err
		}
		if err = os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
			return err
		}
		f, err := os.CreateTemp(filepath.Dir(s.path), ".proxyclean-disabled-*")
		if err != nil {
			return err
		}
		temp := f.Name()
		defer os.Remove(temp)
		if _, err = f.Write(data); err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
		if err = os.Rename(temp, s.path); err != nil {
			return err
		}
	}
	if disabled {
		s.ids[id] = true
	} else {
		delete(s.ids, id)
	}
	return nil
}
