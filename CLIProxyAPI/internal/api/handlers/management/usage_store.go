package management

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usagestore"
)

const usageStoreTimeout = 60 * time.Second

// GetUsage returns the persisted usage aggregate consumed by the web UI's
// collectUsageDetails. The aggregate is computed on demand from the durable store.
//
// Query: after=<epoch ms | RFC3339> — only include records newer than this time.
func (h *Handler) GetUsage(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}

	afterMs, errAfter := parseUsageAfter(c.Query("after"))
	if errAfter != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errAfter.Error()})
		return
	}

	out, err := usagestore.DefaultStore().Aggregate(afterMs)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read usage store"})
		return
	}
	c.JSON(http.StatusOK, out)
}

// ExportUsage returns a downloadable snapshot of the full usage store.
func (h *Handler) ExportUsage(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}
	snapshot, err := usagestore.DefaultStore().Export()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to export usage store"})
		return
	}
	c.JSON(http.StatusOK, snapshot)
}

// ImportUsage merges an exported snapshot into the durable store.
func (h *Handler) ImportUsage(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}

	var payload map[string]any
	if errBind := c.ShouldBindJSON(&payload); errBind != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid usage snapshot payload"})
		return
	}
	if payload == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "empty usage snapshot payload"})
		return
	}

	result, err := usagestore.DefaultStore().Import(payload)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to import usage snapshot"})
		return
	}
	c.JSON(http.StatusOK, result)
}

// DeleteUsage clears all persisted usage records.
func (h *Handler) DeleteUsage(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}
	if err := usagestore.DefaultStore().Clear(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to clear usage store"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// parseUsageAfter accepts an epoch-ms integer or an RFC3339 timestamp. Empty means no filter.
func parseUsageAfter(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	if n, err := strconv.ParseInt(value, 10, 64); err == nil {
		return n, nil
	}
	parsed, errParse := time.Parse(time.RFC3339, value)
	if errParse != nil {
		return 0, errParse
	}
	return parsed.UnixMilli(), nil
}
