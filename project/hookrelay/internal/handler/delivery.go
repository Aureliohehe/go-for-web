package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/aurelio/hookrelay/internal/store"
	"github.com/gin-gonic/gin"
)

type DeliveryFinder func(context.Context, uint64) (store.Delivery, error)

func GetDelivery(find DeliveryFinder) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := strconv.ParseUint(c.Param("id"), 10, 64)
		if err != nil || id == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_delivery_id"})
			return
		}
		delivery, err := find(c.Request.Context(), id)
		if errors.Is(err, store.ErrDeliveryNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "delivery_not_found"})
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error"})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"id":               strconv.FormatUint(delivery.ID, 10),
			"event_id":         strconv.FormatUint(delivery.EventID, 10),
			"status":           delivery.Status,
			"generation":       delivery.Generation,
			"attempt_count":    delivery.AttemptCount,
			"last_http_status": delivery.LastHTTPStatus,
			"last_error":       delivery.LastError,
			"created_at":       delivery.CreatedAt,
			"updated_at":       delivery.UpdatedAt,
		})
	}
}
