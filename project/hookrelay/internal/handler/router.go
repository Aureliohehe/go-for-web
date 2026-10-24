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
	allowedEndpointURL string,
	findEndpoint EndpointFinder,
	createEndpoint EndpointCreator,
	updateEndpointEnabled EndpointEnabledUpdater,
	createEvent EventCreator,
	findDelivery DeliveryFinder,
	listAttempts AttemptLister,
	replayDelivery DeliveryReplayer,
) *gin.Engine {
	router := NewRouter(pingDatabase)

	v1 := router.Group("/v1")
	v1.Use(RequireAPIKey(apiKey))
	v1.GET("/endpoints/:id", GetEndpoint(findEndpoint))
	v1.POST("/endpoints", PostEndpoint(allowedEndpointURL, createEndpoint))
	v1.PATCH("/endpoints/:id", PatchEndpointEnabled(updateEndpointEnabled))
	v1.POST("/events", PostEvent(createEvent))
	v1.GET("/deliveries/:id", GetDelivery(findDelivery))
	v1.GET("/deliveries/:id/attempts", GetDeliveryAttempts(listAttempts))
	v1.POST("/deliveries/:id/replay", PostDeliveryReplay(replayDelivery))

	return router
}
