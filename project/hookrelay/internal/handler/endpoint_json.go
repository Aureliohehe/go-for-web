package handler

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/gin-gonic/gin"
)

const maxEndpointBodyBytes = 4096

// decodeEndpointJSON is shared by POST and PATCH so they enforce the same
// content type, size limit and single strict JSON value.
func decodeEndpointJSON(c *gin.Context, input any) bool {
	return decodeJSON(c, input, maxEndpointBodyBytes)
}

func decodeJSON(c *gin.Context, input any, maxBytes int64) bool {
	mediaType, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || mediaType != "application/json" {
		c.JSON(http.StatusUnsupportedMediaType, gin.H{"error": "unsupported_media_type"})
		return false
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(input); err != nil {
		writeEndpointDecodeError(c, err)
		return false
	}
	// Reject trailing JSON values or oversized padding after the first value.
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err == nil {
			err = errors.New("trailing JSON value")
		}
		writeEndpointDecodeError(c, err)
		return false
	}
	return true
}

func writeEndpointDecodeError(c *gin.Context, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "request_too_large"})
		return
	}
	c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
}
