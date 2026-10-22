package handler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strconv"

	"github.com/aurelio/hookrelay/internal/store"
	"github.com/gin-gonic/gin"
)

type EndpointCreator func(context.Context, *store.Endpoint) error

// PostEndpoint accepts only an exact operator-configured URL. The secret is
// delivered once, in the successful creation response; GET never returns it.
func PostEndpoint(allowedURL string, create EndpointCreator) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input struct {
			URL string `json:"url"`
		}
		if !decodeEndpointJSON(c, &input) {
			return
		}
		if allowedURL == "" || input.URL != allowedURL {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_endpoint_url"})
			return
		}

		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error"})
			return
		}
		endpoint := store.Endpoint{
			URL: input.URL, SigningSecret: hex.EncodeToString(secret), Enabled: true,
		}
		if err := create(c.Request.Context(), &endpoint); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error"})
			return
		}
		c.JSON(http.StatusCreated, gin.H{
			"id":             strconv.FormatUint(endpoint.ID, 10),
			"url":            endpoint.URL,
			"enabled":        endpoint.Enabled,
			"created_at":     endpoint.CreatedAt,
			"signing_secret": endpoint.SigningSecret,
		})
	}
}
