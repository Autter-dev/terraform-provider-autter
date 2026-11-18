// Package client is a small HTTP client for the Autter API (/api/v1).
//
// The API addresses a resource by its repository and its name, and PUT is a
// whole-resource upsert, so every write is idempotent and safe to retry. The
// client retries 429 after Retry-After, retries 502/503/504 and transport
// errors a few times, sends writes one at a time (the API allows 10 writes a
// minute per token), and caches list reads for a minute so a refresh of many
// resources costs one read per repository and resource kind.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// DefaultBaseURL is the production Autter API host. The client appends /api/v1.
const DefaultBaseURL = "https://api.autter.dev"

const (
	// MaxRateLimitAttempts is how many times one request is sent while the API answers 429.
	MaxRateLimitAttempts = 30
	// maxTransientRetries is how many times a 502/503/504 or a transport error is retried.
	maxTransientRetries = 3
	// defaultRetryAfter is the wait when a 429 carries no Retry-After header: past the next minute window.
	defaultRetryAfter = 61 * time.Second
	// defaultListTTL is how long a list read is reused.
	defaultListTTL = 60 * time.Second
)

// Config configures a Client.
type Config struct {
	// BaseURL is the API host, for example https://api.autter.dev. /api/v1 is appended.
	BaseURL string
	// Token is a personal access token (autter_pat_…).
	Token string
	// UserAgent is sent with every request.
	UserAgent string
	// HTTPClient defaults to a client with a 60-second timeout.
	HTTPClient *http.Client
}

// Client calls the Autter API. It is safe for concurrent use.
type Client struct {
	apiRoot    string
	token      string
	userAgent  string
	httpClient *http.Client

	// writeMu sends writes one at a time.
	writeMu sync.Mutex

	lists        *listCache
	repositories *listCache

	// Seams for tests.
	sleep       func(ctx context.Context, d time.Duration) error
	backoffBase time.Duration
	now         func() time.Time
}

// New returns a Client for cfg.
func New(cfg Config) *Client {
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	c := &Client{
		apiRoot:     strings.TrimRight(NormalizeBaseURL(cfg.BaseURL), "/") + "/api/v1",
		token:       cfg.Token,
		userAgent:   cfg.UserAgent,
		httpClient:  httpClient,
		sleep:       sleepContext,
		backoffBase: time.Second,
		now:         time.Now,
	}
	c.lists = newListCache(defaultListTTL, func() time.Time { return c.now() })
	c.repositories = newListCache(defaultListTTL, func() time.Time { return c.now() })
	return c
}

// NormalizeBaseURL strips a trailing slash and a trailing /api/v1, and
// defaults to DefaultBaseURL when empty.
func NormalizeBaseURL(raw string) string {
	base := strings.TrimSpace(raw)
	if base == "" {
		return DefaultBaseURL
	}
	base = strings.TrimRight(base, "/")
	base = strings.TrimSuffix(base, "/api/v1")
	return strings.TrimRight(base, "/")
}

// ValidateBaseURL checks that base is an absolute https URL, or http on a loopback host.
func ValidateBaseURL(base string) error {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%q is not an absolute URL such as https://api.autter.dev", base)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		if host == "localhost" || host == "127.0.0.1" || host == "::1" {
			return nil
		}
		return fmt.Errorf("%q must use https; http is accepted only for localhost", base)
	default:
		return fmt.Errorf("%q must use https", base)
	}
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// escape joins path segments, escaping each (a "/" in a name becomes %2F).
func escape(segments ...string) string {
	var b strings.Builder
	for _, segment := range segments {
		b.WriteByte('/')
		b.WriteString(url.PathEscape(segment))
	}
	return b.String()
}

// do sends one request, with retries, and returns the status and body of a 2xx answer.
func (c *Client) do(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("encoding the request body: %w", err)
		}
	}
	if method != http.MethodGet {
		c.writeMu.Lock()
		defer c.writeMu.Unlock()
	}

	rateLimited, transient := 0, 0
	for {
		status, header, respBody, err := c.send(ctx, method, path, payload)
		if err != nil {
			if ctx.Err() != nil {
				return 0, nil, ctx.Err()
			}
			if transient < maxTransientRetries {
				wait := c.backoffBase << transient
				transient++
				tflog.Warn(ctx, "Autter API request failed, retrying", map[string]any{"method": method, "path": path, "error": err.Error(), "wait": wait.String()})
				if err := c.sleep(ctx, wait); err != nil {
					return 0, nil, err
				}
				continue
			}
			return 0, nil, fmt.Errorf("%s %s: %w", method, path, err)
		}
		switch {
		case status >= 200 && status < 300:
			return status, respBody, nil
		case status == http.StatusTooManyRequests:
			rateLimited++
			if rateLimited >= MaxRateLimitAttempts {
				apiErr := decodeError(method, path, status, respBody)
				apiErr.Message = fmt.Sprintf("still rate limited after %d attempts (about %d minutes). The API allows 10 writes and 60 reads a minute per token. %s",
					rateLimited, rateLimited, apiErr.Message)
				return status, nil, apiErr
			}
			wait := retryAfter(header.Get("Retry-After"))
			tflog.Warn(ctx, "Autter API rate limit reached, waiting", map[string]any{"method": method, "path": path, "wait": wait.String(), "attempt": rateLimited})
			if err := c.sleep(ctx, wait); err != nil {
				return 0, nil, err
			}
		case (status == http.StatusBadGateway || status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout) && transient < maxTransientRetries:
			wait := c.backoffBase << transient
			transient++
			tflog.Warn(ctx, "Autter API unavailable, retrying", map[string]any{"method": method, "path": path, "status": status, "wait": wait.String()})
			if err := c.sleep(ctx, wait); err != nil {
				return 0, nil, err
			}
		default:
			return status, nil, decodeError(method, path, status, respBody)
		}
	}
}

// retryAfter is the header's seconds plus one, or defaultRetryAfter.
func retryAfter(header string) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(header))
	if err != nil || seconds < 0 {
		return defaultRetryAfter
	}
	return time.Duration(seconds+1) * time.Second
}

func (c *Client) send(ctx context.Context, method, path string, payload []byte) (int, http.Header, []byte, error) {
	var reader io.Reader
	if payload != nil {
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.apiRoot+path, reader)
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return 0, nil, nil, err
	}
	return resp.StatusCode, resp.Header, body, nil
}

// decodeJSON decodes body keeping numbers exact (json.Number).
func decodeJSON(body []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decoding the API's answer: %w", err)
	}
	return nil
}
