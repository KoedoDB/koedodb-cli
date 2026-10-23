package api

import (
	"context"
	"net/http"
	"net/url"
	"time"
)

// The branch routes are the customer API's, not the deploy token's: the Bearer is the access token of a person's login, and the Project is named in the path.

// Scope names the Project whose branches are meant (both are ids, UUIDs).
type Scope struct {
	Organization string
	Project      string
}

func (s Scope) path() string {
	return "/organizations/" + url.PathEscape(s.Organization) + "/projects/" + url.PathEscape(s.Project) + "/branches"
}

// Connection is where a branch's database answers: straight to the database, not through the pooler.
type Connection struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Database string `json:"database"`
	User     string `json:"user"`
	SSLMode  string `json:"sslmode"`
}

// Branch is a branch as KoedoDB has it. Kind is "main" (the Project's own database, which cannot be deleted here) or "branch".
type Branch struct {
	ID         string     `json:"id"`
	ProjectID  string     `json:"project_id"`
	Name       string     `json:"name"`
	Kind       string     `json:"kind"`
	Status     string     `json:"status"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
	Connection Connection `json:"connection"`
}

// CreatedBranch is the answer to a new branch: the branch, and the password of its database user, once. The password is empty when KoedoDB could not make one in the same request.
type CreatedBranch struct {
	Branch
	Password string `json:"password,omitempty"`
}

// ListBranches lists the branches of the Project, main included.
func (c *Client) ListBranches(ctx context.Context, s Scope) ([]Branch, error) {
	var out struct {
		Branches []Branch `json:"branches"`
	}
	err := c.do(ctx, http.MethodGet, s.path(), nil, &out)
	return out.Branches, err
}

// GetBranch asks for one branch by its id.
func (c *Client) GetBranch(ctx context.Context, s Scope, id string) (Branch, error) {
	var b Branch
	err := c.do(ctx, http.MethodGet, s.path()+"/"+url.PathEscape(id), nil, &b)
	return b, err
}

// CreateBranch makes an empty branch. days is 0 for a branch that stays until it is deleted.
func (c *Client) CreateBranch(ctx context.Context, s Scope, name string, days int) (CreatedBranch, error) {
	var b CreatedBranch
	err := c.do(ctx, http.MethodPost, s.path(), map[string]any{"name": name, "expires_in_days": days}, &b)
	return b, err
}

// DeleteBranch deletes a branch (the database goes at once).
func (c *Client) DeleteBranch(ctx context.Context, s Scope, id string) error {
	return c.do(ctx, http.MethodDelete, s.path()+"/"+url.PathEscape(id), nil, nil)
}
