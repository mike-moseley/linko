package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
)

func checkDestination(ctx context.Context, targetURL string) error {
	resp, err := http.DefaultClient.Get(targetURL)
	if err != nil {
		return fmt.Errorf("destination unreachable: %w", err)
	}
	defer resp.Body.Close()

	_, span := tracer.Start(ctx, "http.verify_destination")
	defer span.End()

	_, _ = io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("destination returned status %d", resp.StatusCode)
	}
	return nil
}
