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

// swapAfterScan makes the change between the scan and the reading, as another process could.
func swapAfterScan(t *testing.T, change func()) {
	t.Helper()
	afterScan = change
	t.Cleanup(func() { afterScan = nil })
}

func TestAFileSwappedForALinkAfterTheScanIsNotReadAndWhatItPointsAtIsNotPacked(t *testing.T) {
	// The file outside is exactly as long as the one it takes the place of: a difference of size would refuse it for that reason, and hide whether the link itself is.
	secret := "PRIVATE KEY OUTSIDE THE DIRECTORY"
	outside := filepath.Join(t.TempDir(), "id_rsa")
	if err := os.WriteFile(outside, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := site(t, map[string]string{"index.html": "hi", "assets/app.js": strings.Repeat("x", len(secret))})
	swapAfterScan(t, func() {
		p := filepath.Join(dir, "assets", "app.js")
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, p); err != nil {
			t.Skip("no symbolic links here")
		}
	})
	r, err := Pack(dir, Options{})
	var p *Problem
	if err == nil || !errors.As(err, &p) || !strings.Contains(err.Error(), "app.js") || r != nil {
		t.Fatalf("a link in the place of a file: %v %+v", err, r)
	}
	if matches, _ := filepath.Glob(filepath.Join(os.TempDir(), "koedodb-site-*.tar.gz")); len(matches) > 0 {
		for _, m := range matches {
			if data, _ := os.ReadFile(m); strings.Contains(string(data), "PRIVATE KEY") {
				t.Errorf("an archive was left with the outside file in it: %s", m)
			}
		}
	}
}

func TestADirectorySwappedForALinkAfterTheScanDoesNotLetTheFilesOutsideIn(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "app.js"), []byte("OUTSIDE"), 0o600); err != nil { // as long as "inside!" below
		t.Fatal(err)
	}
	dir := site(t, map[string]string{"index.html": "hi", "assets/app.js": "inside!"})
	swapAfterScan(t, func() {
		if err := os.RemoveAll(filepath.Join(dir, "assets")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(dir, "assets")); err != nil {
			t.Skip("no symbolic links here")
		}
	})
	if r, err := Pack(dir, Options{}); err == nil {
		os.Remove(r.Path)
		t.Fatal("a directory that became a link to the outside was packed")
	}
}

func TestAFileReplacedByAnotherAfterTheScanIsRefusedAndAnUnchangedOneIsNot(t *testing.T) {
	dir := site(t, map[string]string{"index.html": "hi", "other.txt": "somebody else's file"})
	swapAfterScan(t, func() {
		// Another plain file renamed over the name: not the file that was checked (the size is the same too).
		replacement := filepath.Join(dir, "replacement")
		if err := os.WriteFile(replacement, []byte("hi"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(replacement, filepath.Join(dir, "index.html")); err != nil {
			t.Fatal(err)
		}
	})
	if r, err := Pack(dir, Options{}); err == nil || !strings.Contains(err.Error(), "changed while it was being packed") {
		if r != nil {
			os.Remove(r.Path)
		}
		t.Fatalf("a file replaced after the scan: %v", err)
	}
	// With nothing changed, the same directory packs.
	afterScan = nil
	r, err := Pack(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(r.Path)
}

func TestADirectoryGivenAsALinkIsFollowedOnceAndItsFilesAreStillReadThroughTheRoot(t *testing.T) {
	real := site(t, map[string]string{"index.html": "hi", "a/b.txt": "b"})
	link := filepath.Join(t.TempDir(), "dist")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("no symbolic links here")
	}
	r, err := Pack(link, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, r); got["index.html"] != "hi" || got["a/b.txt"] != "b" || len(got) != 2 {
		t.Errorf("files: %v", got)
	}
}

func TestTheNamesOfSecretsAreRefusedWhateverTheirCaseAndAnywhereInTheDirectory(t *testing.T) {
	for what, files := range map[string]map[string]string{
		"an upper-case .ENV":           {"index.html": "x", ".ENV": "S=1"},
		"a mixed-case .Env.Production": {"index.html": "x", "api/.Env.Production": "S=1"},
		"an upper-case PACKAGE.JSON":   {"index.html": "x", "PACKAGE.JSON": "{}"},
		"an .npmrc":                    {"index.html": "x", ".npmrc": "//registry.npmjs.org/:_authToken=abc"},
		"a .NETRC":                     {"index.html": "x", "deep/.NETRC": "machine x login y password z"},
		"an .aws directory":            {"index.html": "x", ".aws/credentials": "[default]"},
		"a .ssh directory":             {"index.html": "x", "backup/.ssh/config": "Host x"},
		"an id_rsa":                    {"index.html": "x", "keys/id_rsa": "-----BEGIN"},
		"an id_ed25519":                {"index.html": "x", "ID_ED25519": "-----BEGIN"},
		"a .htpasswd":                  {"index.html": "x", ".htpasswd": "user:hash"},
	} {
		dir := site(t, files)
		r, err := Pack(dir, Options{})
		var p *Problem
		if err == nil || !errors.As(err, &p) {
			if r != nil {
				os.Remove(r.Path)
			}
			t.Errorf("%s was packed", what)
			continue
		}
		if !strings.Contains(err.Error(), "--force") {
			t.Errorf("%s: the message does not say how to override: %v", what, err)
		}
		// --force is for the person who means it.
		if forced, err := Pack(dir, Options{Force: true}); err != nil {
			t.Errorf("%s with --force: %v", what, err)
		} else {
			os.Remove(forced.Path)
		}
	}
	// A name that only looks like one is kept.
	dir := site(t, map[string]string{"index.html": "x", "assets/ssh-guide.html": "y", "docs/aws-pricing.txt": "z", "id_rsa_explained.html": "w", ".envelope.txt": "v"})
	r, err := Pack(dir, Options{})
	if err != nil {
		t.Fatalf("a name that only looks like a secret: %v", err)
	}
	os.Remove(r.Path)
}
