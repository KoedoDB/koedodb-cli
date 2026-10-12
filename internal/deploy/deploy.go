// Package deploy is `koedodb deploy`: pack a build output, ask KoedoDB where to put it, put it, and say it is done.
package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/koedodb/koedodb-cli/internal/api"
	"github.com/koedodb/koedodb-cli/internal/pack"
)

// Options are what a deploy needs.
type Options struct {
	Dir          string
	Force        bool
	AllowNoIndex bool
	// Progress gets a line for each step (nil: none).
	Progress io.Writer
}

// Result is what a deploy reports.
type Result struct {
	Files      int       `json:"files"`
	Bytes      int64     `json:"bytes"`
	Unpacked   int64     `json:"unpacked_bytes"`
	SHA256     string    `json:"sha256"`
	URL        string    `json:"url,omitempty"`
	DeployedAt time.Time `json:"deployed_at,omitzero"`
}

func say(w io.Writer, format string, args ...any) {
	if w != nil {
		fmt.Fprintf(w, format+"\n", args...)
	}
}

// Run does the deploy. The errors it returns are *pack.Problem (fix the directory), *api.Error (KoedoDB said no), *api.Network (no connection) or a plain error.
func Run(ctx context.Context, client *api.Client, opts Options) (*Result, error) {
	say(opts.Progress, "Packing %s", opts.Dir)
	archive, err := pack.Pack(opts.Dir, pack.Options{Force: opts.Force, AllowNoIndex: opts.AllowNoIndex})
	if err != nil {
		return nil, err
	}
	defer os.Remove(archive.Path)
	say(opts.Progress, "  %d files, %s (%s packed)", archive.Files, size(archive.Unpacked), size(archive.Bytes))

	up, err := client.RequestUpload(ctx, archive.SHA256, archive.Bytes)
	if err != nil {
		return nil, err
	}
	say(opts.Progress, "Uploading")
	if err := client.Put(ctx, up, archive.Path, archive.Bytes); err != nil {
		return nil, err
	}
	site, err := client.Complete(ctx, up.ID)
	if err != nil {
		return nil, err
	}
	if !site.Published || site.SHA256 != archive.SHA256 {
		return nil, errors.New("KoedoDB did not record the site that was uploaded; try again")
	}
	say(opts.Progress, "Deployed")
	return &Result{Files: archive.Files, Bytes: archive.Bytes, Unpacked: archive.Unpacked, SHA256: archive.SHA256, URL: site.URL, DeployedAt: site.UpdatedAt}, nil
}

func size(n int64) string {
	switch {
	case n >= 10<<20:
		return fmt.Sprintf("%.0f MiB", float64(n)/(1<<20))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
