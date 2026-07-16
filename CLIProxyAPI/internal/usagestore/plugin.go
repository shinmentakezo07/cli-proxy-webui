package usagestore

import (
	"context"
	"net/http"
	"strings"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// init registers the durable usage store as a plugin on the global usage manager, so
// every usage record published by the executors is also persisted to disk.
func init() {
	coreusage.RegisterPlugin(&usageStorePlugin{})
}

type usageStorePlugin struct{}

func (p *usageStorePlugin) HandleUsage(ctx context.Context, record coreusage.Record) {
	if p == nil {
		return
	}

	timestamp := record.RequestedAt
	if timestamp.IsZero() {
		timestamp = time.Now()
	}

	modelName := strings.TrimSpace(record.Model)
	if modelName == "" {
		modelName = "unknown"
	}
	aliasName := strings.TrimSpace(record.Alias)
	if aliasName == "" {
		aliasName = modelName
	}
	provider := strings.TrimSpace(record.Provider)
	if provider == "" {
		provider = "unknown"
	}
	executorType := strings.TrimSpace(record.ExecutorType)
	if executorType == "" {
		executorType = "unknown"
	}
	authType := strings.TrimSpace(record.AuthType)
	if authType == "" {
		authType = "unknown"
	}
	apiKey := strings.TrimSpace(record.APIKey)
	authIndex := strings.TrimSpace(record.AuthIndex)
	requestID := strings.TrimSpace(internallogging.GetRequestID(ctx))

	reasoningEffort := strings.TrimSpace(record.ReasoningEffort)
	if reasoningEffort == "" {
		reasoningEffort = coreusage.ReasoningEffortFromContext(ctx)
	}
	serviceTier := strings.TrimSpace(record.ServiceTier)
	if serviceTier == "" {
		serviceTier = strings.TrimSpace(record.RequestServiceTier)
	}
	if serviceTier == "" {
		serviceTier = coreusage.ServiceTierFromContext(ctx)
	}

	tokens := TokenStats{
		InputTokens:         record.Detail.InputTokens,
		OutputTokens:        record.Detail.OutputTokens,
		ReasoningTokens:     record.Detail.ReasoningTokens,
		CachedTokens:        record.Detail.CachedTokens,
		CacheReadTokens:     record.Detail.CacheReadTokens,
		CacheCreationTokens: record.Detail.CacheCreationTokens,
		TotalTokens:         record.Detail.TotalTokens,
	}
	if tokens.TotalTokens == 0 {
		tokens.TotalTokens = tokens.InputTokens + tokens.OutputTokens + tokens.ReasoningTokens
	}
	if tokens.TotalTokens == 0 {
		tokens.TotalTokens = tokens.InputTokens + tokens.OutputTokens + tokens.ReasoningTokens + tokens.CachedTokens
	}

	failed := record.Failed
	if !failed {
		failed = !resolveSuccess(ctx)
	}
	statusCode, reason := resolveFail(ctx, record, failed)

	stored := StoredRecord{
		Timestamp:           timestamp,
		Provider:            provider,
		ExecutorType:        executorType,
		Model:               modelName,
		Alias:               aliasName,
		Endpoint:            resolveEndpoint(ctx),
		AuthType:            authType,
		APIKey:              apiKey,
		AuthIndex:           authIndex,
		RequestID:           requestID,
		Source:              strings.TrimSpace(record.Source),
		ReasoningEffort:     reasoningEffort,
		ServiceTier:         serviceTier,
		ResponseServiceTier: strings.TrimSpace(record.ResponseServiceTier),
		LatencyMs:           record.Latency.Milliseconds(),
		TTFTMs:              record.TTFT.Milliseconds(),
		Failed:              failed,
		Generate:            coreusage.GenerateEnabled(record.Generate),
		StatusCode:          statusCode,
		Reason:              reason,
		Tokens:              tokens,
	}

	DefaultStore().Append(stored)
}

const httpStatusBadRequest = 400

func resolveSuccess(ctx context.Context) bool {
	status := internallogging.GetResponseStatus(ctx)
	if status == 0 {
		return true
	}
	return status < httpStatusBadRequest
}

// resolveFail mirrors redisqueue.resolveFail semantics: report the final HTTP failure
// metadata, falling back to the contextual response status when the record lacks one.
func resolveFail(ctx context.Context, record coreusage.Record, failed bool) (int, string) {
	if !failed {
		return 200, ""
	}
	statusCode := record.Fail.StatusCode
	body := strings.TrimSpace(record.Fail.Body)
	if statusCode <= 0 {
		statusCode = internallogging.GetResponseStatus(ctx)
	}
	if statusCode <= 0 {
		statusCode = 500
	}
	return statusCode, body
}

func resolveEndpoint(ctx context.Context) string {
	return strings.TrimSpace(internallogging.GetEndpoint(ctx))
}

// ensure http.Header is referenced so the import stays valid even if future edits drop the
// contextual response-header usage.
var _ http.Header
