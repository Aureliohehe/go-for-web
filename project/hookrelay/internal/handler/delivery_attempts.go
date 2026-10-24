package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/aurelio/hookrelay/internal/store"
	"github.com/gin-gonic/gin"
)

type AttemptLister func(context.Context, uint64, uint64, int) ([]store.DeliveryAttempt, error)

func GetDeliveryAttempts(list AttemptLister) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := strconv.ParseUint(c.Param("id"), 10, 64)
		if err != nil || id == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_delivery_id"})
			return
		}
		after, cursorErr := strconv.ParseUint(c.DefaultQuery("after", "0"), 10, 64)
		limit, limitErr := strconv.Atoi(c.DefaultQuery("limit", "20"))
		if cursorErr != nil || limitErr != nil || limit < 1 || limit > 100 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_pagination"})
			return
		}
		attempts, err := list(c.Request.Context(), id, after, limit)
		if errors.Is(err, store.ErrDeliveryNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "delivery_not_found"})
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error"})
			return
		}
		next := ""
		if len(attempts) > limit {
			attempts = attempts[:limit]
			next = strconv.FormatUint(attempts[len(attempts)-1].ID, 10)
		}
		items := make([]gin.H, 0, len(attempts))
		for _, a := range attempts {
			items = append(items, gin.H{
				"id": strconv.FormatUint(a.ID, 10), "delivery_id": strconv.FormatUint(a.DeliveryID, 10),
				"generation": a.Generation, "attempt_number": a.AttemptNumber, "status": a.Status,
				"started_at": a.StartedAt, "finished_at": a.FinishedAt,
				"http_status": a.HTTPStatus, "error_class": a.ErrorClass, "latency_ms": a.LatencyMS,
			})
		}
		c.JSON(http.StatusOK, gin.H{"items": items, "next_after": next})
	}
}
