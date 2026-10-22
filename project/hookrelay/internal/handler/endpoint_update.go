package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/aurelio/hookrelay/internal/store"
	"github.com/gin-gonic/gin"
)

type EndpointEnabledUpdater func(context.Context, uint64, bool) (store.Endpoint, error)

func PatchEndpointEnabled(update EndpointEnabledUpdater) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := parseEndpointID(c)
		if !ok {
			return
		}
		// A pointer distinguishes false from an omitted or null value.
		var input struct {
			Enabled *bool `json:"enabled"`
		}
		if !decodeEndpointJSON(c, &input) {
			return
		}
		if input.Enabled == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
			return
		}

		endpoint, err := update(c.Request.Context(), id, *input.Enabled)
		if errors.Is(err, store.ErrEndpointNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "endpoint_not_found"})
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error"})
			return
		}
		c.JSON(http.StatusOK, endpointResponse(endpoint))
	}
}
