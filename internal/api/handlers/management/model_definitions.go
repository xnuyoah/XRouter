package management

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// GetStaticModelDefinitions returns static model metadata for a given channel.
// Channel is provided via path param (:channel) or query param (?channel=...).
// Provider model defaults now live in each executor/config family, so the bundled
// catalog has no static sections to serve.
func (h *Handler) GetStaticModelDefinitions(c *gin.Context) {
	channel := strings.TrimSpace(c.Param("channel"))
	if channel == "" {
		channel = strings.TrimSpace(c.Query("channel"))
	}
	if channel == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "channel is required"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"channel": strings.ToLower(channel),
		"models":  []any{},
	})
}
