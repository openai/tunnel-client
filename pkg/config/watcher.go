package config

import (
	"context"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"go.openai.org/api/tunnel-client/pkg/tlsconfig"
)

// Watcher monitors specific files for modifications.
// It is resilient to atomic saves by watching the parent directory and filtering
// events by filename.
type Watcher struct {
	watcher *fsnotify.Watcher
	files   map[string]struct{}
	logger  *slog.Logger
	mu      sync.Mutex
}

// NewWatcher creates a new configuration file watcher.
func NewWatcher(logger *slog.Logger) (*Watcher, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return &Watcher{
		watcher: w,
		files:   make(map[string]struct{}),
		logger:  logger,
	}, nil
}

// Add adds a file to be watched. It actually watches the parent directory.
func (w *Watcher) Add(file string) error {
	if file == "" {
		return nil
	}
	file = filepath.Clean(file)
	dir := filepath.Dir(file)
	w.mu.Lock()
	w.files[file] = struct{}{}
	w.mu.Unlock()
	return w.watcher.Add(dir)
}

// Close stops the watcher.
func (w *Watcher) Close() error {
	return w.watcher.Close()
}

// Start runs the watcher and invokes onChange when a watched file is modified.
// It debounces rapid events (e.g. from atomic saves) to a single callback.
func (w *Watcher) Start(ctx context.Context, onChange func()) {
	var timer *time.Timer
	debounceDuration := 500 * time.Millisecond

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			// Only care about Write or Create events
			if !event.Has(fsnotify.Write) && !event.Has(fsnotify.Create) {
				continue
			}

			w.mu.Lock()
			_, watching := w.files[filepath.Clean(event.Name)]
			w.mu.Unlock()

			if !watching {
				continue
			}

			if w.logger != nil {
				w.logger.Debug("config file changed on disk", slog.String("file", event.Name))
			}

			if timer != nil {
				timer.Stop()
			}
			timer = time.AfterFunc(debounceDuration, func() {
				onChange()
			})
		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			if w.logger != nil {
				w.logger.Error("config file watcher error", slog.String("error", err.Error()))
			}
		}
	}
}

// ReloadDynamicMCPConfig parses the provided configuration file (typically a YAML profile)
// and extracts the resolved TLS bundle and HTTP proxy settings for the MCP client.
// It leverages the same environment mapping logic used during process startup.
func ReloadDynamicMCPConfig(configFile string, lookupEnv func(string) (string, bool)) (*tlsconfig.Bundle, *url.URL, error) {
	if configFile == "" {
		return nil, nil, nil
	}

	data, err := os.ReadFile(configFile)
	if err != nil {
		return nil, nil, err
	}

	cfg, err := parseFileConfig(configFile, data)
	if err != nil {
		return nil, nil, err
	}

	env, err := cfg.toEnv(lookupEnv)
	if err != nil {
		return nil, nil, err
	}

	mergedLookup := lookupEnvWithFileValues(lookupEnv, &fileConfigValues{Env: env})

	caBundlePath, _ := mergedLookup("CA_BUNDLE")
	proxyStr, _ := mergedLookup("MCP_HTTP_PROXY")
	if proxyStr == "" {
		proxyStr, _ = mergedLookup("TUNNEL_CLIENT_HTTP_PROXY")
	}

	var bundle *tlsconfig.Bundle
	if caBundlePath != "" {
		resolvedPath, err := resolvePathReference("ca-bundle", caBundlePath, mergedLookup)
		if err != nil {
			return nil, nil, err
		}
		bundle, err = tlsconfig.LoadBundle(resolvedPath)
		if err != nil {
			return nil, nil, err
		}
	}

	var proxyURL *url.URL
	if proxyStr != "" {
		proxyURL, err = url.Parse(proxyStr)
		if err != nil {
			return nil, nil, err
		}
	}

	return bundle, proxyURL, nil
}
