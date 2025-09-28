package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
)

// A lightweight local receiver for testing webhook payloads.
// Usage:
//
//	go run ./tools/webhook_receiver.go  (defaults to :8080 and path /mail )
//
// Env:
//
//	PORT=9090 PATH=/x-hook SAVE=1
//
// If SAVE=1 it will append pretty JSON into received.jsonl
func main() {
	port := getenv("PORT", "8080")
	path := getenv("PATH", "/mail")
	save := os.Getenv("SAVE") == "1"
	log.Printf("[receiver] listening on :%s path=%s save=%v", port, path, save)

	http.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		body, _ := io.ReadAll(r.Body)
		var generic any
		if err := json.Unmarshal(body, &generic); err != nil {
			log.Printf("[receiver] invalid json: %v raw=%s", err, string(body))
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("invalid json"))
			return
		}
		pretty, _ := json.MarshalIndent(generic, "", "  ")
		log.Print("===== Webhook Received =====")
		log.Println(string(pretty))
		if save {
			f, err := os.OpenFile("received.jsonl", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
			if err == nil {
				_, _ = f.Write(pretty)
				_, _ = f.Write([]byte("\n"))
				f.Close()
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	log.Fatal(http.ListenAndServe(":"+port, nil))
}

func getenv(k, def string) string {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	return v
}
