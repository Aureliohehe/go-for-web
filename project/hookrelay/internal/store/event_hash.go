package store

import (
	"crypto/sha256"
	"encoding/binary"
)

// HashEventRequest identifies the event content protected by an idempotency key.
// Length prefixes keep field boundaries unambiguous; payload bytes are hashed
// as received, without JSON parsing or re-serialization. The key itself is not
// part of the digest: it selects the row to compare against this content.
func HashEventRequest(endpointID uint64, eventType string, payload []byte) [32]byte {
	h := sha256.New()
	var number [8]byte
	binary.BigEndian.PutUint64(number[:], endpointID)
	_, _ = h.Write(number[:])
	binary.BigEndian.PutUint64(number[:], uint64(len(eventType)))
	_, _ = h.Write(number[:])
	_, _ = h.Write([]byte(eventType))
	binary.BigEndian.PutUint64(number[:], uint64(len(payload)))
	_, _ = h.Write(number[:])
	_, _ = h.Write(payload)
	var digest [32]byte
	copy(digest[:], h.Sum(nil))
	return digest
}
