package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	orgID     = "3f2c9a52-0d7e-4b1f-9a64-5c2e8d1b7f30"
	projectID = "7a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d"
	mainID    = "11111111-2222-4333-8444-555555555555"
	prID      = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	access    = "eyJhbGciOiJSUzI1NiJ9.access-token-value"
)

// branchFake is the customer API's branch routes, with what each request carried.
type branchFake struct {
	t        *testing.T
	server   *httptest.Server
	status   map[string]int    // "METHOD name" -> a status to answer with instead of success
	code     map[string]string // the error code to go with it
	auth     []string
	created  []map[string]any
	deleted  []string
	password string
}

func newBranchFake(t *testing.T) *branchFake {
	f := &branchFake{t: t, status: map[string]int{}, code: map[string]string{}, password: "p4ssw0rd-once"}
	base := "/v1/organizations/" + orgID + "/projects/" + projectID + "/branches"
	conn := map[string]any{"host": "db-abc.koedodb.example", "port": 5432, "database": "postgres", "user": "postgres", "sslmode": "require"}
	mainBranch := map[string]any{"id": mainID, "project_id": projectID, "name": "main", "kind": "main", "status": "active", "created_at": "2026-10-20T10:00:00Z", "expires_at": nil, "connection": conn}
	prBranch := map[string]any{"id": prID, "project_id": projectID, "name": "pr-123", "kind": "branch", "status": "active", "created_at": "2026-10-23T10:00:00Z", "expires_at": "2026-10-26T10:00:00Z", "connection": conn}
	mux := http.NewServeMux()
	refuse := func(w http.ResponseWriter, key string) bool {
		if code := f.status[key]; code != 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": f.code[key]})
			return true
		}
		return false
	}
	mux.HandleFunc("GET "+base, func(w http.ResponseWriter, r *http.Request) {
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		if refuse(w, "list") {
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"branches": []any{mainBranch, prBranch}})
	})
	mux.HandleFunc("POST "+base, func(w http.ResponseWriter, r *http.Request) {
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.created = append(f.created, in)
		if refuse(w, "create") {
			return
		}
		answer := map[string]any{}
		for k, v := range prBranch {
			answer[k] = v
		}
		answer["name"], answer["status"] = in["name"], "creating"
		if f.password != "" {
			answer["password"] = f.password
		}
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(answer)
	})
	mux.HandleFunc("DELETE "+base+"/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		f.deleted = append(f.deleted, r.PathValue("id"))
		if refuse(w, "delete") {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	f.server = httptest.NewTLSServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *branchFake) run(args []string, vars map[string]string) (int, string, string) {
	var out, errOut bytes.Buffer
	env := func(k string) string {
		switch k {
		case "KOEDODB_ACCESS_TOKEN":
			return access
		case "KOEDODB_ORGANIZATION":
			return orgID
		case "KOEDODB_PROJECT":
			return projectID
		case "KOEDODB_API_URL":
			return f.server.URL + "/v1"
		}
		return vars[k]
	}
	if v, ok := vars["KOEDODB_ACCESS_TOKEN"]; ok {
		wrapped := env
		env = func(k string) string {
			if k == "KOEDODB_ACCESS_TOKEN" {
				return v
			}
			return wrapped(k)
		}
	}
	code := run(context.Background(), append([]string{"branch"}, args...), env, &out, &errOut, f.server.Client())
	return code, out.String(), errOut.String()
}

func TestBranchCreateShowsWhereItAnswersAndThePasswordOnce(t *testing.T) {
	f := newBranchFake(t)
	code, out, errOut := f.run([]string{"create", "pr-456", "--days", "3"}, nil)
	if code != exitOK {
		t.Fatalf("exit %d: %s %s", code, out, errOut)
	}
	if len(f.created) != 1 || f.created[0]["name"] != "pr-456" || f.created[0]["expires_in_days"] != float64(3) {
		t.Errorf("the request: %v", f.created)
	}
	if f.auth[0] != "Bearer "+access {
		t.Errorf("the token was sent as %q", f.auth[0])
	}
	for _, want := range []string{"pr-456", "db-abc.koedodb.example", "5432", "postgresql://postgres@db-abc.koedodb.example:5432/postgres?sslmode=require", "password  p4ssw0rd-once"} {
		if !strings.Contains(out, want) {
			t.Errorf("the output has no %q:\n%s", want, out)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "address") && strings.Contains(line, "p4ssw0rd") {
			t.Errorf("the address has the password in it: %q", line)
		}
	}
	if !strings.Contains(errOut, "shown once") {
		t.Errorf("no warning that the password is shown once: %q", errOut)
	}
}

func TestBranchCreateJSONHasThePasswordAndNothingElseIsPrinted(t *testing.T) {
	f := newBranchFake(t)
	code, out, errOut := f.run([]string{"create", "try-x", "--json"}, nil)
	if code != exitOK || errOut != "" {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	var got struct {
		Name     string `json:"name"`
		Password string `json:"password"`
		Conn     struct {
			Host string `json:"host"`
		} `json:"connection"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.Name != "try-x" || got.Password != "p4ssw0rd-once" || got.Conn.Host == "" {
		t.Fatalf("the object: %v %s", err, out)
	}
	if f.created[0]["expires_in_days"] != float64(0) {
		t.Errorf("without --days the branch has no end: %v", f.created[0])
	}
}

func TestBranchCreateWithNoPasswordSaysSo(t *testing.T) {
	f := newBranchFake(t)
	f.password = ""
	code, out, errOut := f.run([]string{"create", "pr-9"}, nil)
	if code != exitOK || strings.Contains(out, "password") || !strings.Contains(errOut, "no password") {
		t.Errorf("exit %d\n%s\n%s", code, out, errOut)
	}
}

func TestBranchListAndGetByNameOrId(t *testing.T) {
	f := newBranchFake(t)
	code, out, _ := f.run([]string{"list"}, nil)
	if code != exitOK || !strings.Contains(out, "main") || !strings.Contains(out, "pr-123") || !strings.Contains(out, "2026-10-26 10:00 UTC") || !strings.Contains(out, prID) {
		t.Errorf("list: %d\n%s", code, out)
	}
	code, out, _ = f.run([]string{"get", "pr-123"}, nil)
	if code != exitOK || !strings.Contains(out, "name      pr-123") || !strings.Contains(out, "expires   2026-10-26") {
		t.Errorf("get by name: %d\n%s", code, out)
	}
	code, out, _ = f.run([]string{"get", prID, "--json"}, nil)
	var b struct {
		Name string `json:"name"`
	}
	if code != exitOK || json.Unmarshal([]byte(out), &b) != nil || b.Name != "pr-123" {
		t.Errorf("get by id: %d %s", code, out)
	}
	code, _, errOut := f.run([]string{"get", "nope"}, nil)
	if code != exitInput || !strings.Contains(errOut, "no such branch") {
		t.Errorf("an unknown branch: %d %s", code, errOut)
	}
}

func TestBranchDeleteNeedsYesAndFindsTheBranchByName(t *testing.T) {
	f := newBranchFake(t)
	code, _, errOut := f.run([]string{"delete", "pr-123"}, nil)
	if code != exitUsage || !strings.Contains(errOut, "--yes") || len(f.deleted) != 0 || len(f.auth) != 0 {
		t.Errorf("without --yes: %d %q, sent %d requests", code, errOut, len(f.auth))
	}
	code, out, _ := f.run([]string{"delete", "pr-123", "--yes"}, nil)
	if code != exitOK || len(f.deleted) != 1 || f.deleted[0] != prID || !strings.Contains(out, "deleted pr-123") {
		t.Errorf("delete: %d %q %v", code, out, f.deleted)
	}
	code, out, _ = f.run([]string{"delete", "pr-123", "--yes", "--json"}, nil)
	if code != exitOK || !strings.Contains(out, `"deleted":true`) {
		t.Errorf("delete --json: %d %q", code, out)
	}
}

func TestEachBranchRefusalHasItsExitStatusAndAMessageThatSaysWhatToDo(t *testing.T) {
	cases := []struct {
		name   string
		args   []string
		key    string
		status int
		code   string
		exit   int
		text   string
	}{
		{"limit", []string{"create", "pr-3"}, "create", 403, "branch_limit_reached", exitInput, "most branches"},
		{"not owner", []string{"create", "pr-3"}, "create", 403, "forbidden", exitAuth, "owner or an admin"},
		{"name in use", []string{"create", "pr-123"}, "create", 409, "branch_name_in_use", exitInput, "exists already"},
		{"invalid name", []string{"create", "Bad_Name"}, "create", 400, "invalid_branch", exitInput, "lower-case letters"},
		{"bad expiry", []string{"create", "pr-3", "--days", "9"}, "create", 400, "invalid_expiry", exitInput, "0 to 3650"},
		{"no capacity", []string{"create", "pr-3"}, "create", 503, "no_capacity", exitServer, "Nothing was queued"},
		{"project not ready", []string{"create", "pr-3"}, "create", 409, "project_not_ready", exitServer, "not active yet"},
		{"unknown project", []string{"list"}, "list", 404, "project_not_found", exitInput, "does not know this project"},
		{"token refused", []string{"list"}, "list", 401, "unauthorized", exitAuth, "access token was not accepted"},
		{"main", []string{"delete", "main", "--yes"}, "delete", 409, "main_branch_cannot_be_deleted", exitInput, "own database"},
		{"server", []string{"list"}, "list", 500, "", exitServer, "Run it again"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newBranchFake(t)
			f.status[c.key], f.code[c.key] = c.status, c.code
			code, out, errOut := f.run(c.args, nil)
			if code != c.exit || !strings.Contains(errOut, c.text) || out != "" {
				t.Errorf("exit %d (want %d), stdout %q, stderr %q", code, c.exit, out, errOut)
			}
			// With --json the same failure is one object.
			code, out, _ = f.run(append(append([]string{}, c.args...), "--json"), nil)
			var obj struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if code != c.exit || json.Unmarshal([]byte(out), &obj) != nil || obj.Error.Code == "" {
				t.Errorf("--json: exit %d, %q", code, out)
			}
		})
	}
}

func TestBranchUsageAndTheTokenAreCheckedBeforeAnythingIsSent(t *testing.T) {
	f := newBranchFake(t)
	bad := []struct {
		args []string
		vars map[string]string
		exit int
	}{
		{[]string{}, nil, exitUsage},
		{[]string{"frobnicate"}, nil, exitUsage},
		{[]string{"create"}, nil, exitUsage},
		{[]string{"create", "a", "b"}, nil, exitUsage},
		{[]string{"list", "x"}, nil, exitUsage},
		{[]string{"create", "pr-1", "--days", "-1"}, nil, exitUsage},
		{[]string{"create", "pr-1", "--days", "4000"}, nil, exitUsage},
		{[]string{"list", "--project", "not-an-id"}, nil, exitUsage},
		{[]string{"list", "--organization", "not-an-id"}, nil, exitUsage},
		{[]string{"list"}, map[string]string{"KOEDODB_ACCESS_TOKEN": ""}, exitAuth},
		{[]string{"list"}, map[string]string{"KOEDODB_ACCESS_TOKEN": "has a space"}, exitAuth},
		{[]string{"list", "--api", "http://example.test/v1"}, nil, exitUsage},
	}
	for _, c := range bad {
		code, _, _ := f.run(c.args, c.vars)
		if code != c.exit {
			t.Errorf("%v %v: exit %d, want %d", c.args, c.vars, code, c.exit)
		}
	}
	if len(f.auth) != 0 {
		t.Errorf("%d requests were sent", len(f.auth))
	}
}

func TestBranchFlagsOverrideTheEnvironmentAndMayFollowTheName(t *testing.T) {
	f := newBranchFake(t)
	other := "99999999-8888-4777-8666-555555555555"
	// A different project: the fake knows only its own paths, so the answer is 404 from the server's mux (a plain 404, no code).
	code, _, errOut := f.run([]string{"list", "--project", other}, nil)
	if code != exitInput || !strings.Contains(errOut, "does not know this project") {
		t.Errorf("--project: %d %q", code, errOut)
	}
	if code, _, _ := f.run([]string{"create", "pr-8", "--days", "2", "--json"}, nil); code != exitOK {
		t.Errorf("flags after the name: %d", code)
	}
}

func TestTheTextFromTheServerCannotDriveTheTerminal(t *testing.T) {
	if got := safeText("pr-1\x1b[31mred\x07"); strings.ContainsAny(got, "\x1b\x07") {
		t.Errorf("escape sequence left in %q", got)
	}
	if got := safeText(strings.Repeat("a", 500)); len(got) != 200 {
		t.Errorf("not cut: %d", len(got))
	}
}
