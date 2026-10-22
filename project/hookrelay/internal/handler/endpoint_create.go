package handler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"

	"github.com/aurelio/hookrelay/internal/store"
	"github.com/gin-gonic/gin"
)

const maxEndpointBodyBytes = 4096

type EndpointCreator func(context.Context, *store.Endpoint) error

// PostEndpoint accepts only an exact operator-configured URL. The secret is
// delivered once, in the successful creation response; GET never returns it.
func PostEndpoint(allowedURL string, create EndpointCreator) gin.HandlerFunc {
	return func(c *gin.Context) {
		mediaType, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
		if err != nil || mediaType != "application/json" {
			c.JSON(http.StatusUnsupportedMediaType, gin.H{"error": "unsupported_media_type"})
			return
		}

		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxEndpointBodyBytes)
		decoder := json.NewDecoder(c.Request.Body)
		decoder.DisallowUnknownFields()
		var input struct {
			URL string `json:"url"`
		}
		if err := decoder.Decode(&input); err != nil {
			writeEndpointDecodeError(c, err)
			return
		}
		// A second decode rejects trailing JSON objects, arrays, or oversized padding.
		if err := decoder.Decode(new(any)); err != io.EOF {
			if err == nil {
				err = errors.New("trailing JSON value")
			}
			writeEndpointDecodeError(c, err)
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

func writeEndpointDecodeError(c *gin.Context, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "request_too_large"})
		return
	}
	c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
}
