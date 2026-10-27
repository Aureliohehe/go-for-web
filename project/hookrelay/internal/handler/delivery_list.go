package handler

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/aurelio/hookrelay/internal/store"
	"github.com/gin-gonic/gin"
)

type DeliveryLister func(context.Context, store.DeliveryFilter) ([]store.Delivery, error)

func GetDeliveries(list DeliveryLister) gin.HandlerFunc {
	return func(c *gin.Context) {
		query, err := url.ParseQuery(c.Request.URL.RawQuery)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_delivery_filter"})
			return
		}
		for key, values := range query {
			if (key != "status" && key != "endpoint_id" && key != "after" && key != "limit") || len(values) != 1 {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_delivery_filter"})
				return
			}
		}
		// Defaults apply only when omitted; an explicitly empty ID/limit is invalid.
		if !query.Has("after") {
			query.Set("after", "0")
		}
		if !query.Has("limit") {
			query.Set("limit", "20")
		}
		after, cursorErr := strconv.ParseUint(query.Get("after"), 10, 64)
		limit, limitErr := strconv.Atoi(query.Get("limit"))
		if cursorErr != nil || limitErr != nil || limit < 1 || limit > 100 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_pagination"})
			return
		}
		filter := store.DeliveryFilter{Status: query.Get("status"), After: after, Limit: limit}
		if query.Has("endpoint_id") {
			id, err := strconv.ParseUint(query.Get("endpoint_id"), 10, 64)
			if err != nil || id == 0 {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_endpoint_id"})
				return
			}
			filter.EndpointID = id
		}
		if err := filter.Validate(); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_delivery_filter"})
			return
		}

		// Limit the cost of sparse filters over growing historical data. The
		// query does not lock deliveries or require a separate COUNT query.
		ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
		defer cancel()
		rows, err := list(ctx, filter)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error"})
			return
		}
		next := ""
		if len(rows) > limit {
			rows = rows[:limit]
			next = strconv.FormatUint(rows[len(rows)-1].ID, 10)
		}
		items := make([]gin.H, 0, len(rows))
		for _, row := range rows {
			items = append(items, deliveryJSON(row))
		}
		c.JSON(http.StatusOK, gin.H{"items": items, "next_after": next})
	}
}
