package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const token = "kdb_test_token_value"

// fake is KoedoDB's API and its object store in one server (the client trusts one certificate).
type fake struct {
	t       *testing.T
	server  *httptest.Server
	status  map[string]int // path -> status to answer with instead of success
	apiAuth []string
	putAuth string
	putSize int64
	putBody []byte
	asked   struct {
		sha   string
		bytes int64
	}
	complete int
}

func newFake(t *testing.T) *fake {
	f := &fake{t: t, status: map[string]int{}}
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, path string, ok func()) {
		if code := f.status[path]; code != 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			code2 := map[int]string{409: "project_not_active", 422: "archive_not_uploaded"}[code]
			_ = json.NewEncoder(w).Encode(map[string]string{"error": code2})
			return
		}
		ok()
	}
	mux.HandleFunc("POST /v1/site/uploads", func(w http.ResponseWriter, r *http.Request) {
		f.apiAuth = append(f.apiAuth, r.Header.Get("Authorization"))
		reply(w, "request", func() {
			var in struct {
				SHA256 string `json:"sha256"`
				Bytes  int64  `json:"bytes"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			f.asked.sha, f.asked.bytes = in.SHA256, in.Bytes
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "u1", "method": "PUT", "url": f.server.URL + "/store/a.tar.gz?X-Amz-Signature=secret-signature"})
		})
	})
	mux.HandleFunc("PUT /store/a.tar.gz", func(w http.ResponseWriter, r *http.Request) {
		f.putAuth, f.putSize = r.Header.Get("Authorization"), r.ContentLength
		f.putBody, _ = io.ReadAll(r.Body)
		if code := f.status["put"]; code != 0 {
			w.WriteHeader(code)
			return
		}
	})
	mux.HandleFunc("POST /v1/site/uploads/u1/complete", func(w http.ResponseWriter, r *http.Request) {
		f.apiAuth = append(f.apiAuth, r.Header.Get("Authorization"))
		f.complete++
		reply(w, "complete", func() {
			_ = json.NewEncoder(w).Encode(map[string]any{"published": true, "bytes": f.asked.bytes, "sha256": f.asked.sha, "url": "https://my-app.koedodb.dev", "updated_at": "2026-10-12T12:00:00Z"})
		})
	})
	f.server = httptest.NewTLSServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func site(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func (f *fake) run(args []string, tokenValue string) (int, string, string) {
	var out, errOut bytes.Buffer
	env := func(k string) string {
		switch k {
		case "KOEDODB_TOKEN":
			return tokenValue
		case "KOEDODB_API_URL":
			return f.server.URL + "/v1"
		}
		return ""
	}
	code := run(context.Background(), args, env, &out, &errOut, f.server.Client())
	return code, out.String(), errOut.String()
}

func TestDeployPacksUploadsAndPublishes(t *testing.T) {
	f := newFake(t)
	dir := site(t, map[string]string{"index.html": "<h1>hi</h1>", "assets/app.js": "console.log(1)"})
	code, out, errOut := f.run([]string{"deploy", dir}, token)
	if code != 0 || strings.TrimSpace(out) != "https://my-app.koedodb.dev" {
		t.Fatalf("exit %d, out %q, err %q", code, out, errOut)
	}
	// What was claimed is what was sent: the size is signed into the URL, the hash is the archive's.
	sum := sha256.Sum256(f.putBody)
	if f.asked.sha != hex.EncodeToString(sum[:]) || f.asked.bytes != int64(len(f.putBody)) || f.putSize != int64(len(f.putBody)) {
		t.Errorf("claimed %s %d, sent %x %d (content-length %d)", f.asked.sha, f.asked.bytes, sum, len(f.putBody), f.putSize)
	}
	if _, err := gzip.NewReader(bytes.NewReader(f.putBody)); err != nil {
		t.Errorf("what was sent is not gzip: %v", err)
	}
	// The token goes to the API only, never to the store; and nothing the CLI prints has the token or the signed address in it.
	for _, h := range f.apiAuth {
		if h != "Bearer "+token {
			t.Errorf("an API call without the token: %q", h)
		}
	}
	if f.putAuth != "" {
		t.Errorf("the token was sent to the store: %q", f.putAuth)
	}
	for _, shown := range []string{out, errOut} {
		if strings.Contains(shown, token) || strings.Contains(shown, "secret-signature") {
			t.Errorf("a secret is printed: %q", shown)
		}
	}
	if !strings.Contains(errOut, "Packing") || !strings.Contains(errOut, "Deployed") {
		t.Errorf("progress: %q", errOut)
	}
}

func TestDeployJSONPrintsOneObjectAndNothingElse(t *testing.T) {
	f := newFake(t)
	dir := site(t, map[string]string{"index.html": "x"})
	// A flag after the directory works too.
	code, out, errOut := f.run([]string{"deploy", dir, "--json"}, token)
	var result struct {
		Files  int    `json:"files"`
		SHA256 string `json:"sha256"`
		URL    string `json:"url"`
	}
	if code != 0 || errOut != "" || json.Unmarshal([]byte(out), &result) != nil || result.Files != 1 || len(result.SHA256) != 64 || result.URL == "" {
		t.Fatalf("exit %d, out %q, err %q", code, out, errOut)
	}
	code, out, errOut = f.run([]string{"deploy", "--json", filepath.Join(t.TempDir(), "nope")}, token)
	var failure struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	if code != exitInput || errOut != "" || json.Unmarshal([]byte(out), &failure) != nil || failure.Error.Code != "invalid_directory" || failure.Error.Message == "" {
		t.Fatalf("exit %d, out %q, err %q", code, out, errOut)
	}
}

func TestDeployWithNoDirectoryTakesDistBuildOrOut(t *testing.T) {
	f := newFake(t)
	work := site(t, map[string]string{"build/index.html": "x", "src/main.js": "y"})
	t.Chdir(work)
	if code, out, errOut := f.run([]string{"deploy"}, token); code != 0 || out == "" {
		t.Fatalf("exit %d, out %q, err %q", code, out, errOut)
	}
	empty := t.TempDir()
	t.Chdir(empty)
	if code, _, errOut := f.run([]string{"deploy"}, token); code != exitInput || !strings.Contains(errOut, "no directory given") {
		t.Fatalf("exit %d, err %q", code, errOut)
	}
}

func TestEachFailureHasItsExitStatusAndAMessageThatSaysWhatToDo(t *testing.T) {
	dir := site(t, map[string]string{"index.html": "x"})
	cases := []struct {
		what   string
		status map[string]int
		want   int
		text   string
	}{
		{"the token is refused", map[string]int{"request": 401}, exitAuth, "make a new one"},
		{"the project is not the token's", map[string]int{"request": 404}, exitAuth, "does not know this project"},
		{"the site is too large", map[string]int{"request": 413}, exitInput, "100 MiB"},
		{"the project is not active", map[string]int{"request": 409}, exitServer, "not active yet"},
		{"the server is down", map[string]int{"request": 503}, exitServer, "run it again"},
		{"the store refuses the upload", map[string]int{"put": 403}, exitServer, "run it again"},
		{"the archive is not in the store", map[string]int{"complete": 422}, exitServer, "run it again"},
	}
	for _, c := range cases {
		f := newFake(t)
		f.status = c.status
		code, _, errOut := f.run([]string{"deploy", dir}, token)
		if code != c.want || !strings.Contains(strings.ToLower(errOut), strings.ToLower(c.text)) {
			t.Errorf("%s: exit %d (want %d), err %q", c.what, code, c.want, errOut)
		}
		if strings.Contains(errOut, token) || strings.Contains(errOut, "secret-signature") {
			t.Errorf("%s: a secret is printed: %q", c.what, errOut)
		}
	}
	// No answer at all.
	f := newFake(t)
	f.server.Close()
	if code, _, errOut := f.run([]string{"deploy", dir}, token); code != exitServer || !strings.Contains(errOut, "cannot reach") {
		t.Errorf("no server: exit %d, err %q", code, errOut)
	}
}

func TestUsageMistakesAndTheTokenAreCheckedBeforeAnythingIsSent(t *testing.T) {
	f := newFake(t)
	dir := site(t, map[string]string{"index.html": "x"})
	if code, _, errOut := f.run([]string{"deploy", dir}, ""); code != exitAuth || !strings.Contains(errOut, "KOEDODB_TOKEN") {
		t.Errorf("no token: exit %d, err %q", code, errOut)
	}
	if code, _, _ := f.run([]string{"deploy", dir, dir}, token); code != exitUsage {
		t.Errorf("two directories: %d", code)
	}
	if code, _, _ := f.run([]string{"deploy", "--nope", dir}, token); code != exitUsage {
		t.Errorf("an unknown flag: %d", code)
	}
	if code, _, _ := f.run([]string{"deploy", dir, "--api", "http://api.example.com/v1"}, token); code != exitUsage {
		t.Errorf("an API address that is not https: %d", code)
	}
	if code, _, errOut := f.run([]string{"launch"}, token); code != exitUsage || !strings.Contains(errOut, "not a command") {
		t.Errorf("an unknown command: %d %q", code, errOut)
	}
	if code, out, _ := f.run([]string{"version"}, ""); code != 0 || !strings.HasPrefix(out, "koedodb ") {
		t.Errorf("version: %d %q", code, out)
	}
	if code, out, _ := f.run([]string{"help"}, ""); code != 0 || !strings.Contains(out, "Everything in the") {
		t.Errorf("help: %d", code)
	}
	if len(f.apiAuth) != 0 {
		t.Errorf("something was sent: %v", f.apiAuth)
	}
	// A project root is refused with the reason, and nothing is sent either.
	root := site(t, map[string]string{"index.html": "x", "package.json": "{}", ".env": "SECRET=1"})
	code, _, errOut := f.run([]string{"deploy", root}, token)
	if code != exitInput || !strings.Contains(errOut, "looks like a project") || len(f.apiAuth) != 0 || f.putBody != nil {
		t.Errorf("a project root: exit %d, err %q, sent %v", code, errOut, f.apiAuth)
	}
	_ = strconv.Itoa
}
