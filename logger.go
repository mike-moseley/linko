package main

import (
	"boot.dev/linko/internal/linkoerr"
	"bufio"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	pkgerr "github.com/pkg/errors"
)

type stackTracer interface {
	error
	StackTrace() pkgerr.StackTrace
}

func errAttrs(err error) []slog.Attr {
	attrs := []slog.Attr{slog.String("message", err.Error())}
	if stackErr, ok := errors.AsType[stackTracer](err); ok {
		attrs = append(attrs, slog.String("stack_trace", fmt.Sprintf("%+v", stackErr.StackTrace())))
	}
	return append(attrs, linkoerr.Attrs(err)...)
}

func replaceAttr(groups []string, a slog.Attr) slog.Attr {
	if a.Key == "error" {
		val := a.Value.Any()
		if multiErr, ok := val.(multiError); ok {
			var meAttrs []slog.Attr

			for i, me := range multiErr.Unwrap() {
				attrs := errAttrs(me)
				meAttrs = append(meAttrs, slog.GroupAttrs(fmt.Sprintf("error_%d", i+1), attrs...))
			}
			return slog.GroupAttrs("errors", meAttrs...)
		}
		err, ok := val.(error)
		if !ok {
			return a
		}
		return slog.GroupAttrs("error", errAttrs(err)...)
	}
	return a
}

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
		Level:       slog.LevelDebug,
		ReplaceAttr: replaceAttr,
	})
	buf := bufio.NewWriterSize(f, 8192)
	infoHandler := slog.NewJSONHandler(buf, &slog.HandlerOptions{
		Level:       slog.LevelInfo,
		ReplaceAttr: replaceAttr,
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
