package mockapi

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// The normalizations below are the ones the real API applies and the
// provider's tests rely on (see the API's lib/runtime-alert-rules,
// lib/synthetic-checks and lib/runtime-dashboards). They are deliberately
// partial: semantic checks such as a valid regex stay on the server.

type inputError string

func (e inputError) Error() string { return string(e) }

func fail(format string, args ...any) inputError { return inputError(fmt.Sprintf(format, args...)) }

var (
	alertTriggers   = []string{"new", "regressed", "spiking", "assigned", "resolved"}
	alertPriorities = []string{"p0", "p1", "p2", "p3", "none"}
	matchOps        = []string{"equals", "contains", "starts_with", "regex"}
	checkMethods    = []string{"GET", "HEAD"}
	checkIntervals  = []int64{60, 300, 900, 3600}
	dashboardRanges = []string{"1h", "6h", "24h", "7d", "30d"}
	dashboardViz    = []string{"line", "bar", "stat", "table"}
	dashboardSource = []string{"logs", "spans", "errors", "metrics", "llm", "sql"}
	dashboardMeas   = []string{"count", "rate", "p50", "p95", "p99", "sum"}
	filterOps       = []string{"eq", "neq", "contains", "not_contains", "starts_with", "in", "not_in", "gt", "gte", "lt", "lte"}
	// numberColumns are the dashboard filter fields that hold numbers.
	numberColumns = map[string]bool{"status_code": true, "duration_ms": true, "ai_cost_usd": true, "ai_calls": true}
)

func contains[T comparable](list []T, v T) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

func strictKeys(raw map[string]any, allowed ...string) error {
	for key := range raw {
		if !contains(allowed, key) {
			return fail("Unrecognized key: %q", key)
		}
	}
	return nil
}

func object(raw any, what string) (map[string]any, error) {
	if raw == nil {
		return nil, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, fail("%s must be an object.", what)
	}
	return m, nil
}

func stringList(raw any, what string, lower bool) ([]any, error) {
	out := []any{}
	if raw == nil {
		return out, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fail("%s must be a list.", what)
	}
	seen := map[string]bool{}
	for _, item := range list {
		s, ok := item.(string)
		if !ok {
			return nil, fail("%s must be a list of text values.", what)
		}
		s = strings.TrimSpace(s)
		if lower {
			s = strings.ToLower(s)
		}
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out, nil
}

func integer(raw any) (int64, bool) {
	switch v := raw.(type) {
	case json.Number:
		n, err := v.Int64()
		return n, err == nil
	case float64:
		return int64(v), float64(int64(v)) == v
	case int64:
		return v, true
	case int:
		return int64(v), true
	}
	return 0, false
}

func intOr(raw any, def int64, what string, min, max int64) (int64, error) {
	if raw == nil {
		return def, nil
	}
	n, ok := integer(raw)
	if !ok || n < min || n > max {
		return 0, fail("%s must be a whole number from %d to %d.", what, min, max)
	}
	return n, nil
}

func boolOr(raw any, def bool) bool {
	if b, ok := raw.(bool); ok {
		return b
	}
	return def
}

func nullableString(raw any) any {
	s, ok := raw.(string)
	if !ok || strings.TrimSpace(s) == "" {
		return nil
	}
	return strings.TrimSpace(s)
}

// ── Alert rules ─────────────────────────────────────────────────────────────

func textMatch(raw any, what string) (any, error) {
	m, err := object(raw, what)
	if err != nil || m == nil {
		return nil, err
	}
	op, _ := m["op"].(string)
	if !contains(matchOps, op) {
		return nil, fail("%s: choose how to match (%s).", what, strings.Join(matchOps, ", "))
	}
	value, _ := m["value"].(string)
	if value == "" {
		return nil, nil
	}
	return map[string]any{"op": op, "value": value}, nil
}

func recipients(raw any, what string) (any, error) {
	m, err := object(raw, what)
	if err != nil || m == nil {
		return nil, err
	}
	ids, err := stringList(m["user_ids"], what+" members", false)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"assignee": m["assignee"] == true, "watchers": m["watchers"] == true, "user_ids": ids, "everyone": m["everyone"] == true}
	if out["assignee"] == false && out["watchers"] == false && out["everyone"] == false && len(ids) == 0 {
		return nil, nil
	}
	return out, nil
}

// normalizeAlertRule returns the stored fields of an alert rule.
func normalizeAlertRule(in map[string]any) (map[string]any, error) {
	if err := strictKeys(in, "name", "enabled", "triggers", "filters", "actions"); err != nil {
		return nil, err
	}
	triggers, err := stringList(in["triggers"], "Triggers", true)
	if err != nil {
		return nil, err
	}
	if len(triggers) == 0 {
		return nil, fail("Choose at least one trigger.")
	}
	for _, t := range triggers {
		if !contains(alertTriggers, t.(string)) {
			return nil, fail("Triggers: choose from %s.", strings.Join(alertTriggers, ", "))
		}
	}
	rawFilters, err := object(in["filters"], "Filters")
	if err != nil {
		return nil, err
	}
	if rawFilters == nil {
		rawFilters = map[string]any{}
	}
	filters := map[string]any{}
	for _, key := range []string{"environments", "services"} {
		if filters[key], err = stringList(rawFilters[key], key, false); err != nil {
			return nil, err
		}
	}
	for _, key := range []string{"levels", "priorities"} {
		if filters[key], err = stringList(rawFilters[key], key, true); err != nil {
			return nil, err
		}
	}
	for _, p := range filters["priorities"].([]any) {
		if !contains(alertPriorities, p.(string)) {
			return nil, fail("Priorities: choose from %s.", strings.Join(alertPriorities, ", "))
		}
	}
	if filters["error_type"], err = textMatch(rawFilters["error_type"], "Error type"); err != nil {
		return nil, err
	}
	if filters["message"], err = textMatch(rawFilters["message"], "Message"); err != nil {
		return nil, err
	}
	filters["assignee"] = "any"
	if a, ok := rawFilters["assignee"].(string); ok && a != "" {
		filters["assignee"] = a
	}
	filters["threshold"] = nil
	if t, err := object(rawFilters["threshold"], "The minimum count"); err != nil {
		return nil, err
	} else if t != nil && (t["min_events"] != nil || t["min_users"] != nil) {
		window, err := intOr(t["window_minutes"], 60, "The window", 1, 10080)
		if err != nil {
			return nil, err
		}
		filters["threshold"] = map[string]any{"min_events": numberOrNil(t["min_events"]), "min_users": numberOrNil(t["min_users"]), "window_minutes": json.Number(strconv.FormatInt(window, 10))}
	}

	rawActions, err := object(in["actions"], "Actions")
	if err != nil {
		return nil, err
	}
	if rawActions == nil {
		return nil, fail("Choose at least one action.")
	}
	actions := map[string]any{"slack": nil, "oncall": rawActions["oncall"] == true, "auto_assignments": rawActions["auto_assignments"] == true}
	if slack, err := object(rawActions["slack"], "The Slack action"); err != nil {
		return nil, err
	} else if slack != nil {
		actions["slack"] = map[string]any{"channel_id": nullableString(slack["channel_id"])}
	}
	if actions["email"], err = recipients(rawActions["email"], "Email"); err != nil {
		return nil, err
	}
	if actions["in_app"], err = recipients(rawActions["in_app"], "In-app"); err != nil {
		return nil, err
	}
	if actions["slack"] == nil && actions["email"] == nil && actions["in_app"] == nil && actions["oncall"] == false {
		return nil, fail("Choose at least one action: Slack, email, in-app or on-call.")
	}
	return map[string]any{"enabled": boolOr(in["enabled"], true), "triggers": triggers, "filters": filters, "actions": actions}, nil
}

func numberOrNil(raw any) any {
	if n, ok := integer(raw); ok {
		return json.Number(strconv.FormatInt(n, 10))
	}
	return nil
}

// ── Synthetic checks ────────────────────────────────────────────────────────

// normalizeCheck returns the stored fields of a check and its headers.
func normalizeCheck(in map[string]any) (map[string]any, map[string]string, error) {
	if err := strictKeys(in, "name", "url", "method", "headers", "expected_status", "body_contains", "timeout_ms", "interval_seconds",
		"latency_threshold_ms", "failures_to_fire", "service", "environment", "enabled"); err != nil {
		return nil, nil, err
	}
	rawURL, _ := in["url"].(string)
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, nil, fail("Give an http or https address.")
	}
	if u.Path == "" {
		u.Path = "/"
	}
	method := "GET"
	if m, ok := in["method"].(string); ok {
		method = strings.ToUpper(m)
	}
	if !contains(checkMethods, method) {
		return nil, nil, fail("Method: GET or HEAD.")
	}
	headers := map[string]string{}
	if raw, err := object(in["headers"], "Headers"); err != nil {
		return nil, nil, err
	} else {
		for k, v := range raw {
			s, ok := v.(string)
			if !ok {
				return nil, nil, fail("Header values are text.")
			}
			headers[k] = s
		}
	}
	if len(headers) > 20 {
		return nil, nil, fail("At most 20 headers.")
	}
	out := map[string]any{"url": u.String(), "method": method, "body_contains": nullableString(in["body_contains"]), "service": nullableString(in["service"])}
	ints := []struct {
		key           string
		def, min, max int64
	}{
		{"expected_status", 200, 100, 599},
		{"timeout_ms", 10000, 1000, 30000},
		{"interval_seconds", 300, 60, 3600},
		{"failures_to_fire", 2, 1, 10},
	}
	for _, f := range ints {
		n, err := intOr(in[f.key], f.def, f.key, f.min, f.max)
		if err != nil {
			return nil, nil, err
		}
		out[f.key] = json.Number(strconv.FormatInt(n, 10))
	}
	if n, _ := integer(out["interval_seconds"]); !contains(checkIntervals, n) {
		return nil, nil, fail("Interval: 60, 300, 900 or 3600 seconds.")
	}
	out["latency_threshold_ms"] = numberOrNil(in["latency_threshold_ms"])
	if method == "HEAD" && out["body_contains"] != nil {
		return nil, nil, fail("A HEAD request has no body to search.")
	}
	out["environment"] = "production"
	if env := nullableString(in["environment"]); env != nil {
		out["environment"] = env
	}
	out["enabled"] = boolOr(in["enabled"], true)
	return out, headers, nil
}

func headerNames(headers map[string]string) []any {
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]any, len(names))
	for i, n := range names {
		out[i] = n
	}
	return out
}

// ── Dashboards ──────────────────────────────────────────────────────────────

func filterValue(raw any, numeric bool) (any, error) {
	if numeric {
		switch v := raw.(type) {
		case json.Number:
			return v, nil
		case string:
			if _, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
				return json.Number(strings.TrimSpace(v)), nil
			}
		}
		return nil, fail("Filter: give a number.")
	}
	switch v := raw.(type) {
	case string:
		return v, nil
	case json.Number:
		return v.String(), nil
	}
	return nil, fail("Filter: give a value.")
}

func normalizeWidget(raw any, index int) (map[string]any, error) {
	w, err := object(raw, "A widget")
	if err != nil || w == nil {
		return nil, fail("A widget is an object.")
	}
	id, _ := w["id"].(string)
	if id == "" {
		id = fmt.Sprintf("w%d", index+1)
	}
	title, _ := w["title"].(string)
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, fail("Give every widget a title.")
	}
	viz, _ := w["viz"].(string)
	source, _ := w["source"].(string)
	if !contains(dashboardViz, viz) || !contains(dashboardSource, source) {
		return nil, fail("%q: choose a chart type and a source.", title)
	}
	if source == "sql" {
		sql, _ := w["sql"].(string)
		if strings.TrimSpace(sql) == "" {
			return nil, fail("%q: give a SELECT.", title)
		}
		return map[string]any{"id": id, "title": title, "viz": viz, "source": source, "sql": strings.TrimSpace(sql)}, nil
	}
	measure, _ := w["measure"].(string)
	if !contains(dashboardMeas, measure) {
		return nil, fail("%q measure: choose from %s.", title, strings.Join(dashboardMeas, ", "))
	}
	field := nullableString(w["field"])
	if field == nil && (source == "logs" || source == "spans") && strings.HasPrefix(measure, "p") {
		field = "duration_ms"
	}
	groupBy, err := stringList(w["group_by"], "group by", false)
	if err != nil {
		return nil, err
	}
	filters := []any{}
	if rawFilters, ok := w["filters"].([]any); ok {
		for _, rf := range rawFilters {
			f, err := object(rf, "A filter")
			if err != nil || f == nil {
				return nil, fail("A filter is an object.")
			}
			fieldName, _ := f["field"].(string)
			op, _ := f["op"].(string)
			if !contains(filterOps, op) {
				return nil, fail("Filter operator: choose from %s.", strings.Join(filterOps, ", "))
			}
			numeric := numberColumns[fieldName]
			var value any
			if op == "in" || op == "not_in" {
				list, ok := f["value"].([]any)
				if !ok {
					list = []any{f["value"]}
				}
				values := make([]any, 0, len(list))
				for _, item := range list {
					v, err := filterValue(item, numeric)
					if err != nil {
						return nil, err
					}
					values = append(values, v)
				}
				value = values
			} else {
				if _, isList := f["value"].([]any); isList {
					return nil, fail("Filter: give one value, or use in / not_in for a list.")
				}
				if value, err = filterValue(f["value"], numeric); err != nil {
					return nil, err
				}
			}
			filters = append(filters, map[string]any{"field": fieldName, "op": op, "value": value})
		}
	}
	return map[string]any{"id": id, "title": title, "viz": viz, "source": source, "measure": measure, "field": field, "group_by": groupBy, "filters": filters}, nil
}

// normalizeDashboard returns the stored fields of a dashboard.
func normalizeDashboard(in map[string]any) (map[string]any, error) {
	if err := strictKeys(in, "name", "variables", "widgets", "layout"); err != nil {
		return nil, err
	}
	vars, err := object(in["variables"], "Variables")
	if err != nil {
		return nil, err
	}
	variables := map[string]any{"service": nil, "environment": nil, "range": "24h"}
	if vars != nil {
		variables["service"] = nullableString(vars["service"])
		variables["environment"] = nullableString(vars["environment"])
		if r, ok := vars["range"].(string); ok {
			if !contains(dashboardRanges, r) {
				return nil, fail("Range: choose from %s.", strings.Join(dashboardRanges, ", "))
			}
			variables["range"] = r
		}
	}
	rawWidgets, ok := in["widgets"].([]any)
	if !ok {
		return nil, fail("widgets: Invalid input: expected array")
	}
	widgets := make([]any, 0, len(rawWidgets))
	ids := map[string]bool{}
	for i, raw := range rawWidgets {
		w, err := normalizeWidget(raw, i)
		if err != nil {
			return nil, err
		}
		if ids[w["id"].(string)] {
			return nil, fail("Two widgets have the id %q.", w["id"])
		}
		ids[w["id"].(string)] = true
		widgets = append(widgets, w)
	}
	layout := []any{}
	placed := map[string]bool{}
	if rawLayout, ok := in["layout"].([]any); ok {
		for _, raw := range rawLayout {
			item, _ := raw.(map[string]any)
			id, _ := item["id"].(string)
			if !ids[id] || placed[id] {
				continue
			}
			placed[id] = true
			layout = append(layout, map[string]any{"id": id, "w": item["w"], "h": item["h"]})
		}
	}
	for _, w := range widgets {
		id := w.(map[string]any)["id"].(string)
		if !placed[id] {
			layout = append(layout, map[string]any{"id": id, "w": json.Number("6"), "h": json.Number("2")})
		}
	}
	return map[string]any{"variables": variables, "widgets": widgets, "layout": layout}, nil
}
