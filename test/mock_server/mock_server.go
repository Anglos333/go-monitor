package main

import (
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"strconv"
	"time"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:18080", "listen address")
	flag.Parse()

	mux := http.NewServeMux()

	// 正常可达：200 OK
	mux.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// 固定延迟：/slow?ms=100
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		ms := int64(100)
		if v := r.URL.Query().Get("ms"); v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				ms = n
			}
		}
		time.Sleep(time.Duration(ms) * time.Millisecond)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(fmt.Sprintf("slow %dms", ms)))
	})

	// 抖动延迟：/jitter?min=50&max=200
	mux.HandleFunc("/jitter", func(w http.ResponseWriter, r *http.Request) {
		minMs := int64(50)
		maxMs := int64(200)
		q := r.URL.Query()
		if v := q.Get("min"); v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				minMs = n
			}
		}
		if v := q.Get("max"); v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				maxMs = n
			}
		}
		if maxMs < minMs {
			maxMs = minMs
		}
		d := minMs + rand.Int63n(maxMs-minMs+1)
		time.Sleep(time.Duration(d) * time.Millisecond)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(fmt.Sprintf("jitter %dms", d)))
	})

	// 非200：/status?code=403
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		code := 403
		if v := r.URL.Query().Get("code"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				code = n
			}
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(fmt.Sprintf("status=%d", code)))
	})

	s := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("mock server listening on http://%s\n", *addr)
	log.Printf("endpoints: /ok  /slow?ms=100  /jitter?min=50&max=200  /status?code=403\n")
	log.Fatal(s.ListenAndServe())
}
