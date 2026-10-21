package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// NewRouter accepts a narrow dependency so health checks can be unit tested
// without a real database. Production passes sql.DB.PingContext.
func NewRouter(pingDatabase func(context.Context) error) *gin.Engine {
	router := gin.New()
	router.Use(gin.Logger(), gin.Recovery())

	router.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	router.GET("/readyz", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), time.Second)
		defer cancel()
		if err := pingDatabase(ctx); err != nil {
			// Do not expose driver errors or connection settings to clients.
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not_ready"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ready"})
	})

	return router
}

func NewAPIRouter(
	pingDatabase func(context.Context) error,
	apiKey string,
	findEndpoint EndpointFinder,
) *gin.Engine {
	router := NewRouter(pingDatabase)

	v1 := router.Group("/v1")
	v1.Use(RequireAPIKey(apiKey))
	v1.GET("/endpoints/:id", GetEndpoint(findEndpoint))

	return router
}
