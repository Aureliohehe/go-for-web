package store

import "testing"

func TestHashEventRequest(t *testing.T) {
	base := HashEventRequest(1, "order.created", []byte(`{"id":1}`))
	if base != HashEventRequest(1, "order.created", []byte(`{"id":1}`)) {
		t.Fatal("identical endpoint, type and raw bytes must have the same hash")
	}
	tests := []struct {
		name       string
		endpointID uint64
		eventType  string
		payload    []byte
	}{
		{name: "different endpoint", endpointID: 2, eventType: "order.created", payload: []byte(`{"id":1}`)},
		{name: "different type", endpointID: 1, eventType: "order.updated", payload: []byte(`{"id":1}`)},
		{name: "different payload", endpointID: 1, eventType: "order.created", payload: []byte(`{"id":2}`)},
		{name: "different JSON whitespace", endpointID: 1, eventType: "order.created", payload: []byte(`{ "id":1 }`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if base == HashEventRequest(tt.endpointID, tt.eventType, tt.payload) {
				t.Fatal("different request content must change the digest")
			}
		})
	}
	// Without field boundaries these pairs would both concatenate to "abc".
	if HashEventRequest(1, "a", []byte("bc")) == HashEventRequest(1, "ab", []byte("c")) {
		t.Fatal("event type and payload boundaries must be unambiguous")
	}
}
