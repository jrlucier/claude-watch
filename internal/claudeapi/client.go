package claudeapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultEndpoint is the OAuth usage API.
const DefaultEndpoint = "https://api.anthropic.com/api/oauth/usage"

// betaHeader matches the value the Haletran extension sends (extension.js:297).
const betaHeader = "oauth-2025-04-20"

// Client calls the OAuth usage API.
type Client struct {
	HTTP     *http.Client
	Endpoint string // override for tests
}

// NewClient returns a Client with a 10-second timeout and optional proxy.
// Empty proxyURL means honor environment / no proxy.
func NewClient(proxyURL string) (*Client, error) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if proxyURL != "" {
		u, err := url.Parse(proxyURL)
		if err != nil {
			return nil, fmt.Errorf("parse proxy %q: %w", proxyURL, err)
		}
		tr.Proxy = http.ProxyURL(u)
	}
	return &Client{
		HTTP:     &http.Client{Timeout: 10 * time.Second, Transport: tr},
		Endpoint: DefaultEndpoint,
	}, nil
}

// Fetch makes one usage call. The bearer token is passed in directly so
// callers control credential refresh.
func (c *Client) Fetch(ctx context.Context, token string) (UsageResponse, error) {
	var zero UsageResponse
	if token == "" {
		return zero, fmt.Errorf("empty oauth token")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Endpoint, nil)
	if err != nil {
		return zero, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", betaHeader)
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return zero, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return zero, &HTTPError{
			Status:     resp.StatusCode,
			Body:       string(body),
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
		}
	}
	var u UsageResponse
	if err := json.Unmarshal(body, &u); err != nil {
		return zero, fmt.Errorf("decode usage response: %w (raw: %s)", err, truncate(string(body), 200))
	}
	return u, nil
}

// HTTPError is returned on a non-2xx response. Carries the status code so the
// caller can distinguish 401 (token refresh) from other failures, plus the
// parsed Retry-After hint for 429s.
type HTTPError struct {
	Status     int
	Body       string
	RetryAfter time.Duration // zero if not present or unparseable
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("http %d: %s", e.Status, truncate(e.Body, 200))
}

// parseRetryAfter honors both seconds-form ("60") and HTTP-date form.
// Returns 0 when the value is missing or unparseable.
func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// FriendlyError maps a raw error string (the value stored in
// state.APILastError) into a short human-friendly label suitable for display
// in the tray menu or CLI status. The raw error is still preserved in the
// daemon log; this is purely for surface-level UI.
func FriendlyError(raw string) string {
	if raw == "" {
		return ""
	}
	r := strings.ToLower(raw)
	switch {
	case strings.Contains(r, "http 429"):
		return "rate limited"
	case strings.Contains(r, "http 401"), strings.Contains(r, "http 403"):
		return "auth expired"
	case strings.Contains(r, "http 5"):
		return "server error"
	case strings.Contains(r, "no such host"),
		strings.Contains(r, "connection refused"),
		strings.Contains(r, "network is unreachable"),
		strings.Contains(r, "no route to host"):
		return "offline"
	case strings.Contains(r, "timeout"),
		strings.Contains(r, "deadline exceeded"),
		strings.Contains(r, "i/o timeout"):
		return "timed out"
	case strings.HasPrefix(r, "http "):
		// Strip the body, keep just "http 4xx".
		if i := strings.Index(raw, ":"); i > 0 {
			return strings.TrimSpace(raw[:i])
		}
		return raw
	}
	// Trim to a single short line as a last resort so we never dump a JSON
	// blob into the tray menu.
	if i := strings.IndexAny(raw, "\n\r"); i > 0 {
		raw = raw[:i]
	}
	return truncate(raw, 48)
}
