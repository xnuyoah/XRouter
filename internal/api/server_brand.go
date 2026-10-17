package api

import (
	_ "embed"
	"net/http"

	"github.com/gin-gonic/gin"
)

// 源资源：assets/brand/favicon.ico（测试会保持一份同步副本）。
//
//go:embed brand/favicon.ico
var embeddedFaviconICO []byte

// serveFavicon 为裸主机以及
// 服务端渲染页面（OAuth 回调、安全模式警告）提供品牌 favicon。
func (s *Server) serveFavicon(c *gin.Context) {
	c.Header("Cache-Control", "public, max-age=86400")
	c.Data(http.StatusOK, "image/x-icon", embeddedFaviconICO)
}
