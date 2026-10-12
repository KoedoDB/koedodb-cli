package pack

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func site(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func read(t *testing.T, r *Result) map[string]string {
	t.Helper()
	defer os.Remove(r.Path)
	f, err := os.Open(r.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Typeflag != tar.TypeReg || hdr.Mode != 0o644 || hdr.Uid != 0 || hdr.Gid != 0 || hdr.Uname != "" || !hdr.ModTime.Equal(time.Unix(0, 0)) {
			t.Errorf("%s: type %v mode %o uid %d gid %d user %q time %v", hdr.Name, hdr.Typeflag, hdr.Mode, hdr.Uid, hdr.Gid, hdr.Uname, hdr.ModTime)
		}
		body, _ := io.ReadAll(tr)
		got[hdr.Name] = string(body)
	}
	return got
}

func TestPackMakesTheArchiveOfTheFilesAndNothingElse(t *testing.T) {
	dir := site(t, map[string]string{"index.html": "<h1>hi</h1>", "assets/app.js": "x", "assets/css/site.css": "y", ".DS_Store": "junk", ".well-known/security.txt": "z"})
	r, err := Pack(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Files != 4 || r.Bytes <= 0 || len(r.SHA256) != 64 {
		t.Errorf("result: %+v", r)
	}
	got := read(t, r)
	want := map[string]string{"index.html": "<h1>hi</h1>", "assets/app.js": "x", "assets/css/site.css": "y", ".well-known/security.txt": "z"}
	if len(got) != len(want) {
		t.Fatalf("files: %v", got)
	}
	for name, body := range want {
		if got[name] != body {
			t.Errorf("%s = %q", name, got[name])
		}
	}
}

func TestPackIsTheSameBytesForTheSameFilesWhateverTheirTimes(t *testing.T) {
	files := map[string]string{"index.html": "a", "b/c.txt": "c", "b/a.txt": "d"}
	one, two := site(t, files), site(t, files)
	old := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(two, "index.html"), old, old); err != nil {
		t.Fatal(err)
	}
	a, err := Pack(one, Options{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Pack(two, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(a.Path)
	defer os.Remove(b.Path)
	if a.SHA256 != b.SHA256 || a.Bytes != b.Bytes {
		t.Errorf("the same files made different archives: %s %s", a.SHA256, b.SHA256)
	}
	changed := site(t, map[string]string{"index.html": "A", "b/c.txt": "c", "b/a.txt": "d"})
	c, err := Pack(changed, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(c.Path)
	if c.SHA256 == a.SHA256 {
		t.Error("different files made the same archive")
	}
}

func TestPackRefusesWhatASiteMustNotContainAndSaysWhatToDo(t *testing.T) {
	cases := map[string]struct {
		files map[string]string
		want  string
	}{
		"a project root (package.json)": {map[string]string{"index.html": "x", "package.json": "{}"}, "looks like a project"},
		"a project root (.git)":         {map[string]string{"index.html": "x", ".git/HEAD": "ref"}, "looks like a project"},
		"a project root (node_modules)": {map[string]string{"index.html": "x", "node_modules/a/b.js": "x"}, "looks like a project"},
		"a Dockerfile":                  {map[string]string{"index.html": "x", "Dockerfile": "FROM x"}, "looks like a project"},
		"an env file":                   {map[string]string{"index.html": "x", ".env": "SECRET=1"}, "environment file"},
		"an env file in a directory":    {map[string]string{"index.html": "x", "api/.env.production": "SECRET=1"}, "environment file"},
		"node_modules below":            {map[string]string{"index.html": "x", "vendor/node_modules/a.js": "x"}, "node_modules"},
		"no index.html":                 {map[string]string{"main.html": "x"}, "no index.html"},
		"no files":                      {map[string]string{}, "no files"},
	}
	for what, c := range cases {
		dir := site(t, c.files)
		r, err := Pack(dir, Options{})
		var p *Problem
		if err == nil || !errors.As(err, &p) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", what, err)
		}
		if r != nil {
			os.Remove(r.Path)
		}
	}
	if _, err := Pack(filepath.Join(t.TempDir(), "nope"), Options{}); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("a missing directory: %v", err)
	}
	file := filepath.Join(t.TempDir(), "f")
	_ = os.WriteFile(file, []byte("x"), 0o600)
	if _, err := Pack(file, Options{}); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("a file: %v", err)
	}
}

func TestPackRefusesALinkAndTooDeepAndTooManyFiles(t *testing.T) {
	dir := site(t, map[string]string{"index.html": "x", "real.txt": "y"})
	if err := os.Symlink(filepath.Join(dir, "real.txt"), filepath.Join(dir, "link.txt")); err != nil {
		t.Skip("no symbolic links here")
	}
	if _, err := Pack(dir, Options{}); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Errorf("a link: %v", err)
	}
	deep := site(t, map[string]string{"index.html": "x", strings.Repeat("d/", MaxDepth+1) + "f.txt": "y"})
	if _, err := Pack(deep, Options{}); err == nil || !strings.Contains(err.Error(), "deep") {
		t.Errorf("too deep: %v", err)
	}
	many := map[string]string{"index.html": "x"}
	for i := 0; i < MaxEntries; i++ {
		many[fmt.Sprintf("f/%05d.txt", i)] = ""
	}
	if _, err := Pack(site(t, many), Options{}); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Errorf("too many: %v", err)
	}
}

func TestForceAndAllowNoIndexLetThroughWhatTheyAreFor(t *testing.T) {
	dir := site(t, map[string]string{"index.html": "x", "package.json": "{}", ".env": "S=1", ".git/HEAD": "ref"})
	r, err := Pack(dir, Options{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	got := read(t, r)
	if _, ok := got[".git/HEAD"]; ok {
		t.Error(".git was packed")
	}
	if got["package.json"] == "" || got[".env"] == "" {
		t.Errorf("--force must keep the files it was given for: %v", got)
	}
	r, err = Pack(site(t, map[string]string{"main.html": "x"}), Options{AllowNoIndex: true})
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(r.Path)
}
