package task

import (
	"context"
	"testing"
)

func TestDeferRequiresAsynqContext(t *testing.T) {
	// A missing context must fail before attempting Redis I/O.
	client := &Client{}
	if _, err := client.DeferDelivery(context.Background(), 9, 1); err == nil {
		t.Fatal("deferral without queue/retry context must fail")
	}
}
