// Package mockapi is an in-memory Autter API (/api/v1) for the provider's
// tests. It implements the documented behaviour: bearer tokens with scopes,
// repositories addressed by id, owner/repo or short name, upsert by name with
// 201 / 200 updated / 200 unchanged, name conflicts, roles, feature switches
// and plan limits; the normalizations the tests rely on (normalize.go); a
// request log; and switches to inject 429s, 503s and out-of-band edits.
package mockapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
)

// Kinds, as in the API's paths.
const (
	AlertRules      = "alert-rules"
	SyntheticChecks = "synthetic-checks"
	Dashboards      = "dashboards"
)

// Token is the token every new Server accepts with full access.
const Token = "autter_pat_mock"

// Entry is one request the server answered.
type Entry struct {
	Method     string
	Kind       string
	Repository string
	Name       string
	Status     int
	// Outcome of a write: created, updated, unchanged, deleted, or the error code.
	Outcome string
}

// Repository is a repository the server knows.
type Repository struct {
	ID            string
	Name          string
	FullName      string
	DefaultBranch string
}

type stored struct {
	fields  map[string]any
	headers map[string]string
}

// Server is the mock API. Its methods are safe to call while it serves.
type Server struct {
	*httptest.Server

	mu         sync.Mutex
	tokens     map[string][]string
	role       string
	repos      []Repository
	data       map[string]map[string][]*stored // kind -> repository id -> resources
	log        []Entry
	disabled   map[string]bool
	maxChecks  int
	failures   []injected
	nextID     int
	clockTicks int
}

type injected struct {
	status     int
	retryAfter string
}

// New starts a server with one token (Token, full access), the role admin, and
// no repositories. It is closed when the test ends.
func New(t interface{ Cleanup(func()) }) *Server {
	s := &Server{
		tokens:   map[string][]string{Token: nil},
		role:     "admin",
		data:     map[string]map[string][]*stored{AlertRules: {}, SyntheticChecks: {}, Dashboards: {}},
		disabled: map[string]bool{},
	}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

// AddToken accepts token with these scopes (nil: every scope).
func (s *Server) AddToken(token string, scopes []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[token] = scopes
}

// AddRepository connects a repository.
func (s *Server) AddRepository(id, fullName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	short := fullName[strings.LastIndex(fullName, "/")+1:]
	s.repos = append(s.repos, Repository{ID: id, Name: short, FullName: fullName, DefaultBranch: "main"})
}

// SetRole sets the member role of every token: owner, admin, member or viewer.
func (s *Server) SetRole(role string) { s.mu.Lock(); s.role = role; s.mu.Unlock() }

// SetFeatureDisabled turns synthetic checks or dashboards off (feature_disabled).
func (s *Server) SetFeatureDisabled(kind string, disabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.disabled[kind] = disabled
}

// SetMaxChecks caps the number of checks per repository (limit_reached); 0 is no cap.
func (s *Server) SetMaxChecks(n int) { s.mu.Lock(); s.maxChecks = n; s.mu.Unlock() }

// FailNext answers the next n requests with status (429 with Retry-After: 0, or a bare 503).
func (s *Server) FailNext(status, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := 0; i < n; i++ {
		s.failures = append(s.failures, injected{status: status, retryAfter: "0"})
	}
}

// Log returns a copy of the request log.
func (s *Server) Log() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Entry(nil), s.log...)
}

// Writes returns the PUT and DELETE entries of the log.
func (s *Server) Writes() []Entry {
	var out []Entry
	for _, e := range s.Log() {
		if e.Method != http.MethodGet {
			out = append(out, e)
		}
	}
	return out
}

// ResetLog empties the request log.
func (s *Server) ResetLog() { s.mu.Lock(); s.log = nil; s.mu.Unlock() }

// Seed stores resource as is (in the API's output shape), filling id,
// repository_id and timestamps when absent. It may create a duplicate name.
// Headers are stored for a check's header_names, with the value "seeded".
func (s *Server) Seed(repositoryID, kind string, resource map[string]any) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	fields := normalizeNumbers(resource).(map[string]any)
	if _, ok := fields["id"]; !ok {
		s.nextID++
		fields["id"] = fmt.Sprintf("%s_%d", idPrefix(kind), s.nextID)
	}
	fields["repository_id"] = repositoryID
	for _, key := range []string{"created_at", "updated_at"} {
		if _, ok := fields[key]; !ok {
			fields[key] = s.timestamp()
		}
	}
	headers := map[string]string{}
	if names, ok := fields["header_names"].([]any); ok {
		for _, n := range names {
			headers[n.(string)] = "seeded"
		}
	}
	s.data[kind][repositoryID] = append(s.data[kind][repositoryID], &stored{fields: fields, headers: headers})
	return copyJSON(fields)
}

// Get returns the resource with this name (the first one), or nil.
func (s *Server) Get(repositoryID, kind, name string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.data[kind][repositoryID] {
		if r.fields["name"] == name {
			return copyJSON(r.fields)
		}
	}
	return nil
}

// Headers returns the stored headers of a check.
func (s *Server) Headers(repositoryID, name string) map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.data[SyntheticChecks][repositoryID] {
		if r.fields["name"] == name {
			out := map[string]string{}
			for k, v := range r.headers {
				out[k] = v
			}
			return out
		}
	}
	return nil
}

// Edit changes a stored resource out of band, as the dashboard would. edit may
// change "header_names" of a check; its headers follow.
func (s *Server) Edit(repositoryID, kind, name string, edit func(fields map[string]any)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.data[kind][repositoryID] {
		if r.fields["name"] == name {
			edit(r.fields)
			r.fields = normalizeNumbers(r.fields).(map[string]any)
			r.fields["updated_at"] = s.timestamp()
			if names, ok := r.fields["header_names"].([]any); ok && kind == SyntheticChecks {
				headers := map[string]string{}
				for _, n := range names {
					headers[n.(string)] = r.headers[n.(string)]
				}
				r.headers = headers
			}
			return
		}
	}
	panic(fmt.Sprintf("mockapi: no %s named %q in %s", kind, name, repositoryID))
}

// Remove deletes a resource out of band.
func (s *Server) Remove(repositoryID, kind, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.remove(repositoryID, kind, name)
}

func (s *Server) remove(repositoryID, kind, name string) {
	list := s.data[kind][repositoryID]
	for i, r := range list {
		if r.fields["name"] == name {
			s.data[kind][repositoryID] = append(list[:i:i], list[i+1:]...)
			return
		}
	}
}

// Count returns how many resources of kind a repository has.
func (s *Server) Count(repositoryID, kind string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.data[kind][repositoryID])
}

func idPrefix(kind string) string {
	switch kind {
	case AlertRules:
		return "iar"
	case SyntheticChecks:
		return "sck"
	}
	return "rdb"
}

// timestamp is a distinct, increasing time for each call, so updated_at moves on every write.
func (s *Server) timestamp() string {
	s.clockTicks++
	return time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC).Add(time.Duration(s.clockTicks) * time.Second).Format("2006-01-02T15:04:05.000Z")
}

// ── HTTP ────────────────────────────────────────────────────────────────────

type answer struct {
	status  int
	body    any
	outcome string
}

func apiError(status int, code, message string, detail any) answer {
	errBody := map[string]any{"code": code, "message": message}
	if detail != nil {
		errBody["detail"] = detail
	}
	return answer{status: status, body: map[string]any{"error": errBody}, outcome: code}
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.failures) > 0 {
		f := s.failures[0]
		s.failures = s.failures[1:]
		s.log = append(s.log, Entry{Method: r.Method, Status: f.status, Outcome: "injected"})
		if f.status == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", f.retryAfter)
			write(w, apiError(429, "rate_limited", "Rate limit exceeded — this token allows 10 write calls per minute.", nil))
			return
		}
		w.WriteHeader(f.status)
		return
	}

	segments := strings.Split(strings.TrimPrefix(r.URL.EscapedPath(), "/api/v1/"), "/")
	for i, seg := range segments {
		if unescaped, err := url.PathUnescape(seg); err == nil {
			segments[i] = unescaped
		}
	}
	entry := Entry{Method: r.Method}
	if len(segments) > 1 {
		entry.Repository = segments[1]
	}
	if len(segments) > 2 {
		entry.Kind = segments[2]
	}
	if len(segments) > 3 {
		entry.Name = segments[3]
	}

	a := s.route(r, segments)
	entry.Status = a.status
	if r.Method != http.MethodGet {
		entry.Outcome = a.outcome
	}
	s.log = append(s.log, entry)
	write(w, a)
}

func write(w http.ResponseWriter, a answer) {
	w.Header().Set("Content-Type", "application/json")
	if a.status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer realm="autter-api"`)
	}
	w.WriteHeader(a.status)
	_ = json.NewEncoder(w).Encode(a.body)
}

func (s *Server) route(r *http.Request, segments []string) answer {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	scopes, known := s.tokens[token]
	if !known || token == "" {
		return apiError(401, "unauthorized", "Access token is not valid. Create one under Settings → Access Tokens.", nil)
	}
	kind := "read"
	if r.Method != http.MethodGet {
		kind = "write"
	}
	if scopes != nil && !contains(scopes, "api:"+kind) {
		hasAPI := false
		for _, sc := range scopes {
			hasAPI = hasAPI || strings.HasPrefix(sc, "api:")
		}
		if !hasAPI {
			return apiError(403, "insufficient_scope", "This token is restricted to MCP. Create a token with the api:read and api:write scopes.", nil)
		}
		return apiError(403, "insufficient_scope", fmt.Sprintf("This token lacks the api:%s scope this request needs.", kind), nil)
	}

	switch {
	case len(segments) == 1 && segments[0] == "repositories" && r.Method == http.MethodGet:
		// Ordered by full name, as the API does.
		repos := append([]Repository(nil), s.repos...)
		sort.Slice(repos, func(i, j int) bool { return repos[i].FullName < repos[j].FullName })
		data := []any{}
		for _, repo := range repos {
			data = append(data, map[string]any{"id": repo.ID, "name": repo.Name, "full_name": repo.FullName, "default_branch": repo.DefaultBranch, "enabled": true})
		}
		return answer{status: 200, body: map[string]any{"data": data}}
	case len(segments) == 3 || len(segments) == 4:
		if segments[0] != "repositories" {
			break
		}
		resource := segments[2]
		if _, ok := s.data[resource]; !ok {
			return apiError(404, "not_found", fmt.Sprintf("Unknown resource %q.", resource), nil)
		}
		repo, problem := s.findRepository(segments[1])
		if problem != nil {
			return *problem
		}
		if s.disabled[resource] {
			return apiError(403, "feature_disabled", fmt.Sprintf("%s are not enabled for this organization. An owner or admin can turn them on in the dashboard.", label(resource)), nil)
		}
		if len(segments) == 3 {
			if r.Method != http.MethodGet {
				break
			}
			data := []any{}
			for _, res := range s.data[resource][repo.ID] {
				data = append(data, s.output(resource, res))
			}
			return answer{status: 200, body: map[string]any{"data": data}}
		}
		name := strings.TrimSpace(segments[3])
		if name == "" {
			return apiError(400, "invalid_argument", "Give the name in the path.", nil)
		}
		switch r.Method {
		case http.MethodGet:
			found := s.byName(resource, repo.ID, name)
			if len(found) == 0 {
				return apiError(404, "not_found", fmt.Sprintf("No %s named %q in %s.", singular(resource), name, repo.FullName), nil)
			}
			if len(found) > 1 {
				return conflict(resource, name, found)
			}
			return answer{status: 200, body: s.output(resource, found[0])}
		case http.MethodPut:
			return s.put(r, resource, repo, name)
		case http.MethodDelete:
			found := s.byName(resource, repo.ID, name)
			if len(found) == 0 {
				return apiError(404, "not_found", fmt.Sprintf("No %s named %q in %s.", singular(resource), name, repo.FullName), nil)
			}
			if len(found) > 1 {
				return conflict(resource, name, found)
			}
			if !s.mayWrite(resource) {
				return roleError(resource, "delete")
			}
			s.remove(repo.ID, resource, name)
			return answer{status: 200, body: map[string]any{"deleted": true, "id": found[0].fields["id"]}, outcome: "deleted"}
		}
	}
	return apiError(404, "not_found", fmt.Sprintf("No route %s /api/v1/%s.", r.Method, strings.Join(segments, "/")), nil)
}

func label(kind string) string {
	switch kind {
	case SyntheticChecks:
		return "Synthetic checks"
	case Dashboards:
		return "Runtime dashboards"
	}
	return "Alert rules"
}

func singular(kind string) string {
	switch kind {
	case SyntheticChecks:
		return "synthetic check"
	case Dashboards:
		return "dashboard"
	}
	return "alert rule"
}

func (s *Server) mayWrite(kind string) bool {
	if kind == Dashboards {
		return s.role != "viewer"
	}
	return s.role == "owner" || s.role == "admin"
}

func roleError(kind, action string) answer {
	who := "an owner or admin"
	if kind == Dashboards {
		who = "a member who is not a viewer"
	}
	return apiError(403, "insufficient_role", fmt.Sprintf("Only %s can %s a %s.", who, action, singular(kind)), nil)
}

func conflict(kind, name string, found []*stored) answer {
	ids := []any{}
	for _, r := range found {
		ids = append(ids, r.fields["id"])
	}
	return apiError(409, "name_conflict", fmt.Sprintf("%d %ss in this repository are named %q. Rename all but one in the dashboard; the API addresses a %s by its name.", len(found), singular(kind), name, singular(kind)), map[string]any{"ids": ids})
}

func (s *Server) findRepository(ref string) (*Repository, *answer) {
	ref = strings.TrimSpace(ref)
	notFound := apiError(404, "repository_not_found", fmt.Sprintf("No repository %q is connected to this organization. GET /api/v1/repositories lists them.", ref), nil)
	for i := range s.repos {
		if s.repos[i].ID == ref {
			return &s.repos[i], nil
		}
	}
	if strings.Contains(ref, "/") {
		for i := range s.repos {
			if strings.EqualFold(s.repos[i].FullName, ref) {
				return &s.repos[i], nil
			}
		}
		return nil, &notFound
	}
	var matches []*Repository
	for i := range s.repos {
		if s.repos[i].Name == ref {
			matches = append(matches, &s.repos[i])
		}
	}
	if len(matches) > 1 {
		candidates := []any{}
		for _, m := range matches {
			candidates = append(candidates, m.FullName)
		}
		a := apiError(404, "repository_not_found", fmt.Sprintf("Repository name %q is ambiguous in this organization: use its id or its full owner/repo name.", ref), map[string]any{"candidates": candidates})
		return nil, &a
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	return nil, &notFound
}

func (s *Server) byName(kind, repositoryID, name string) []*stored {
	var out []*stored
	for _, r := range s.data[kind][repositoryID] {
		if r.fields["name"] == name {
			out = append(out, r)
		}
	}
	return out
}

func (s *Server) output(kind string, r *stored) map[string]any {
	out := copyJSON(r.fields)
	if kind == SyntheticChecks {
		out["header_names"] = headerNames(r.headers)
	}
	return out
}

func (s *Server) put(r *http.Request, kind string, repo *Repository, name string) answer {
	var body map[string]any
	decoder := json.NewDecoder(r.Body)
	decoder.UseNumber()
	if err := decoder.Decode(&body); err != nil || body == nil {
		return apiError(400, "invalid_argument", "The request body is not valid JSON.", nil)
	}
	if sent, ok := body["name"].(string); ok && strings.TrimSpace(sent) != name {
		return apiError(400, "invalid_argument", fmt.Sprintf("The body names %q but the path names %q.", strings.TrimSpace(sent), name), nil)
	}
	found := s.byName(kind, repo.ID, name)
	if len(found) > 1 {
		return conflict(kind, name, found)
	}
	var fields map[string]any
	var headers map[string]string
	var err error
	switch kind {
	case AlertRules:
		fields, err = normalizeAlertRule(body)
	case SyntheticChecks:
		currentKind := ""
		if len(found) == 1 {
			currentKind = "http"
			if k, ok := found[0].fields["kind"].(string); ok && k != "" {
				currentKind = k
			}
		}
		fields, headers, err = normalizeCheck(body, currentKind)
	case Dashboards:
		fields, err = normalizeDashboard(body)
	}
	if err != nil {
		return apiError(400, "invalid_argument", err.Error(), nil)
	}
	fields["name"] = name

	if len(found) == 0 {
		if !s.mayWrite(kind) {
			return roleError(kind, "create")
		}
		if kind == SyntheticChecks && s.maxChecks > 0 && len(s.data[kind][repo.ID]) >= s.maxChecks {
			return apiError(422, "limit_reached", fmt.Sprintf("Your plan includes %d synthetic checks. Upgrade to add more.", s.maxChecks), nil)
		}
		s.nextID++
		now := s.timestamp()
		fields["id"] = fmt.Sprintf("%s_%d", idPrefix(kind), s.nextID)
		fields["repository_id"] = repo.ID
		fields["created_by"] = "u-mock"
		fields["created_at"] = now
		fields["updated_at"] = now
		switch kind {
		case AlertRules:
			fields["is_default"] = false
		case SyntheticChecks:
			addCheckStatus(fields, now)
		}
		res := &stored{fields: fields, headers: headers}
		s.data[kind][repo.ID] = append(s.data[kind][repo.ID], res)
		return answer{status: 201, body: s.output(kind, res), outcome: "created"}
	}
	current := found[0]
	if !s.mayWrite(kind) {
		return roleError(kind, "change")
	}
	same := true
	for key, value := range fields {
		if !reflect.DeepEqual(canonical(current.fields[key]), canonical(value)) {
			same = false
		}
	}
	if kind == SyntheticChecks && !reflect.DeepEqual(current.headers, headers) {
		same = false
	}
	if same {
		return answer{status: 200, body: s.output(kind, current), outcome: "unchanged"}
	}
	for key, value := range fields {
		current.fields[key] = value
	}
	if kind == SyntheticChecks {
		current.headers = headers
	}
	current.fields["updated_at"] = s.timestamp()
	return answer{status: 200, body: s.output(kind, current), outcome: "updated"}
}

func addCheckStatus(fields map[string]any, now string) {
	fields["last_ok"] = nil
	fields["last_status"] = nil
	fields["last_duration_ms"] = nil
	fields["last_error"] = nil
	fields["last_checked_at"] = nil
	fields["next_due_at"] = now
	fields["consecutive_failures"] = json.Number("0")
	fields["firing"] = false
	fields["issue_id"] = "iss_" + fields["id"].(string)
	fields["first_passed_at"] = nil
}

// canonical round-trips v through JSON so equal values compare equal whatever their Go types.
func canonical(v any) any {
	raw, _ := json.Marshal(v)
	var out any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	_ = decoder.Decode(&out)
	return out
}

func normalizeNumbers(v any) any { return canonical(v) }

func copyJSON(v map[string]any) map[string]any {
	out, _ := canonical(v).(map[string]any)
	return out
}

// SortedNames returns the names of a repository's resources of kind, sorted.
func (s *Server) SortedNames(repositoryID, kind string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var names []string
	for _, r := range s.data[kind][repositoryID] {
		names = append(names, r.fields["name"].(string))
	}
	sort.Strings(names)
	return names
}
