package mockapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Autter-dev/terraform-provider-autter/internal/client"
	"github.com/Autter-dev/terraform-provider-autter/internal/mockapi"
)

func setup(t *testing.T) (*mockapi.Server, *client.Client) {
	t.Helper()
	s := mockapi.New(t)
	s.AddRepository("repo-1", "acme/web")
	s.AddRepository("repo-2", "other/web")
	s.AddRepository("repo-3", "acme/api")
	return s, client.New(client.Config{BaseURL: s.URL, Token: mockapi.Token})
}

func TestUpsertByNameAndNormalization(t *testing.T) {
	s, c := setup(t)
	ctx := context.Background()
	body := map[string]any{
		"name":     "Timeouts",
		"triggers": []any{"NEW", "new", "Regressed"},
		"filters":  map[string]any{"message": map[string]any{"op": "contains", "value": ""}},
		"actions":  map[string]any{"slack": map[string]any{}, "email": map[string]any{"assignee": false}},
	}
	created, isNew, err := c.PutResource(ctx, "acme/api", client.AlertRules, "Timeouts", body)
	if err != nil || !isNew {
		t.Fatalf("create: %v %v", err, isNew)
	}
	if fmt.Sprint(created["triggers"]) != "[new regressed]" || created["actions"].(map[string]any)["email"] != nil {
		t.Errorf("normalized: %v", created)
	}
	if slack := created["actions"].(map[string]any)["slack"].(map[string]any); slack["channel_id"] != nil {
		t.Errorf("slack: %v", slack)
	}
	if created["filters"].(map[string]any)["message"] != nil || created["filters"].(map[string]any)["assignee"] != "any" {
		t.Errorf("filters: %v", created["filters"])
	}
	again, isNew, err := c.PutResource(ctx, "repo-3", client.AlertRules, "Timeouts", body)
	if err != nil || isNew || again["updated_at"] != created["updated_at"] {
		t.Fatalf("unchanged: %v %v", err, again)
	}
	writes := s.Writes()
	if len(writes) != 2 || writes[0].Outcome != "created" || writes[1].Outcome != "unchanged" {
		t.Errorf("log: %+v", writes)
	}
}

func TestCheckURLHeadersAndStrictInput(t *testing.T) {
	s, c := setup(t)
	ctx := context.Background()
	check, _, err := c.PutResource(ctx, "repo-1", client.SyntheticChecks, "Health", map[string]any{
		"url": "https://example.com", "method": "get", "headers": map[string]any{"X-Key": "secret", "Authorization": "Bearer x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if check["url"] != "https://example.com/" || check["method"] != "GET" || check["timeout_ms"] != json.Number("10000") {
		t.Errorf("check: %v", check)
	}
	if fmt.Sprint(check["header_names"]) != "[Authorization X-Key]" || check["headers"] != nil {
		t.Errorf("header names: %v", check["header_names"])
	}
	if got := s.Headers("repo-1", "Health"); got["X-Key"] != "secret" {
		t.Errorf("headers stored: %v", got)
	}
	_, _, err = c.PutResource(ctx, "repo-1", client.SyntheticChecks, "Health", map[string]any{"url": "https://example.com", "type": "http"})
	if client.ErrorCode(err) != client.CodeInvalidArgument {
		t.Errorf("unknown top-level key: %v", err)
	}
}

func TestJourneyChecks(t *testing.T) {
	_, c := setup(t)
	ctx := context.Background()
	check, created, err := c.PutResource(ctx, "repo-1", client.SyntheticChecks, "Sign-up", map[string]any{
		"kind": "journey", "url": "https://app.example.com", "journey_id": "wj_1",
	})
	if err != nil || !created {
		t.Fatalf("create: %v", err)
	}
	if check["kind"] != "journey" || check["url"] != "https://app.example.com/" || check["interval_seconds"] != json.Number("3600") ||
		check["method"] != "GET" || check["first_passed_at"] != nil {
		t.Errorf("journey check: %v", check)
	}
	// Without kind, a PUT keeps the check's kind; another kind is refused.
	if _, _, err := c.PutResource(ctx, "repo-1", client.SyntheticChecks, "Sign-up", map[string]any{"url": "https://app.example.com", "journey_id": "wj_1"}); err != nil {
		t.Errorf("same kind: %v", err)
	}
	refused := map[string]map[string]any{
		"kind change":     {"kind": "http", "url": "https://app.example.com"},
		"http-only":       {"kind": "journey", "url": "https://app.example.com", "journey_id": "wj_1", "method": "GET"},
		"path":            {"kind": "journey", "url": "https://app.example.com/signup", "journey_id": "wj_1"},
		"no journey":      {"kind": "journey", "url": "https://app.example.com"},
		"http interval":   {"kind": "journey", "url": "https://app.example.com", "journey_id": "wj_1", "interval_seconds": 60},
		"journey on http": {"url": "https://example.com", "journey_id": "wj_1"},
	}
	for what, body := range refused {
		name := "Sign-up"
		if what == "journey on http" {
			name = "Plain"
		}
		if _, _, err := c.PutResource(ctx, "repo-1", client.SyntheticChecks, name, body); client.ErrorCode(err) != client.CodeInvalidArgument {
			t.Errorf("%s: %v", what, err)
		}
	}
}

func TestDashboardDefaults(t *testing.T) {
	_, c := setup(t)
	d, _, err := c.PutResource(context.Background(), "repo-1", client.Dashboards, "Ops", map[string]any{
		"widgets": []any{
			map[string]any{"title": " Latency ", "viz": "line", "source": "spans", "measure": "p95",
				"filters": []any{
					map[string]any{"field": "status_code", "op": "gte", "value": "500"},
					map[string]any{"field": "route", "op": "in", "value": "/a"},
				}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	w := d["widgets"].([]any)[0].(map[string]any)
	if w["id"] != "w1" || w["title"] != "Latency" || w["field"] != "duration_ms" {
		t.Errorf("widget: %v", w)
	}
	filters := w["filters"].([]any)
	if filters[0].(map[string]any)["value"] != json.Number("500") || fmt.Sprint(filters[1].(map[string]any)["value"]) != "[/a]" {
		t.Errorf("filters: %v", filters)
	}
	if fmt.Sprint(d["layout"]) != "[map[h:2 id:w1 w:6]]" {
		t.Errorf("layout: %v", d["layout"])
	}
}

func TestErrorsAndInjection(t *testing.T) {
	s, c := setup(t)
	ctx := context.Background()
	s.AddRepository("repo-4", "third/web")
	if _, err := c.ListResources(ctx, "web", client.AlertRules); !client.IsRepositoryNotFound(err) {
		t.Errorf("ambiguous short name: %v", err)
	}
	s.SetFeatureDisabled(mockapi.Dashboards, true)
	if _, err := c.ListResources(ctx, "repo-1", client.Dashboards); client.ErrorCode(err) != client.CodeFeatureDisabled {
		t.Errorf("feature: %v", err)
	}
	s.SetRole("member")
	if _, _, err := c.PutResource(ctx, "repo-1", client.AlertRules, "X", map[string]any{"triggers": []any{"new"}, "actions": map[string]any{"oncall": true}}); client.ErrorCode(err) != client.CodeInsufficientRole {
		t.Errorf("role: %v", err)
	}
	s.SetRole("admin")
	s.SetMaxChecks(1)
	s.Seed("repo-1", mockapi.SyntheticChecks, map[string]any{"name": "One", "url": "https://a.example/"})
	if _, _, err := c.PutResource(ctx, "repo-1", client.SyntheticChecks, "Two", map[string]any{"url": "https://b.example/"}); client.ErrorCode(err) != client.CodeLimitReached {
		t.Errorf("limit: %v", err)
	}
	s.Seed("repo-1", mockapi.AlertRules, map[string]any{"name": "Twin"})
	s.Seed("repo-1", mockapi.AlertRules, map[string]any{"name": "Twin"})
	if err := c.DeleteResource(ctx, "repo-1", client.AlertRules, "Twin"); client.ErrorCode(err) != client.CodeNameConflict {
		t.Errorf("conflict: %v", err)
	}
	unauth := client.New(client.Config{BaseURL: s.URL, Token: "nope"})
	if _, err := unauth.ListRepositories(ctx); client.ErrorCode(err) != client.CodeUnauthorized {
		t.Errorf("token: %v", err)
	}
	s.AddToken("mcp-only", []string{"mcp:read"})
	mcp := client.New(client.Config{BaseURL: s.URL, Token: "mcp-only"})
	if _, err := mcp.ListRepositories(ctx); client.ErrorCode(err) != client.CodeInsufficientScope {
		t.Errorf("scope: %v", err)
	}

	s.FailNext(429, 1)
	start := time.Now()
	if _, err := c.ListRepositories(ctx); err != nil {
		t.Errorf("429 then success: %v", err)
	}
	if time.Since(start) < time.Second {
		t.Errorf("Retry-After not honoured")
	}
}
