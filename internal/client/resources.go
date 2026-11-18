package client

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// Kind is a resource collection of the API, as it appears in the path.
type Kind string

const (
	AlertRules      Kind = "alert-rules"
	SyntheticChecks Kind = "synthetic-checks"
	Dashboards      Kind = "dashboards"
)

// Label is the kind in words, for messages: "alert rule".
func (k Kind) Label() string {
	switch k {
	case AlertRules:
		return "alert rule"
	case SyntheticChecks:
		return "synthetic check"
	case Dashboards:
		return "dashboard"
	}
	return string(k)
}

// Resource is one alert rule, synthetic check or dashboard as the API returns
// it: snake_case fields, numbers kept as json.Number.
type Resource = map[string]any

// Repository is a repository connected to the token's organization.
type Repository struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	FullName      string  `json:"full_name"`
	DefaultBranch *string `json:"default_branch"`
	Enabled       bool    `json:"enabled"`
}

func listKey(kind Kind, repository string) string {
	return string(kind) + "\x00" + repository
}

// ListResources lists the resources of one kind in a repository. Reads are
// cached for a minute and dropped on any write of the same kind.
func (c *Client) ListResources(ctx context.Context, repository string, kind Kind) ([]Resource, error) {
	value, err := c.lists.get(ctx, listKey(kind, repository), func(ctx context.Context) (any, error) {
		path := escape("repositories", repository, string(kind))
		_, body, err := c.do(ctx, http.MethodGet, path, nil)
		if err != nil {
			return nil, err
		}
		var list struct {
			Data []Resource `json:"data"`
		}
		if err := decodeJSON(body, &list); err != nil {
			return nil, err
		}
		return list.Data, nil
	})
	if err != nil {
		return nil, err
	}
	return value.([]Resource), nil
}

// FindResource returns the one resource of this kind with this name. It reads
// the cached list, and answers like the API: not_found when there is none,
// name_conflict when two or more share the name.
func (c *Client) FindResource(ctx context.Context, repository string, kind Kind, name string) (Resource, error) {
	all, err := c.ListResources(ctx, repository, kind)
	if err != nil {
		return nil, err
	}
	var found []Resource
	for _, resource := range all {
		if n, _ := resource["name"].(string); n == name {
			found = append(found, resource)
		}
	}
	path := escape("repositories", repository, string(kind), name)
	switch len(found) {
	case 0:
		return nil, &APIError{Status: http.StatusNotFound, Code: CodeNotFound, Message: fmt.Sprintf("No %s named %q in %s.", kind.Label(), name, repository), Method: http.MethodGet, Path: path}
	case 1:
		return found[0], nil
	default:
		ids := make([]string, 0, len(found))
		for _, resource := range found {
			id, _ := resource["id"].(string)
			ids = append(ids, fmt.Sprintf("%q", id))
		}
		return nil, &APIError{
			Status:  http.StatusConflict,
			Code:    CodeNameConflict,
			Message: fmt.Sprintf("%d %ss in this repository are named %q. Rename all but one in the dashboard; the API addresses a %s by its name.", len(found), kind.Label(), name, kind.Label()),
			Detail:  []byte(`{"ids":[` + strings.Join(ids, ",") + `]}`),
			Method:  http.MethodGet,
			Path:    path,
		}
	}
}

// PutResource creates the resource with this name or makes the existing one
// match body. created is true for 201, false when a resource with the name
// already existed (updated or unchanged).
func (c *Client) PutResource(ctx context.Context, repository string, kind Kind, name string, body map[string]any) (Resource, bool, error) {
	defer c.lists.invalidate(string(kind) + "\x00")
	status, respBody, err := c.do(ctx, http.MethodPut, escape("repositories", repository, string(kind), name), body)
	if err != nil {
		return nil, false, err
	}
	var resource Resource
	if err := decodeJSON(respBody, &resource); err != nil {
		return nil, false, err
	}
	return resource, status == http.StatusCreated, nil
}

// DeleteResource deletes the resource with this name.
func (c *Client) DeleteResource(ctx context.Context, repository string, kind Kind, name string) error {
	defer c.lists.invalidate(string(kind) + "\x00")
	_, _, err := c.do(ctx, http.MethodDelete, escape("repositories", repository, string(kind), name), nil)
	return err
}

// ListRepositories lists the organization's repositories, cached for a minute.
func (c *Client) ListRepositories(ctx context.Context) ([]Repository, error) {
	value, err := c.repositories.get(ctx, "repositories", func(ctx context.Context) (any, error) {
		_, body, err := c.do(ctx, http.MethodGet, "/repositories", nil)
		if err != nil {
			return nil, err
		}
		var list struct {
			Data []Repository `json:"data"`
		}
		if err := decodeJSON(body, &list); err != nil {
			return nil, err
		}
		return list.Data, nil
	})
	if err != nil {
		return nil, err
	}
	return value.([]Repository), nil
}
