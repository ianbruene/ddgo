package mockgrbl

import (
	"encoding/json"
	"net/http"
	"strings"
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
	mux.HandleFunc("/hard-limit", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		axis := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("axis")))
		if axis != "X" && axis != "Y" && axis != "Z" {
			http.Error(w, "axis must be X, Y, or Z", http.StatusBadRequest)
			return
		}
		responses := c.HardLimit(axis)
		c.queueSerial(responses)
		write(w, responses)
	})
	return mux
}
