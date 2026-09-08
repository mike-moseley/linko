package main

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func Test_requestLogger(t *testing.T) {
	logBuffer := &bytes.Buffer{}

	logger := slog.New(slog.NewTextHandler(logBuffer, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				return slog.Time(slog.TimeKey, time.Date(2023, 10, 1, 12, 34, 57, 0, time.UTC))
			}
			return a
		},
	}))

	requestLoggerMiddleware := requestLogger(logger)
	dummyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	loggedHandler := requestLoggerMiddleware(dummyHandler)

	req := httptest.NewRequest("GET", "http://lin.ko/api/stats", nil)
	rr := httptest.NewRecorder()
	loggedHandler.ServeHTTP(rr, req)

	const expectedLogString = `time=2023-10-01T12:34:57.000Z level=INFO msg="Served request" method=GET path=/api/stats client_ip=192.0.2.1:1234` + "\n"
	const expectedStatusCode = http.StatusOK

	// replace the .Skip() call with two checks to verify the log string and status code here
	// If either doesn't match, use t.Errorf to report the failure with a helpful message.
	
	 if !(expectedLogString == logBuffer.String()) {
		t.Errorf("Log string does not match expected result:\n  Expected: %s\n  Received: %s\n", expectedLogString,logBuffer.String())
	}
	if !(expectedStatusCode == rr.Code) {
		t.Errorf("Status code does not match expected result:\n  Expected: %d\n  Received: %d\n", expectedStatusCode, rr.Code)
	}
}
