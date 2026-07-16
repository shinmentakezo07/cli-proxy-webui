package usagestore

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.jsonl")
	s := &Store{path: path, maxSizeBytes: 1024, enabled: true, captureEnabled: true}
	return s, path
}

func sampleRecord(model, endpoint string, failed bool, ts time.Time) StoredRecord {
	return StoredRecord{
		Timestamp: ts,
		Provider:  "test",
		Model:     model,
		Endpoint:  endpoint,
		Source:    "sk-test",
		AuthIndex: "1",
		Failed:    failed,
		Tokens: TokenStats{
			InputTokens:  100,
			OutputTokens: 50,
			TotalTokens:  150,
		},
	}
}

func TestAppendAndAggregate(t *testing.T) {
	s, _ := newTestStore(t)
	ts := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)

	s.Append(sampleRecord("gpt-4", "POST /v1/chat/completions", false, ts))
	s.Append(sampleRecord("gpt-4", "POST /v1/chat/completions", true, ts.Add(time.Second)))

	out, err := s.Aggregate(0)
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if got := out["total_requests"]; got != int64(2) {
		t.Fatalf("total_requests = %v, want 2", got)
	}
	if got := out["success_count"]; got != int64(1) {
		t.Fatalf("success_count = %v, want 1", got)
	}
	if got := out["failure_count"]; got != int64(1) {
		t.Fatalf("failure_count = %v, want 1", got)
	}
	if got := out["total_tokens"]; got != int64(300) {
		t.Fatalf("total_tokens = %v, want 300", got)
	}
	apis, _ := out["apis"].(map[string]any)
	if _, ok := apis["POST /v1/chat/completions"]; !ok {
		t.Fatalf("missing endpoint entry, got %v", apis)
	}
}

func TestAggregateAfterFilter(t *testing.T) {
	s, _ := newTestStore(t)
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)

	s.Append(sampleRecord("m", "POST /v1/chat/completions", false, old))
	s.Append(sampleRecord("m", "POST /v1/chat/completions", false, recent))

	out, err := s.Aggregate(recent.UnixMilli())
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if got := out["total_requests"]; got != int64(1) {
		t.Fatalf("total_requests = %v, want 1 (after-filter)", got)
	}
}

func TestPruningKeepsNewest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.jsonl")
	// A cap big enough for a handful of records but far smaller than all 200.
	s := &Store{path: path, maxSizeBytes: 1500, enabled: true, captureEnabled: true}

	for i := 0; i < 200; i++ {
		s.Append(sampleRecord("m", "POST /v1/chat/completions", false, time.Now()))
	}

	info, errStat := os.Stat(path)
	if errStat != nil {
		t.Fatalf("stat: %v", errStat)
	}
	if info.Size() > 1500*2 {
		t.Fatalf("store not pruned: size=%d", info.Size())
	}

	out, err := s.Aggregate(0)
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if got := out["total_requests"]; got == int64(0) {
		t.Fatalf("pruning dropped everything")
	}
	// Pruning must have removed records, so far fewer than the 200 written.
	if got, _ := out["total_requests"].(int64); got >= 200 {
		t.Fatalf("store not pruned: still has %d records", got)
	}
}

func TestExportImportRoundTrip(t *testing.T) {
	s, _ := newTestStore(t)
	ts := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	s.Append(sampleRecord("gpt-4", "POST /v1/chat/completions", false, ts))

	export, err := s.Export()
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if export.Version != 1 {
		t.Fatalf("version = %d", export.Version)
	}
	if export.Usage["total_requests"] != int64(1) {
		t.Fatalf("export total = %v", export.Usage["total_requests"])
	}

	// Import into a fresh store in a new directory.
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.jsonl")
	target := &Store{path: path, maxSizeBytes: 1024, enabled: true, captureEnabled: true}

	usage := export.Usage
	res, err := target.Import(map[string]any{"usage": usage, "version": 1, "exported_at": export.ExportedAt.Format(time.RFC3339Nano)})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.Added != 1 {
		t.Fatalf("added = %d, want 1", res.Added)
	}
	if res.TotalRequests != 1 {
		t.Fatalf("total = %d, want 1", res.TotalRequests)
	}

	out, err := target.Aggregate(0)
	if err != nil {
		t.Fatalf("aggregate after import: %v", err)
	}
	if out["total_requests"] != int64(1) {
		t.Fatalf("post-import total = %v", out["total_requests"])
	}
}

func TestResolveStorePath(t *testing.T) {
	if got := ResolveStorePath("", "/data/auth"); got != "/data/auth/usage.jsonl" {
		t.Fatalf("default path = %q", got)
	}
	if got := ResolveStorePath("custom.jsonl", "/data/auth"); got != "/data/auth/custom.jsonl" {
		t.Fatalf("relative path = %q", got)
	}
	if got := ResolveStorePath("/abs/path/z.jsonl", "/data/auth"); got != "/abs/path/z.jsonl" {
		t.Fatalf("absolute path = %q", got)
	}
}

func TestGatedAppendDiscards(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.jsonl")
	s := &Store{path: path, maxSizeBytes: 1024, enabled: true, captureEnabled: false}
	s.Append(sampleRecord("m", "POST /v1/chat/completions", false, time.Now()))

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("captureEnabled=false should not create a file (err=%v)", err)
	}
}
