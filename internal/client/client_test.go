package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type recorded struct {
	method, rawPath, auth, userAgent, body string
}

// testServer answers with handler and records every request.
func testServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, n int)) (*Client, *[]recorded, *sync.Mutex) {
	t.Helper()
	var mu sync.Mutex
	var requests []recorded
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requests = append(requests, recorded{r.Method, r.URL.EscapedPath(), r.Header.Get("Authorization"), r.Header.Get("User-Agent"), string(body)})
		n := len(requests)
		mu.Unlock()
		handler(w, r, n)
	}))
	t.Cleanup(server.Close)
	c := New(Config{BaseURL: server.URL + "/api/v1/", Token: "autter_pat_test", UserAgent: "terraform-provider-autter/test"})
	c.sleep = func(ctx context.Context, d time.Duration) error { return ctx.Err() }
	c.backoffBase = time.Millisecond
	return c, &requests, &mu
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func TestNormalizeAndValidateBaseURL(t *testing.T) {
	cases := map[string]string{
		"":                               DefaultBaseURL,
		"https://api.autter.dev/":        "https://api.autter.dev",
		"https://api.autter.dev/api/v1":  "https://api.autter.dev",
		"https://api.autter.dev/api/v1/": "https://api.autter.dev",
		" http://127.0.0.1:8080 ":        "http://127.0.0.1:8080",
	}
	for in, want := range cases {
		if got := NormalizeBaseURL(in); got != want {
			t.Errorf("NormalizeBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
	for _, ok := range []string{"https://api.autter.dev", "http://localhost:3000", "http://127.0.0.1:1", "http://[::1]:9"} {
		if err := ValidateBaseURL(ok); err != nil {
			t.Errorf("ValidateBaseURL(%q): %v", ok, err)
		}
	}
	for _, bad := range []string{"http://api.autter.dev", "ftp://x", "api.autter.dev", "https://"} {
		if err := ValidateBaseURL(bad); err == nil {
			t.Errorf("ValidateBaseURL(%q) accepted", bad)
		}
	}
}

func TestSendsTokenUserAgentAndEscapedPath(t *testing.T) {
	c, requests, _ := testServer(t, func(w http.ResponseWriter, r *http.Request, n int) {
		writeJSON(w, http.StatusCreated, `{"id":"x","name":"a/b c%d é","count":12345678901234}`)
	})
	resource, created, err := c.PutResource(context.Background(), "acme/web", AlertRules, "a/b c%d é", map[string]any{"name": "a/b c%d é"})
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Error("201 should report created")
	}
	if n, ok := resource["count"].(json.Number); !ok || n.String() != "12345678901234" {
		t.Errorf("numbers should stay exact, got %#v", resource["count"])
	}
	got := (*requests)[0]
	if got.rawPath != "/api/v1/repositories/acme%2Fweb/alert-rules/a%2Fb%20c%25d%20%C3%A9" {
		t.Errorf("path %q", got.rawPath)
	}
	if got.auth != "Bearer autter_pat_test" || got.userAgent != "terraform-provider-autter/test" {
		t.Errorf("headers %q %q", got.auth, got.userAgent)
	}
	if got.body != `{"name":"a/b c%d é"}` {
		t.Errorf("body %q", got.body)
	}
}

func TestDecodesErrors(t *testing.T) {
	c, _, _ := testServer(t, func(w http.ResponseWriter, r *http.Request, n int) {
		switch {
		case strings.Contains(r.URL.Path, "conflict"):
			writeJSON(w, 409, `{"error":{"code":"name_conflict","message":"2 alert rules are named x.","detail":{"ids":["a","b"]}}}`)
		case strings.Contains(r.URL.Path, "proxy"):
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			_, _ = io.WriteString(w, "<html>413 Request Entity Too Large</html>")
		default:
			writeJSON(w, 404, `{"error":{"code":"repository_not_found","message":"No repository.","detail":{"candidates":["acme/web","other/web"]}}}`)
		}
	})
	ctx := context.Background()
	err := c.DeleteResource(ctx, "r", AlertRules, "conflict")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != CodeNameConflict || fmt.Sprint(apiErr.IDs()) != "[a b]" {
		t.Fatalf("conflict: %#v", err)
	}
	err = c.DeleteResource(ctx, "r", AlertRules, "proxy")
	if !errors.As(err, &apiErr) || apiErr.Code != "http_413" || !strings.Contains(apiErr.Message, "Too Large") {
		t.Fatalf("non-JSON: %#v", err)
	}
	_, err = c.ListResources(ctx, "web", Dashboards)
	if !errors.As(err, &apiErr) || apiErr.Code != CodeRepositoryNotFound || fmt.Sprint(apiErr.Candidates()) != "[acme/web other/web]" {
		t.Fatalf("repository: %#v", err)
	}
}

func TestRetriesRateLimitAfterRetryAfter(t *testing.T) {
	c, requests, _ := testServer(t, func(w http.ResponseWriter, r *http.Request, n int) {
		if n <= 2 {
			w.Header().Set("Retry-After", "7")
			writeJSON(w, 429, `{"error":{"code":"rate_limited","message":"Rate limit exceeded."}}`)
			return
		}
		writeJSON(w, 200, `{"id":"x"}`)
	})
	var waits []time.Duration
	c.sleep = func(ctx context.Context, d time.Duration) error { waits = append(waits, d); return nil }
	if _, created, err := c.PutResource(context.Background(), "r", AlertRules, "x", map[string]any{}); err != nil || created {
		t.Fatalf("err %v created %v", err, created)
	}
	if len(*requests) != 3 || fmt.Sprint(waits) != "[8s 8s]" {
		t.Errorf("requests %d waits %v", len(*requests), waits)
	}
}

func TestGivesUpAfterMaxRateLimitAttempts(t *testing.T) {
	c, requests, _ := testServer(t, func(w http.ResponseWriter, r *http.Request, n int) {
		writeJSON(w, 429, `{"error":{"code":"rate_limited","message":"Rate limit exceeded."}}`)
	})
	var waits []time.Duration
	c.sleep = func(ctx context.Context, d time.Duration) error { waits = append(waits, d); return nil }
	_, err := c.ListResources(context.Background(), "r", AlertRules)
	if ErrorCode(err) != CodeRateLimited || !strings.Contains(err.Error(), "still rate limited") {
		t.Fatalf("err %v", err)
	}
	if len(*requests) != MaxRateLimitAttempts || waits[0] != defaultRetryAfter {
		t.Errorf("requests %d first wait %v", len(*requests), waits[0])
	}
}

func TestRetriesUnavailableAndTransportErrors(t *testing.T) {
	c, requests, _ := testServer(t, func(w http.ResponseWriter, r *http.Request, n int) {
		if n <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, 200, `{"data":[]}`)
	})
	if _, err := c.ListResources(context.Background(), "r", AlertRules); err != nil {
		t.Fatal(err)
	}
	if len(*requests) != 3 {
		t.Errorf("requests %d", len(*requests))
	}

	// Four 503s in a row: three retries, then the error.
	c2, requests2, _ := testServer(t, func(w http.ResponseWriter, r *http.Request, n int) { w.WriteHeader(http.StatusBadGateway) })
	_, err := c2.ListResources(context.Background(), "r", AlertRules)
	if ErrorCode(err) != "http_502" || len(*requests2) != 4 {
		t.Errorf("err %v requests %d", err, len(*requests2))
	}

	// Nothing listening: transport errors are retried, then reported.
	dead := New(Config{BaseURL: "http://127.0.0.1:1", Token: "t"})
	attempts := 0
	dead.sleep = func(ctx context.Context, d time.Duration) error { attempts++; return nil }
	if _, err := dead.ListRepositories(context.Background()); err == nil || attempts != maxTransientRetries {
		t.Errorf("err %v attempts %d", err, attempts)
	}
}

func TestStopsWhenTheContextIsCancelled(t *testing.T) {
	c, _, _ := testServer(t, func(w http.ResponseWriter, r *http.Request, n int) {
		writeJSON(w, 429, `{"error":{"code":"rate_limited","message":"x"}}`)
	})
	c.sleep = sleepContext
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.ListRepositories(ctx)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 5*time.Second {
		t.Fatalf("err %v after %v", err, time.Since(start))
	}
}

func TestWritesNeverOverlap(t *testing.T) {
	var inFlight, maxInFlight int32
	c, requests, _ := testServer(t, func(w http.ResponseWriter, r *http.Request, n int) {
		now := atomic.AddInt32(&inFlight, 1)
		for {
			seen := atomic.LoadInt32(&maxInFlight)
			if now <= seen || atomic.CompareAndSwapInt32(&maxInFlight, seen, now) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt32(&inFlight, -1)
		writeJSON(w, 200, `{"id":"x"}`)
	})
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				_, _, _ = c.PutResource(context.Background(), "r", AlertRules, fmt.Sprint(i), map[string]any{})
			} else {
				_ = c.DeleteResource(context.Background(), "r", Dashboards, fmt.Sprint(i))
			}
		}(i)
	}
	wg.Wait()
	if len(*requests) != 6 || maxInFlight != 1 {
		t.Errorf("requests %d, max concurrent writes %d", len(*requests), maxInFlight)
	}
}

func TestListCacheHitExpiryAndInvalidation(t *testing.T) {
	c, requests, mu := testServer(t, func(w http.ResponseWriter, r *http.Request, n int) {
		if r.Method == http.MethodPut {
			writeJSON(w, 200, `{"id":"x"}`)
			return
		}
		writeJSON(w, 200, `{"data":[{"id":"a1","name":"One"},{"id":"a2","name":"Two"}]}`)
	})
	now := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	ctx := context.Background()
	count := func() int { mu.Lock(); defer mu.Unlock(); return len(*requests) }

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.FindResource(ctx, "r", AlertRules, "One"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if count() != 1 {
		t.Fatalf("ten parallel reads sent %d requests", count())
	}
	if _, err := c.FindResource(ctx, "r", AlertRules, "Missing"); !IsNotFound(err) {
		t.Errorf("missing: %v", err)
	}
	if _, err := c.ListResources(ctx, "r", Dashboards); err != nil || count() != 2 {
		t.Errorf("another kind is another key: %v %d", err, count())
	}
	now = now.Add(61 * time.Second)
	_, _ = c.ListResources(ctx, "r", AlertRules)
	if count() != 3 {
		t.Errorf("expired entry not refetched: %d", count())
	}
	_, _ = c.ListResources(ctx, "r", Dashboards)
	_, _, _ = c.PutResource(ctx, "other-repo", AlertRules, "One", map[string]any{})
	_, _ = c.ListResources(ctx, "r", AlertRules)
	_, _ = c.ListResources(ctx, "r", Dashboards)
	if count() != 6 {
		t.Errorf("a write drops that kind's lists only: %d", count())
	}
}

func TestDuplicateNamesAreAConflict(t *testing.T) {
	c, _, _ := testServer(t, func(w http.ResponseWriter, r *http.Request, n int) {
		writeJSON(w, 200, `{"data":[{"id":"a1","name":"Twin"},{"id":"a2","name":"Twin"}]}`)
	})
	_, err := c.FindResource(context.Background(), "r", AlertRules, "Twin")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != CodeNameConflict || fmt.Sprint(apiErr.IDs()) != "[a1 a2]" {
		t.Fatalf("err %#v", err)
	}
}
