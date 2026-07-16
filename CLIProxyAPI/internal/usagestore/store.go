// Package usagestore provides a durable, append-only file-backed store for usage
// records emitted by the proxy runtime. It supplements the in-memory redisqueue
// buffer with a persistent JSONL file so that the Management API /usage endpoints
// (consumed by the web UI) can report all historical requests and their token usage.
//
// The store is gated by both usage-statistics-enabled (the global recording flag) and
// usage-store-enabled (the local persistence flag). When either is false, records are
// discarded and no data is written or served.
package usagestore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// StoredRecord is the on-disk representation of a single finished provider request.
// Field names are kept compatible with redisqueue.queuedUsageDetail so that the
// redisqueue sink and this durable store record equivalent data.
type StoredRecord struct {
	Timestamp           time.Time  `json:"timestamp"`
	Provider            string     `json:"provider"`
	ExecutorType        string     `json:"executor_type"`
	Model               string     `json:"model"`
	Alias               string     `json:"alias"`
	Endpoint            string     `json:"endpoint"`
	AuthType            string     `json:"auth_type"`
	APIKey              string     `json:"api_key"`
	AuthIndex           string     `json:"auth_index"`
	RequestID           string     `json:"request_id"`
	Source              string     `json:"source"`
	ReasoningEffort     string     `json:"reasoning_effort"`
	ServiceTier         string     `json:"service_tier"`
	ResponseServiceTier string     `json:"response_service_tier,omitempty"`
	LatencyMs           int64      `json:"latency_ms"`
	TTFTMs              int64      `json:"ttft_ms"`
	Failed              bool       `json:"failed"`
	Generate            bool       `json:"generate"`
	StatusCode          int        `json:"status_code,omitempty"`
	Reason              string     `json:"reason,omitempty"`
	Tokens              TokenStats `json:"tokens"`
}

// TokenStats mirrors the token breakdown persisted by the redisqueue sink.
type TokenStats struct {
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	ReasoningTokens     int64 `json:"reasoning_tokens"`
	CachedTokens        int64 `json:"cached_tokens"`
	CacheReadTokens     int64 `json:"cache_read_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_tokens"`
	TotalTokens         int64 `json:"total_tokens"`
}

// Store is a size-capped append-only JSONL store. Writes are serialized by a mutex.
// Reads scan the file line by line. When the file exceeds maxSizeBytes, the oldest
// records are pruned to bring it back under the cap.
type Store struct {
	mu           sync.Mutex
	path         string
	maxSizeBytes int64
	enabled      bool
	// useCaptureMetrics controls whether records are written. It is the conjunction of
	// usage-statistics-enabled and usage-store-enabled, computed by SetConfig.
	captureEnabled bool
}

var defaultStore = &Store{}

// DefaultStore returns the process-wide store used by the registered usage plugin and
// the Management API handlers.
func DefaultStore() *Store { return defaultStore }

// SetEnabled enables or disables the durable store. When false, Append is a no-op and
// Aggregate returns no data.
func (s *Store) SetEnabled(enabled bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.enabled = enabled
	s.mu.Unlock()
}

// SetCaptureEnabled reports whether the plugin should actually persist records. It is
// driven by SetConfig's usage-statistics + usage-store flags.
func (s *Store) SetCaptureEnabled(capture bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.captureEnabled = capture
	s.mu.Unlock()
}

// SetConfig configures the file location, max size, and persistence gate.
// authDir is used to resolve a relative path when path is empty or relative.
func (s *Store) SetConfig(path, authDir string, maxMB int, statsEnabled, storeEnabled bool) {
	if s == nil {
		return
	}
	resolved := ResolveStorePath(path, authDir)
	maxBytes := int64(maxMB) * 1024 * 1024
	if maxBytes < 0 {
		maxBytes = 0
	}

	s.mu.Lock()
	changedPath := s.path != resolved
	s.path = resolved
	s.maxSizeBytes = maxBytes
	s.enabled = storeEnabled
	s.captureEnabled = statsEnabled && storeEnabled
	s.mu.Unlock()

	if changedPath && resolved != "" {
		// Ensure the parent directory exists so the first Append can create the file.
		if dirErr := os.MkdirAll(filepath.Dir(resolved), 0o700); dirErr != nil {
			log.WithError(dirErr).WithField("path", resolved).Warn("usagestore: failed to ensure store directory")
		}
	}
}

// Append writes a single record as one JSON line. It is safe to call concurrently.
func (s *Store) Append(record StoredRecord) {
	if s == nil {
		return
	}
	s.mu.Lock()
	path := s.path
	if !s.enabled || !s.captureEnabled || path == "" {
		s.mu.Unlock()
		return
	}
	maxBytes := s.maxSizeBytes
	s.mu.Unlock()

	payload, errMarshal := json.Marshal(record)
	if errMarshal != nil {
		log.WithError(errMarshal).Warn("usagestore: failed to marshal record")
		return
	}
	payload = append(payload, '\n')

	if errAppend := s.appendLine(path, payload, maxBytes); errAppend != nil {
		log.WithError(errAppend).WithField("path", path).Warn("usagestore: failed to append record")
	}
}

func (s *Store) appendLine(path string, line []byte, maxBytes int64) error {
	// O_APPEND with existing-file semantics; create if missing.
	f, errOpen := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if errOpen != nil {
		return errOpen
	}
	defer func() {
		if errClose := f.Close(); errClose != nil {
			log.WithError(errClose).Warn("usagestore: failed to close store file")
		}
	}()

	if _, errWrite := f.Write(line); errWrite != nil {
		return errWrite
	}

	if maxBytes > 0 {
		// Best-effort pruning of the oldest lines once the file exceeds the cap.
		pruneErr := s.pruneIfOversizedLocked(path, maxBytes)
		if pruneErr != nil {
			log.WithError(pruneErr).WithField("path", path).Warn("usagestore: prune failed")
		}
	}
	return nil
}

// pruneIfOversizedLocked trims the oldest JSONL lines until the file is under maxBytes.
// It keeps whole lines (compact: skip, read, rewrite) so records stay valid.
func (s *Store) pruneIfOversizedLocked(path string, maxBytes int64) error {
	info, errStat := os.Stat(path)
	if errStat != nil {
		return errStat
	}
	if info.Size() <= maxBytes {
		return nil
	}

	data, errRead := os.ReadFile(path)
	if errRead != nil {
		return errRead
	}

	// Split into lines, dropping trailing empty line from the final newline.
	lines := splitLines(data)
	if len(lines) == 0 {
		return nil
	}

	// Drop oldest lines until under cap. Always keep at least the newest line.
	var kept [][]byte
	var size int64
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		if size > 0 && size+int64(len(line))+1 > maxBytes {
			break
		}
		kept = append(kept, line)
		size += int64(len(line)) + 1
	}
	// Reverse kept back to chronological order.
	for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
		kept[i], kept[j] = kept[j], kept[i]
	}

	buf := make([]byte, 0, int(size))
	for _, line := range kept {
		buf = append(buf, line...)
		buf = append(buf, '\n')
	}

	return os.WriteFile(path, buf, 0o600)
}

func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i := 0; i < len(data); i++ {
		if data[i] == '\n' {
			if i > start {
				lines = append(lines, append([]byte(nil), data[start:i]...))
			}
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, append([]byte(nil), data[start:]...))
	}
	return lines
}

// ReadAll decodes every stored record. It is used by Aggregate and Import merge.
func (s *Store) ReadAll() ([]StoredRecord, error) {
	if s == nil {
		return nil, nil
	}
	s.mu.Lock()
	path := s.path
	if path == "" {
		s.mu.Unlock()
		return nil, nil
	}
	s.mu.Unlock()

	data, errRead := os.ReadFile(path)
	if errRead != nil {
		if os.IsNotExist(errRead) {
			return nil, nil
		}
		return nil, errRead
	}

	lines := splitLines(data)
	records := make([]StoredRecord, 0, len(lines))
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		var rec StoredRecord
		if errUnmarshal := json.Unmarshal(line, &rec); errUnmarshal != nil {
			// Skip malformed lines rather than aborting the whole read.
			log.WithError(errUnmarshal).Warn("usagestore: skipping malformed record line")
			continue
		}
		records = append(records, rec)
	}
	return records, nil
}

// Clear truncates the store file, removing all persisted records.
func (s *Store) Clear() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	path := s.path
	s.mu.Unlock()
	if path == "" {
		return nil
	}
	if errTrunc := os.WriteFile(path, nil, 0o600); errTrunc != nil {
		return errTrunc
	}
	return nil
}

// AppendAll writes a batch of records, used by Import to merge an exported snapshot.
func (s *Store) AppendAll(records []StoredRecord) (int, error) {
	if s == nil {
		return 0, nil
	}
	s.mu.Lock()
	path := s.path
	if !s.enabled || !s.captureEnabled || path == "" {
		s.mu.Unlock()
		return 0, nil
	}
	maxBytes := s.maxSizeBytes
	s.mu.Unlock()

	added := 0
	for _, rec := range records {
		payload, errMarshal := json.Marshal(rec)
		if errMarshal != nil {
			continue
		}
		payload = append(payload, '\n')
		if errAppend := s.appendLine(path, payload, maxBytes); errAppend != nil {
			return added, errAppend
		}
		added++
	}
	return added, nil
}

// ResolveStorePath returns the absolute path for the store file.
// When path is empty, it defaults to usage.jsonl under authDir. When path is relative
// and authDir is non-empty, it is joined to authDir. ~ is expanded.
func ResolveStorePath(path, authDir string) string {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" || path == "." {
		if strings.TrimSpace(authDir) == "" {
			return ""
		}
		return filepath.Join(expandHome(authDir), "usage.jsonl")
	}
	if strings.HasPrefix(path, "~") {
		path = expandHome(path)
		return filepath.Clean(path)
	}
	if !filepath.IsAbs(path) && strings.TrimSpace(authDir) != "" {
		return filepath.Join(expandHome(authDir), path)
	}
	return path
}

// expandHome expands a leading ~ to the user's home directory.
func expandHome(path string) string {
	if !strings.HasPrefix(path, "~") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, path[2:])
	}
	return path
}

var _ = time.Time{}
