// Package pack turns a build output directory into the archive KoedoDB serves as a static site: a tar.gz of the files, the same bytes for the same files. It refuses what a site must not
// contain, early, before anything is uploaded, so that the mistake a person (or an AI tool) is most likely to make, deploying the whole project instead of its build output, costs nothing.
package pack

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The limits of the platform (the server and the Host check them again; these only say it sooner).
const (
	MaxArchiveBytes  = 100 << 20
	MaxUnpackedBytes = 500 << 20
	MaxFileBytes     = 200 << 20
	MaxEntries       = 20000
	MaxPathBytes     = 1024
	MaxDepth         = 32
)

// Options are the things a person may override.
type Options struct {
	// Force allows a directory that looks like a project (not a build output) and a file named .env*. It is for the rare site that really has them.
	Force bool
	// AllowNoIndex allows a directory without index.html at its root.
	AllowNoIndex bool
}

// Result is the archive that was made. The file is the caller's to remove.
type Result struct {
	Path     string
	SHA256   string
	Bytes    int64
	Files    int
	Unpacked int64
}

// Problem is an error the person can fix; its text says how.
type Problem struct{ Msg string }

func (p *Problem) Error() string { return p.Msg }

func problem(format string, args ...any) error { return &Problem{Msg: fmt.Sprintf(format, args...)} }

// projectMarkers are what a project's root has and a build output does not.
var projectMarkers = []string{".git", "node_modules", "package.json", "go.mod", "pyproject.toml", "requirements.txt", "Cargo.toml", "Dockerfile"}

// File systems of macOS and Windows do not tell .ENV from .env: neither do we.
func isEnvFile(name string) bool {
	name = strings.ToLower(name)
	return name == ".env" || strings.HasPrefix(name, ".env.")
}

// credentials are the files and directories that keep a person's logins, which a build output has no use for and the internet must not see: the tokens of a package registry, a network login,
// the keys of ssh and of the cloud. Anywhere in the directory, by name.
var credentials = map[string]bool{
	".npmrc": true, ".netrc": true, ".pypirc": true, ".htpasswd": true, ".aws": true, ".ssh": true, ".gnupg": true, ".docker": true,
	"id_rsa": true, "id_dsa": true, "id_ecdsa": true, "id_ed25519": true,
}

// Pack makes the archive of dir in a temporary file.
func Pack(dir string, opts Options) (*Result, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, problem("%s does not exist. Build the site first, then deploy its output directory (dist, build or out).", dir)
	}
	if !info.IsDir() {
		return nil, problem("%s is not a directory. Deploy the directory the build writes the site to (dist, build or out).", dir)
	}
	// The directory itself may be a link the person gave on purpose (dist -> build); it is followed once, here. Everything below it is read through this root and never leaves it.
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, problem("cannot read %s: %v", dir, err)
	}
	if !opts.Force {
		// By name, the way the file system sees it: Package.json is a package.json on a Mac or on Windows.
		entries, err := os.ReadDir(root)
		if err != nil {
			return nil, problem("cannot read %s: %v", dir, err)
		}
		for _, entry := range entries {
			for _, marker := range projectMarkers {
				if strings.EqualFold(entry.Name(), marker) {
					return nil, problem("%s has %s: it looks like a project, not a build output. Deploy the directory the build writes to (dist, build or out). Everything in it is published on the internet. --force deploys it anyway.", dir, entry.Name())
				}
			}
		}
	}
	files, unpacked, err := collect(root, opts)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, problem("%s has no files.", dir)
	}
	if !opts.AllowNoIndex {
		found := false
		for _, f := range files {
			if f.rel == "index.html" {
				found = true
				break
			}
		}
		if !found {
			return nil, problem("%s has no index.html at its top. Did the build run? --allow-no-index deploys it anyway.", dir)
		}
	}
	if afterScan != nil {
		afterScan() // a test changes the directory here: between the check and the use
	}
	return write(root, files, unpacked)
}

// afterScan is for the tests: the moment between the scan (a file is a plain file) and the reading of it.
var afterScan func()

type file struct {
	rel, abs string
	size     int64
	// info is what the scan saw (an lstat, not following a link): the file that is read must be this one.
	info fs.FileInfo
}

func collect(dir string, opts Options) ([]file, int64, error) {
	var files []file
	var unpacked int64
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return problem("cannot read %s: %v", p, err)
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		name := d.Name()
		switch {
		case d.IsDir() && strings.EqualFold(name, ".git"):
			return filepath.SkipDir
		case credentials[strings.ToLower(name)] && !opts.Force:
			return problem("%s is a file of logins or keys. Everything in the site is published on the internet. Take it out (--force keeps it).", rel)
		case d.IsDir() && strings.EqualFold(name, "node_modules"):
			return problem("%s is in the site. node_modules is not part of a build output.", rel)
		case name == ".DS_Store":
			return nil
		case d.Type()&fs.ModeSymlink != 0:
			return problem("%s is a symbolic link. A site is plain files; copy the file in.", rel)
		case d.IsDir():
			if depth(rel) > MaxDepth {
				return problem("%s is nested more than %d directories deep.", rel, MaxDepth)
			}
			return nil
		case !d.Type().IsRegular():
			return problem("%s is not a plain file.", rel)
		case isEnvFile(name) && !opts.Force:
			return problem("%s is an environment file. Everything in the site is published on the internet. Take it out (--force keeps it).", rel)
		}
		if len(rel) > MaxPathBytes || strings.ContainsAny(rel, "\\\x00") {
			return problem("the name of %s is not one a site may have.", rel)
		}
		info, err := d.Info()
		if err != nil {
			return problem("cannot read %s: %v", rel, err)
		}
		if info.Size() > MaxFileBytes {
			return problem("%s is larger than %d MiB.", rel, MaxFileBytes>>20)
		}
		if unpacked += info.Size(); unpacked > MaxUnpackedBytes {
			return problem("the files are larger than %d MiB together.", MaxUnpackedBytes>>20)
		}
		if len(files) >= MaxEntries {
			return problem("the site has more than %d files.", MaxEntries)
		}
		files = append(files, file{rel: rel, abs: p, size: info.Size(), info: info})
		return nil
	})
	if err != nil {
		var p *Problem
		if errors.As(err, &p) {
			return nil, 0, err
		}
		return nil, 0, problem("cannot read the directory: %v", err)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].rel < files[j].rel })
	return files, unpacked, nil
}

func depth(rel string) int { return strings.Count(path.Clean(rel), "/") + 1 }

// write makes the tar.gz: files only (the directories are implied), in order, with no owner, no time and fixed modes, so the same files always make the same bytes.
func write(rootDir string, files []file, unpacked int64) (*Result, error) {
	// Files are opened through the root: the system refuses a path that leaves the directory (a ".." or a link that points out, in the name or in a directory of it), whatever was changed since the scan.
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		return nil, problem("cannot read %s: %v", rootDir, err)
	}
	defer root.Close()
	out, err := os.CreateTemp("", "koedodb-site-*.tar.gz")
	if err != nil {
		return nil, fmt.Errorf("cannot make a temporary file: %w", err)
	}
	ok := false
	defer func() {
		out.Close()
		if !ok {
			os.Remove(out.Name())
		}
	}()
	hash := sha256.New()
	counted := &countWriter{w: io.MultiWriter(out, hash)}
	gz, err := gzip.NewWriterLevel(counted, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	tw := tar.NewWriter(gz)
	for _, f := range files {
		in, err := root.Open(filepath.FromSlash(f.rel))
		if err != nil {
			return nil, problem("cannot read %s: %v. It changed while it was being packed (a link in its place?).", f.rel, err)
		}
		// What was opened is checked, not the name: a plain file, of the size the scan saw, and the very file the scan saw (not another that took its name, and not what a link points to).
		if st, err := in.Stat(); err != nil || !st.Mode().IsRegular() || st.Size() != f.size || !os.SameFile(f.info, st) {
			in.Close()
			return nil, problem("%s changed while it was being packed (it is not the file that was checked). Run it again.", f.rel)
		}
		hdr := &tar.Header{Name: f.rel, Typeflag: tar.TypeReg, Mode: 0o644, Size: f.size, ModTime: time.Unix(0, 0)}
		if err := tw.WriteHeader(hdr); err != nil {
			in.Close()
			return nil, err
		}
		n, err := io.Copy(tw, in)
		in.Close()
		if err != nil || n != f.size {
			return nil, problem("%s changed while it was being packed.", f.rel)
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	if counted.n > MaxArchiveBytes {
		return nil, problem("the archive is %d MiB, more than the %d MiB a site may be.", counted.n>>20, MaxArchiveBytes>>20)
	}
	ok = true
	return &Result{Path: out.Name(), SHA256: hex.EncodeToString(hash.Sum(nil)), Bytes: counted.n, Files: len(files), Unpacked: unpacked}, nil
}

type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}
