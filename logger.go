package main

import (
	"bufio"
	"fmt"
	"log/slog"
	"net/http"
	"os"
)

func initializeLogger(logFile string) (*slog.Logger, closeFunc, error) {
	if logFile == "" {
		logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
		return logger, func() error { return nil }, nil
	}
	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open access log: %v", err)
	}
	debugHandler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})
	buf := bufio.NewWriterSize(f, 8192)
	infoHandler := slog.NewJSONHandler(buf, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})
	logger := slog.New(slog.NewMultiHandler(
		debugHandler,
		infoHandler,
	))
	closeFn := func() error {
		if flushErr := buf.Flush(); flushErr != nil {
			f.Close()
			return fmt.Errorf("flush log buffer: %w", flushErr)
		}
		return f.Close()
	}
	return logger, closeFn, nil
}

func requestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r)
			logger.Info("Served request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.String("client_ip", r.RemoteAddr),
			)
		})
	}
}
