package adminui

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"strconv"
	"time"

	"go.openai.org/api/tunnel-client/pkg/httpguard"
)

//go:embed assets/*
var embeddedAssets embed.FS

var adminUIFS = func() fs.FS {
	sub, err := fs.Sub(embeddedAssets, "assets")
	if err != nil {
		panic(err)
	}
	return sub
}()

type logsResponse struct {
	Events []LogEvent `json:"events"`
}

func setNoCacheHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL == nil {
		http.NotFound(w, r)
		return
	}
	switch r.URL.Path {
	case "/", "/ui", "/ui/":
		// ok
	default:
		http.NotFound(w, r)
		return
	}

	setNoCacheHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)

	data, err := fs.ReadFile(adminUIFS, "index.html")
	if err != nil {
		http.Error(w, "missing ui asset index.html", http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(data)
}

func handleAssets() http.Handler {
	fsHandler := http.StripPrefix("/assets/", http.FileServer(http.FS(adminUIFS)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setNoCacheHeaders(w)
		fsHandler.ServeHTTP(w, r)
	})
}

func handleLogsJSON(buf *LogBuffer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit := parseLimit(r, 200, 5000)
		events := buf.Recent(limit)
		writeJSON(w, http.StatusOK, logsResponse{Events: events})
	}
}

func handleLogsStream(buf *LogBuffer, shutdownCtx context.Context) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		streamCtx := httpguard.MergeContexts(r.Context(), shutdownCtx)
		notify := buf.Subscribe(streamCtx)
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-streamCtx.Done():
				return
			case ev, ok := <-notify:
				if !ok {
					return
				}
				payload, err := json.Marshal(ev)
				if err != nil {
					continue
				}
				_, _ = fmt.Fprintf(w, "event: log\nid: %d\ndata: %s\n\n", ev.Seq, payload)
				flusher.Flush()
			case <-ticker.C:
				_, _ = fmt.Fprint(w, ": ping\n\n")
				flusher.Flush()
			}
		}
	}
}

func parseLimit(r *http.Request, def, max int) int {
	if r == nil || r.URL == nil {
		return def
	}
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return def
	}
	if max > 0 && n > max {
		return max
	}
	return n
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(true)
	_ = enc.Encode(v)
}
