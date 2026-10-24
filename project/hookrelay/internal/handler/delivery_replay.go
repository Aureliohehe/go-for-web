package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/aurelio/hookrelay/internal/store"
	"github.com/gin-gonic/gin"
)

type DeliveryReplayer func(context.Context, uint64) (store.Delivery, error)

func PostDeliveryReplay(replay DeliveryReplayer) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := strconv.ParseUint(c.Param("id"), 10, 64)
		if err != nil || id == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_delivery_id"})
			return
		}
		d, err := replay(c.Request.Context(), id)
		if err != nil {
			status, code := http.StatusInternalServerError, "internal_error"
			switch {
			case errors.Is(err, store.ErrDeliveryNotFound):
				status, code = http.StatusNotFound, "delivery_not_found"
			case errors.Is(err, store.ErrDeliveryNotReplayable):
				status, code = http.StatusConflict, "delivery_not_replayable"
			case errors.Is(err, store.ErrEndpointDisabled):
				status, code = http.StatusConflict, "endpoint_disabled"
			case errors.Is(err, store.ErrBacklogFull):
				c.Header("Retry-After", "5")
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "backlog_full", "replayed": false})
				return
			}
			c.JSON(status, gin.H{"error": code})
			return
		}
		c.JSON(http.StatusAccepted, gin.H{
			"delivery_id": strconv.FormatUint(d.ID, 10), "event_id": strconv.FormatUint(d.EventID, 10),
			"generation": d.Generation, "status": d.Status,
		})
	}
}
