package adminui

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"go.openai.org/api/tunnel-client/pkg/config"
	tclog "go.openai.org/api/tunnel-client/pkg/log"
)

func TestHandleLogLevelGetReturnsCurrentLevel(t *testing.T) {
	t.Parallel()

	controller, err := tclog.NewLevelController(&config.LoggingConfig{
		Format: config.LogFormatStructText,
		Level:  slog.LevelInfo,
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/log-level", nil)
	rec := httptest.NewRecorder()

	handleLogLevel(controller, nil).ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp logLevelResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.Equal(t, "info", resp.Level)
	require.Equal(t, []string{"debug", "info", "warn"}, resp.SupportedLevels)
}

func TestHandleLogLevelPutUpdatesController(t *testing.T) {
	t.Parallel()

	var logBuf bytes.Buffer
	logCfg := &config.LoggingConfig{
		Format: config.LogFormatStructText,
		Level:  slog.LevelInfo,
	}

	controller, err := tclog.NewLevelController(logCfg)
	require.NoError(t, err)

	logger, closer, err := tclog.NewLoggerWithLevelController(logCfg, &logBuf, controller)
	require.NoError(t, err)
	defer tclog.CloseIfNeeded(closer)

	req := httptest.NewRequest(http.MethodPut, "/api/log-level", strings.NewReader(`{"level":"debug"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handleLogLevel(controller, logger).ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "debug", controller.LevelString())

	var resp logLevelResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.Equal(t, "debug", resp.Level)
	require.Contains(t, logBuf.String(), "runtime log level updated")

	logger.Debug("debug-line-visible")
	require.Contains(t, logBuf.String(), "debug-line-visible")
}

func TestHandleLogLevelRejectsUnsupportedLevel(t *testing.T) {
	t.Parallel()

	controller, err := tclog.NewLevelController(&config.LoggingConfig{
		Format: config.LogFormatStructText,
		Level:  slog.LevelInfo,
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPut, "/api/log-level", strings.NewReader(`{"level":"error"}`))
	rec := httptest.NewRecorder()

	handleLogLevel(controller, nil).ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "info", controller.LevelString())
	require.Contains(t, rec.Body.String(), "unsupported log level")
}

func TestDecodeJSONBodyRejectsExtraContent(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodPut, "/api/log-level", strings.NewReader(`{"level":"info"}{"extra":true}`))

	var payload updateLogLevelRequest
	err := decodeJSONBody(req, &payload)
	require.Error(t, err)
	require.NotEqual(t, io.EOF, err)
}
