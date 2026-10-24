// Local test receiver only: no signature verification or persistent deduplication.
package main

import (
	"flag"
	"log"
	"net/http"
	"time"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:9090", "listen address")
	status := flag.Int("status", 200, "HTTP response status")
	delay := flag.Duration("delay", 0, "delay before response, e.g. 8s")
	flag.Parse()
	if *status < 200 || *status > 599 {
		log.Fatal("status must be 200..599")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("webhook delivery_id=%s", r.Header.Get("X-Webhook-ID"))
		timer := time.NewTimer(*delay)
		defer timer.Stop()
		select {
		case <-timer.C:
			w.WriteHeader(*status)
		case <-r.Context().Done():
		}
	})
	server := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Printf("local receiver listening on %s", *addr)
	log.Fatal(server.ListenAndServe())
}
