package management

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// GetStaticModelDefinitions 返回指定 channel 的静态模型元数据。
// channel 通过路径参数（:channel）或查询参数（?channel=...）提供。
// 供应商模型默认值现在位于各执行器/配置族中，因此内置
// 目录没有可提供的静态区段。
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
