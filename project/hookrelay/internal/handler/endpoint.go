package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/aurelio/hookrelay/internal/store"
	"github.com/gin-gonic/gin"
)

type EndpointFinder func(context.Context, uint64) (store.Endpoint, error)

func GetEndpoint(find EndpointFinder) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := strconv.ParseUint(c.Param("id"), 10, 64)
		if err != nil || id == 0 {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "invalid_endpoint_id",
			})
			return
		}

		endpoint, err := find(c.Request.Context(), id)
		if errors.Is(err, store.ErrEndpointNotFound) {
			c.JSON(http.StatusNotFound, gin.H{
				"error": "endpoint_not_found",
			})
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "internal_error",
			})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"id":         strconv.FormatUint(endpoint.ID, 10),
			"url":        endpoint.URL,
			"enabled":    endpoint.Enabled,
			"created_at": endpoint.CreatedAt,
		})
	}
}
