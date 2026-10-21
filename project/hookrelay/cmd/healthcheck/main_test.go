package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestShutdownHTTPWaitsForRequest(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	shutdownStarted := make(chan struct{})

	var once sync.Once
	unblock := func() {
		once.Do(func() { close(release) })
	}

	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			close(entered)
			<-release
			_, _ = io.WriteString(w, "done")
		},
	))
	// Release the handler even if an assertion fails before the request finishes.
	t.Cleanup(func() {
		unblock()
		server.Close()
	})

	server.Config.RegisterOnShutdown(func() {
		close(shutdownStarted)
	})

	waitSignal := func(ch <-chan struct{}, name string) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out waiting for %s", name)
		}
	}

	client := server.Client()
	client.Timeout = 10 * time.Second

	requestDone := make(chan error, 1)
	go func() {
		resp, err := client.Get(server.URL)
		if err != nil {
			requestDone <- err
			return
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			requestDone <- err
			return
		}
		if resp.StatusCode != http.StatusOK || string(body) != "done" {
			requestDone <- fmt.Errorf(
				"unexpected response: status=%d body=%q",
				resp.StatusCode, body,
			)
			return
		}
		requestDone <- nil
	}()

	waitSignal(entered, "request to enter handler")

	shutdownDone := make(chan error, 1)
	go func() {
		shutdownDone <- shutdownHTTP(server.Config)
	}()

	waitSignal(shutdownStarted, "shutdown to start")

	// Shutdown must not finish while the active handler is blocked.
	select {
	case err := <-shutdownDone:
		t.Fatalf("shutdown finished before request completed: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	unblock()

	select {
	case err := <-requestDone:
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for request to finish")
	}

	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("shutdown failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for shutdown to finish")
	}
}
