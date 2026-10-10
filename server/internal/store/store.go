package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

var (
	muData   sync.Mutex
	dataDir  string
	onceData sync.Once
	// dataDirOverride, when set through SetDataDir, wins over the environment and
	// over the directory sniffing below. Tests use it to stay inside a temp dir.
	dataDirOverride string
)

func DataDir() string {
	if v := dataDirOverride; v != "" {
		return v
	}
	onceData.Do(func() {
		if v := os.Getenv("JM_AURA_DATA_DIR"); v != "" {
			dataDir = v
			return
		}
		for _, c := range []string{"backend/config", "config"} {
			if st, err := os.Stat(c); err == nil && st.IsDir() {
				dataDir = c
				return
			}
		}
		dataDir = "config"
	})
	return dataDir
}

// SetDataDir pins the data directory for the rest of the process. It exists for
// tests, which must never write into a real config directory; production code
// configures JM_AURA_DATA_DIR instead.
func SetDataDir(dir string) {
	muData.Lock()
	defer muData.Unlock()
	dataDirOverride = dir
	dataDir = dir
	onceData = sync.Once{}
	onceData.Do(func() {}) // mark the once as already fired
}

func Path(rel string) string {
	if filepath.IsAbs(rel) {
		return rel
	}
	return filepath.Join(DataDir(), rel)
}

func LoadJSON(rel string) (map[string]any, bool) {
	muData.Lock()
	defer muData.Unlock()
	p := Path(rel)
	raw, err := os.ReadFile(p)
	if err != nil {
		return map[string]any{}, false
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil || doc == nil {
		return map[string]any{}, false
	}
	return doc, true
}

func SaveJSON(rel string, doc map[string]any) error {
	muData.Lock()
	defer muData.Unlock()
	p := Path(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func getStr(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func setStr(m map[string]any, key, val string) {
	if val == "" {
		delete(m, key)
		return
	}
	m[key] = val
}
