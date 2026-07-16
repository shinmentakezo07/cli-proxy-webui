package usagestore

import (
	"strings"
	"time"
)

// Aggregate builds the usage snapshot consumed by the web UI's collectUsageDetails.
// Shape:
//
//	{
//	  "total_requests": int,
//	  "success_count": int,
//	  "failure_count": int,
//	  "total_tokens": int,
//	  "apis": {
//	    "<endpoint>": {
//	      "total_requests": int, "success_count": int, "failure_count": int, "total_tokens": int,
//	      "models": {
//	        "<model>": { "details": [ {timestamp, source, auth_index, latency_ms, tokens:{...}, failed} ] }
//	      }
//	    }
//	  }
//	}
//
// afterMs filters out records whose timestamp is older than the given epoch ms (0 = no filter).
func (s *Store) Aggregate(afterMs int64) (map[string]any, error) {
	records, errRead := s.ReadAll()
	if errRead != nil {
		return nil, errRead
	}

	totalReqs := int64(0)
	success := int64(0)
	failure := int64(0)
	totalTokens := int64(0)
	apis := map[string]any{}

	for _, rec := range records {
		if afterMs > 0 {
			if rec.Timestamp.IsZero() {
				continue
			}
			if rec.Timestamp.UnixMilli() < afterMs {
				continue
			}
		}

		endpoint := endpointKey(rec.Endpoint)
		apiEntryAny, ok := apis[endpoint]
		var apiEntry map[string]any
		if !ok || apiEntryAny == nil {
			apiEntry = newAPIEntry()
			apis[endpoint] = apiEntry
		} else {
			apiEntry, _ = apiEntryAny.(map[string]any)
		}

		modelsAny, _ := apiEntry["models"].(map[string]any)
		if modelsAny == nil {
			modelsAny = map[string]any{}
			apiEntry["models"] = modelsAny
		}

		modelName := rec.Model
		if strings.TrimSpace(modelName) == "" {
			modelName = "unknown"
		}
		modelEntryAny, ok := modelsAny[modelName]
		var modelEntry map[string]any
		if !ok || modelEntryAny == nil {
			modelEntry = map[string]any{}
			modelsAny[modelName] = modelEntry
		} else {
			modelEntry, _ = modelEntryAny.(map[string]any)
		}

		detailsAny, _ := modelEntry["details"].([]any)
		detail := buildDetail(rec)
		detailsAny = append(detailsAny, any(detail))
		modelEntry["details"] = detailsAny

		totalReqs++
		apiEntry["total_requests"] = bumpInt(apiEntry["total_requests"]) + 1
		if rec.Failed {
			failure++
			apiEntry["failure_count"] = bumpInt(apiEntry["failure_count"]) + 1
		} else {
			success++
			apiEntry["success_count"] = bumpInt(apiEntry["success_count"]) + 1
		}
		tok := rec.Tokens.TotalTokens
		if tok == 0 {
			tok = rec.Tokens.InputTokens + rec.Tokens.OutputTokens + rec.Tokens.ReasoningTokens + rec.Tokens.CachedTokens
		}
		totalTokens += tok
		apiEntry["total_tokens"] = bumpInt(apiEntry["total_tokens"]) + tok
	}

	return map[string]any{
		"total_requests": totalReqs,
		"success_count":  success,
		"failure_count":  failure,
		"total_tokens":   totalTokens,
		"apis":           apis,
	}, nil
}

func newAPIEntry() map[string]any {
	return map[string]any{
		"total_requests": int64(0),
		"success_count":  int64(0),
		"failure_count":  int64(0),
		"total_tokens":   int64(0),
		"models":         map[string]any{},
	}
}

func bumpInt(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	default:
		return 0
	}
}

func endpointKey(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint != "" {
		return endpoint
	}
	return "unknown"
}

func buildDetail(rec StoredRecord) map[string]any {
	detail := map[string]any{
		"timestamp":  rec.Timestamp.UTC().Format(time.RFC3339Nano),
		"source":     rec.Source,
		"auth_index": rec.AuthIndex,
		"failed":     rec.Failed,
		"tokens": map[string]any{
			"input_tokens":     rec.Tokens.InputTokens,
			"output_tokens":    rec.Tokens.OutputTokens,
			"reasoning_tokens": rec.Tokens.ReasoningTokens,
			"cached_tokens":    rec.Tokens.CachedTokens,
			"cache_tokens":     rec.Tokens.CacheReadTokens,
			"total_tokens":     rec.Tokens.TotalTokens,
		},
	}
	if rec.LatencyMs > 0 {
		detail["latency_ms"] = rec.LatencyMs
	}
	return detail
}

// ExportSnapshot is the payload returned by the export endpoint.
type ExportSnapshot struct {
	Version    int            `json:"version"`
	ExportedAt time.Time      `json:"exported_at"`
	Usage      map[string]any `json:"usage"`
}

// Export returns the full aggregate wrapped as an exportable snapshot.
func (s *Store) Export() (*ExportSnapshot, error) {
	usage, err := s.Aggregate(0)
	if err != nil {
		return nil, err
	}
	return &ExportSnapshot{
		Version:    1,
		ExportedAt: time.Now().UTC(),
		Usage:      usage,
	}, nil
}

// ImportResult summarizes a merge of an exported snapshot into the store.
type ImportResult struct {
	Added          int   `json:"added"`
	Skipped        int   `json:"skipped"`
	TotalRequests  int64 `json:"total_requests"`
	FailedRequests int64 `json:"failed_requests"`
}

// Import merges an exported snapshot payload into the durable store. It walks the
// apis{endpoint{models{model{details[]}}}} tree, converting each detail back into a
// StoredRecord before appending. Malformed details are counted as skipped.
func (s *Store) Import(payload map[string]any) (*ImportResult, error) {
	result := &ImportResult{}

	usageAny, ok := payload["usage"]
	if !ok {
		// Accept a top-level apis payload too.
		usageAny, ok = payload["apis"]
	}
	usage, _ := usageAny.(map[string]any)
	apis, _ := usage["apis"].(map[string]any)
	if apis == nil {
		return result, nil
	}

	var records []StoredRecord
	for endpoint, apiAny := range apis {
		api, ok := apiAny.(map[string]any)
		if !ok {
			continue
		}
		modelsAny, _ := api["models"].(map[string]any)
		for model, modelAny := range modelsAny {
			modelEntry, ok := modelAny.(map[string]any)
			if !ok {
				continue
			}
			detailsAny, _ := modelEntry["details"].([]any)
			for _, detailAny := range detailsAny {
				detail, ok := detailAny.(map[string]any)
				if !ok {
					result.Skipped++
					continue
				}
				rec, ok := recordFromDetail(endpoint, model, detail)
				if !ok {
					result.Skipped++
					continue
				}
				records = append(records, rec)
			}
		}
	}

	added, errAppend := s.AppendAll(records)
	if errAppend != nil {
		return result, errAppend
	}
	result.Added = added
	for _, rec := range records {
		result.TotalRequests++
		if rec.Failed {
			result.FailedRequests++
		}
	}
	return result, nil
}

func recordFromDetail(endpoint, model string, detail map[string]any) (StoredRecord, bool) {
	timestampStr, _ := detail["timestamp"].(string)
	var ts time.Time
	if timestampStr != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, timestampStr); err == nil {
			ts = parsed
		} else if parsed, err := time.Parse(time.RFC3339, timestampStr); err == nil {
			ts = parsed
		}
	}
	if ts.IsZero() {
		ts = time.Now().UTC()
	}

	source, _ := detail["source"].(string)
	var authIndex string
	switch v := detail["auth_index"].(type) {
	case string:
		authIndex = v
	case float64:
		authIndex = strings.TrimSpace(formatInt(int64(v)))
	}
	failed, _ := detail["failed"].(bool)

	var tokens TokenStats
	if t, ok := detail["tokens"].(map[string]any); ok {
		tokens = tokenStatsFromMap(t)
	} else {
		return StoredRecord{}, false
	}

	var latencyMs int64
	if v, ok := detail["latency_ms"].(float64); ok {
		latencyMs = int64(v)
	}

	return StoredRecord{
		Timestamp: ts,
		Endpoint:  endpoint,
		Model:     model,
		Source:    source,
		AuthIndex: authIndex,
		LatencyMs: latencyMs,
		Failed:    failed,
		Tokens:    tokens,
	}, true
}

func tokenStatsFromMap(t map[string]any) TokenStats {
	return TokenStats{
		InputTokens:         toInt64(t["input_tokens"]),
		OutputTokens:        toInt64(t["output_tokens"]),
		ReasoningTokens:     toInt64(t["reasoning_tokens"]),
		CachedTokens:        toInt64(t["cached_tokens"]),
		CacheReadTokens:     toInt64(t["cache_read_tokens"]),
		CacheCreationTokens: toInt64(t["cache_creation_tokens"]),
		TotalTokens:         toInt64(t["total_tokens"]),
	}
}

func toInt64(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	default:
		return 0
	}
}

func formatInt(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		return "-" + string(digits)
	}
	return string(digits)
}
