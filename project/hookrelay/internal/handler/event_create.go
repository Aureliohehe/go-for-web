package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/aurelio/hookrelay/internal/service"
	"github.com/aurelio/hookrelay/internal/store"
	"github.com/gin-gonic/gin"
)

// Allow a 256 KiB payload plus the surrounding event fields.
const maxEventBodyBytes = 256*1024 + 4096

type EventCreator func(context.Context, store.EventInput) (store.EventResult, error)

func PostEvent(create EventCreator) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input struct {
			EndpointID string          `json:"endpoint_id"`
			EventType  string          `json:"event_type"`
			Payload    json.RawMessage `json:"payload"`
		}
		if !decodeJSON(c, &input, maxEventBodyBytes) {
			return
		}
		endpointID, err := strconv.ParseUint(input.EndpointID, 10, 64)
		if err != nil || endpointID == 0 || len(input.Payload) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_event"})
			return
		}

		// RawMessage preserves payload bytes for hashing and later delivery.
		// Key/type/payload limits and endpoint state are checked by the store.
		result, err := create(c.Request.Context(), store.EventInput{
			IdempotencyKey: c.GetHeader("Idempotency-Key"),
			EndpointID:     endpointID, EventType: input.EventType, Payload: input.Payload,
		})
		if errors.Is(err, service.ErrEnqueue) {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error": "event_saved_queue_unavailable", "persisted": true,
				"event_id":    strconv.FormatUint(result.EventID, 10),
				"delivery_id": strconv.FormatUint(result.DeliveryID, 10),
			})
			return
		}
		if err != nil {
			status, code := http.StatusInternalServerError, "internal_error"
			switch {
			case errors.Is(err, store.ErrInvalidEvent):
				status, code = http.StatusBadRequest, "invalid_event"
			case errors.Is(err, store.ErrEndpointNotFound):
				status, code = http.StatusNotFound, "endpoint_not_found"
			case errors.Is(err, store.ErrEndpointDisabled):
				status, code = http.StatusConflict, "endpoint_disabled"
			case errors.Is(err, store.ErrIdempotencyConflict):
				status, code = http.StatusConflict, "idempotency_conflict"
			}
			c.JSON(status, gin.H{"error": code})
			return
		}
		status := http.StatusAccepted
		if result.Reused {
			status = http.StatusOK
		}
		c.JSON(status, gin.H{
			"event_id":    strconv.FormatUint(result.EventID, 10),
			"delivery_id": strconv.FormatUint(result.DeliveryID, 10),
			"reused":      result.Reused,
		})
	}
}
