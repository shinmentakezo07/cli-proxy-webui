package usagestore

import (
	"context"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// TestPipelinePublishesAndAggregates verifies the end-to-end guarantee the UI relies on:
// when an executor publishes a coreusage.Record through the default usage manager, the
// durable store plugin captures it and it appears in the /usage Aggregate output.
func TestPipelinePublishesAndAggregates(t *testing.T) {
	dir := t.TempDir()
	store := DefaultStore()
	store.SetConfig("", dir, 100, true, true)

	// Records flow through the global default manager's plugin list. The usagestore
	// plugin registered itself via init(); ensure the dispatcher is running.
	coreusage.StartDefault(context.Background())
	t.Cleanup(func() {
		coreusage.StopDefault()
		store.SetEnabled(false)
	})

	record := coreusage.Record{
		Provider:    "claude",
		Model:       "claude-fable-5",
		Alias:       "claude-fable-5",
		APIKey:      "k:test",
		AuthIndex:   "1",
		AuthType:    "api_key",
		Source:      "t:test",
		RequestedAt: time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC),
		Detail: coreusage.Detail{
			InputTokens:  120,
			OutputTokens: 80,
			TotalTokens:  200,
		},
	}
	coreusage.PublishRecord(context.Background(), record)

	// The manager dispatches asynchronously on a background goroutine; give it a moment.
	deadline := time.Now().Add(2 * time.Second)
	for {
		out, err := store.Aggregate(0)
		if err != nil {
			t.Fatalf("aggregate: %v", err)
		}
		if out["total_requests"] == int64(1) {
			apis, _ := out["apis"].(map[string]any)
			if len(apis) == 0 {
				t.Fatalf("no apis despite one record")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("published record never reached the durable store (total=%v)", out["total_requests"])
		}
		time.Sleep(20 * time.Millisecond)
	}
}
