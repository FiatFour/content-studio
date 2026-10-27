package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"

	"github.com/fiat/content-studio/internal/jobs"
)

const maxResponse = 64 << 20

// postJSON sends body and returns the raw response. Connection failures are classified as
// transient so the job can retry; HTTP errors carry only the status, never the response body.
func postJSON(ctx context.Context, client *http.Client, url string, body any) ([]byte, error) {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return do(client, req)
}

func get(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return do(client, req)
}

func do(client *http.Client, req *http.Request) ([]byte, error) {
	resp, err := client.Do(req)
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) || errors.Is(err, io.EOF) || isConnRefused(err) {
			return nil, jobs.Errorf(jobs.ErrTransientNetwork, err, "local service %s is unreachable", req.URL.Host)
		}
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return nil, jobs.Errorf(jobs.ErrTransientNetwork, err, "reading response from %s", req.URL.Host)
	}
	if resp.StatusCode >= 500 {
		return nil, jobs.Errorf(jobs.ErrTransientNetwork, nil, "local service %s returned HTTP %d", req.URL.Host, resp.StatusCode)
	}
	if resp.StatusCode >= 300 {
		return nil, jobs.Errorf(jobs.ErrInvalidInput, nil, "local service %s rejected the request (HTTP %d)", req.URL.Host, resp.StatusCode)
	}
	return data, nil
}

func isConnRefused(err error) bool {
	var oe *net.OpError
	return errors.As(err, &oe)
}

func decode(data []byte, v any, what string) error {
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("decode %s response: %w", what, err)
	}
	return nil
}
