package api

import (
	_ "embed"
	"net/http"

	"github.com/gin-gonic/gin"
)

// Source asset: assets/brand/favicon.ico (a copy kept in sync by tests).
//
//go:embed brand/favicon.ico
var embeddedFaviconICO []byte

// serveFavicon serves the brand favicon for the bare host and for the
// server-rendered pages (OAuth callback, safe-mode warning).
func (s *Server) serveFavicon(c *gin.Context) {
	c.Header("Cache-Control", "public, max-age=86400")
	c.Data(http.StatusOK, "image/x-icon", embeddedFaviconICO)
}
