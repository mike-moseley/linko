package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"time"

	"boot.dev/linko/internal/linkoerr"

	"github.com/lmittmann/tint"
	"github.com/mattn/go-isatty"
	"github.com/natefinch/lumberjack"
	pkgerr "github.com/pkg/errors"
)

type stackTracer interface {
	error
	StackTrace() pkgerr.StackTrace
}

type spyReadCloser struct {
	io.ReadCloser
	bytesRead int
}

func (r *spyReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	r.bytesRead += n
	return n, err
}

type spyResponseWriter struct {
	http.ResponseWriter
	bytesWritten int
	statusCode   int
}

func (w *spyResponseWriter) Write(p []byte) (int, error) {
	if w.statusCode == 0 {
		w.statusCode = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytesWritten += n
	return n, err
}

func (w *spyResponseWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
}

const logContextKey contextKey = "log_context"

type LogContext struct {
	Username string
	Error    error
}

func errAttrs(err error) []slog.Attr {
	attrs := []slog.Attr{slog.String("message", err.Error())}
	if stackErr, ok := errors.AsType[stackTracer](err); ok {
		attrs = append(attrs, slog.String("stack_trace", fmt.Sprintf("%+v", stackErr.StackTrace())))
	}
	return append(attrs, linkoerr.Attrs(err)...)
}

func replaceAttr(groups []string, a slog.Attr) slog.Attr {
	sensitiveKeys := []string{"user", "password", "key", "apikey", "secret", "pin", "creditcardno"}
	var errs []error
	if val, err := url.Parse(a.Value.String()); err != nil {
		errs = append(errs, err)
	} else if _, ok := val.User.Password(); ok {
		val.User = url.UserPassword(val.User.Username(), "[REDACTED]")
		a.Value = slog.StringValue(val.String())
	}

	if a.Key == "error" {
		val := a.Value.Any()
		if multiErr, ok := val.(multiError); ok {
			var meAttrs []slog.Attr
			for _, e := range errs {
				meAttrs = append(meAttrs, errAttrs(e)...)
			}
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
	if slices.Contains(sensitiveKeys, a.Key) {
		a.Value = slog.StringValue("[REDACTED]")
	}
	return a
}

func InitializeLogger(logFile string) (*slog.Logger, closeFunc, error) {
	var (
		handlers []slog.Handler
		closers  []closeFunc
	)
	tintNC := !isatty.IsTerminal(os.Stderr.Fd()) && !isatty.IsCygwinTerminal(os.Stderr.Fd())
	if logFile == "" {
		logger := slog.New(tint.NewTextHandler(os.Stderr, &tint.Options{NoColor: tintNC}))
		return logger, func() error { return nil }, nil
	}
	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open access log: %v", err)
	}
	bufferedFile := bufio.NewWriter(f)

	handlers = append(handlers, slog.NewJSONHandler(bufferedFile, &slog.HandlerOptions{
		ReplaceAttr: replaceAttr,
	}))

	logger := &lumberjack.Logger{
		Filename:   logFile,
		MaxSize:    1,
		MaxAge:     28,
		MaxBackups: 10,
		LocalTime:  false,
		Compress:   true,
	}

	closers = append(closers, func() error {
		err := logger.Close()
		return err
	})

	handlers = append(handlers, slog.NewJSONHandler(logger, &slog.HandlerOptions{
		ReplaceAttr: replaceAttr,
	}))
	close := func() error {
		var errs []error
		for _, closer := range closers {
			errs = append(errs, closer())
		}
		return errors.Join(errs...)
	}

	return slog.New(slog.NewMultiHandler(handlers...)), close, nil
}

func requestID() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			head := r.Header.Get("X-Request-ID")
			if head == "" {
				head = rand.Text()
			}
			w.Header().Set("X-Request-ID", head)
			next.ServeHTTP(w, r)
		})
	}
}

func redactIP(addr string) (string, error) {
	ip, _, err := net.SplitHostPort(addr)
	if err != nil {
		return "", err
	}
	parsedIp := net.ParseIP(ip)
	if parsedIp == nil {
		return "", errors.New("Invalid IP")
	}
	ip4 := parsedIp.To4()
	if ip4 == nil {
		return addr, nil
	}

	return fmt.Sprintf("%d.%d.%d.x", ip4[0], ip4[1], ip4[2]), nil
}

func requestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			reqID := w.Header().Get("X-Request-ID")
			spyReader := &spyReadCloser{ReadCloser: r.Body}
			spyWriter := &spyResponseWriter{ResponseWriter: w}
			r.Body = spyReader
			logCtx := &LogContext{}
			r = r.WithContext(context.WithValue(r.Context(), logContextKey, logCtx))
			next.ServeHTTP(spyWriter, r)
			redactedAddr, err := redactIP(r.RemoteAddr)
			if err != nil {
				logCtx.Error = errors.New("Invalid address")
			}

			attrs := []any{slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.String("client_ip", redactedAddr),
				slog.String("request_id", reqID),
				slog.Int("request_body_bytes", spyReader.bytesRead),
				slog.Int("response_status", spyWriter.statusCode),
				slog.Int("response_body_bytes", spyWriter.bytesWritten),
				slog.Duration("duration", time.Since(start)),
			}
			if logCtx.Username != "" {
				attrs = append(attrs, slog.String("user", logCtx.Username))
			}
			if logCtx.Error != nil {
				attrs = append(attrs, slog.Any("error", logCtx.Error))
			}

			logger.Info("Served request", attrs...)
		})
	}
}

func httpError(ctx context.Context, w http.ResponseWriter, status int, err error) {
	if logCtx, ok := ctx.Value(logContextKey).(*LogContext); ok {
		logCtx.Error = err
	}
	switch status {
	case 401, 403:
		http.Error(w, "Unauthorized", status)
	case 500:
		http.Error(w, "Internal Server Error", status)
	default:
		http.Error(w, err.Error(), status)
	}
}
