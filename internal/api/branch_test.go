package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBranchRoutesAreOfTheProjectAndCarryTheTokenAsABearer(t *testing.T) {
	var seen []string
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		seen = append(seen, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
		switch r.Method {
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		case http.MethodPost:
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"id":"b1","name":"pr-1","password":"pw"}`))
		default:
			_, _ = w.Write([]byte(`{"branches":[{"id":"b1","name":"main","kind":"main"}]}`))
		}
	}))
	defer server.Close()
	c := &Client{BaseURL: server.URL + "/v1", Token: "access"}
	s := Scope{Organization: "org-1", Project: "prj-1"}
	ctx := context.Background()
	list, err := c.ListBranches(ctx, s)
	if err != nil || len(list) != 1 || list[0].Kind != "main" {
		t.Fatalf("list: %v %v", list, err)
	}
	created, err := c.CreateBranch(ctx, s, "pr-1", 3)
	if err != nil || created.Password != "pw" || created.Name != "pr-1" || body != `{"expires_in_days":3,"name":"pr-1"}` {
		t.Fatalf("create: %+v %v %s", created, err, body)
	}
	if err := c.DeleteBranch(ctx, s, "b1"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"GET /v1/organizations/org-1/projects/prj-1/branches Bearer access",
		"POST /v1/organizations/org-1/projects/prj-1/branches Bearer access",
		"DELETE /v1/organizations/org-1/projects/prj-1/branches/b1 Bearer access",
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Errorf("request %d: %q, want %q", i, seen[i], want[i])
		}
	}
}

func TestAnIdWithASlashCannotChangeThePath(t *testing.T) {
	s := Scope{Organization: "o/../x", Project: "p"}
	if got := s.path(); got != "/organizations/o%2F..%2Fx/projects/p/branches" {
		t.Errorf("path %q", got)
	}
}
