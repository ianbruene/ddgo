package mockgrbl

import (
	"encoding/json"
	"net/http"
)

func DebugHandler(c *Controller) http.Handler {
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("/state", func(w http.ResponseWriter, r *http.Request) { write(w, c.Snapshot()) })
	mux.HandleFunc("/commands", func(w http.ResponseWriter, r *http.Request) { write(w, c.Commands()) })
	mux.HandleFunc("/responses", func(w http.ResponseWriter, r *http.Request) { write(w, c.Responses()) })
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) { write(w, c.Events()) })
	mux.HandleFunc("/profile", func(w http.ResponseWriter, r *http.Request) { write(w, c.Profile()) })
	mux.HandleFunc("/reset", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			return
		}
		write(w, c.Reset())
	})
	return mux
}
