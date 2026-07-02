package adminui

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	tclog "go.openai.org/api/tunnel-client/pkg/log"
)

type logLevelResponse struct {
	Level           string   `json:"level"`
	SupportedLevels []string `json:"supported_levels,omitempty"`
}

type updateLogLevelRequest struct {
	Level string `json:"level"`
}

func handleLogLevel(controller *tclog.LevelController, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if controller == nil {
			http.Error(w, "runtime log level control unavailable", http.StatusInternalServerError)
			return
		}

		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, buildLogLevelResponse(controller))
		case http.MethodPut:
			var req updateLogLevelRequest
			if err := decodeJSONBody(r, &req); err != nil {
				http.Error(w, fmt.Sprintf("decode request: %v", err), http.StatusBadRequest)
				return
			}

			previousLevel := controller.LevelString()
			nextLevel, err := controller.SetString(req.Level)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}

			if logger != nil {
				logger.With(slog.String(tclog.FieldComponent, "adminui")).LogAttrs(
					r.Context(),
					nextLevel,
					"runtime log level updated",
					slog.String("previous_level", previousLevel),
					slog.String("new_level", controller.LevelString()),
				)
			}

			writeJSON(w, http.StatusOK, buildLogLevelResponse(controller))
		default:
			w.Header().Set("Allow", "GET, PUT")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func buildLogLevelResponse(controller *tclog.LevelController) logLevelResponse {
	return logLevelResponse{
		Level:           controller.LevelString(),
		SupportedLevels: tclog.SupportedRuntimeLogLevels(),
	}
}

func decodeJSONBody(r *http.Request, dst any) error {
	if r == nil || r.Body == nil {
		return fmt.Errorf("request body is required")
	}
	defer closeRequestBody(r.Body)

	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}

	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("request body must contain exactly one JSON object")
		}
		return err
	}

	return nil
}

func closeRequestBody(body io.Closer) {
	if body == nil {
		return
	}
	if err := body.Close(); err != nil {
		return
	}
}
