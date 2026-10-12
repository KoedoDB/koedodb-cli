// Package api is the little of KoedoDB's customer API that deploying a static site uses. The token is a deploy token (it can deploy the one Project it was made for, and nothing else). The bytes of
// the site do not go to the API: it hands back a presigned URL of the object store, and the archive is PUT there.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// Client talks to one API.
type Client struct {
	BaseURL   string // https://api.koedodb.com/v1
	Token     string
	UserAgent string
	HTTP      *http.Client
}

// Error is an answer that was not a success. Code is the API's own stable code when it gave one.
type Error struct {
	Status int
	Code   string
}

func (e *Error) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("KoedoDB answered %d (%s)", e.Status, e.Code)
	}
	return fmt.Sprintf("KoedoDB answered %d", e.Status)
}

// Network is a failure to reach a server (not an answer).
type Network struct{ What string }

func (n *Network) Error() string { return "cannot reach " + n.What }

// Upload is where to PUT the archive and the id to complete it with.
type Upload struct {
	ID        string    `json:"id"`
	Method    string    `json:"method"`
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Site is the site as KoedoDB has it.
type Site struct {
	Published bool      `json:"published"`
	Bytes     int64     `json:"bytes,omitempty"`
	SHA256    string    `json:"sha256,omitempty"`
	URL       string    `json:"url,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitzero"`
}

// client never follows a redirect. The API does not redirect, and a redirect would carry the token to wherever it points (the same domain's other hosts, or the same host in clear text after a
// downgrade to http): it is an error to be told, not a road to follow. The same for the object store.
func (c *Client) client() *http.Client {
	var client http.Client
	if c.HTTP != nil {
		client = *c.HTTP
	} else {
		client = http.Client{Timeout: 30 * time.Second, Transport: transport()}
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &client
}

// transport is the default one with the time to connect and to hear the first answer bounded (the body of an upload takes as long as the line does).
func transport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.TLSHandshakeTimeout = 15 * time.Second
	t.ResponseHeaderTimeout = 2 * time.Minute
	return t
}

// UploadTimeout is the most the upload of an archive may take (100 MiB at 0.5 Mbit/s is 27 minutes).
const UploadTimeout = 30 * time.Minute

var codePattern = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, reader)
	if err != nil {
		return errors.New("the API address is not valid")
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return &Network{What: hostOf(c.BaseURL)}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &failure)
		// The code is shown to the person: it is what the API's codes are made of, or it is dropped (a terminal obeys the escape sequences in what a server sends).
		if !codePattern.MatchString(failure.Error) {
			failure.Error = ""
		}
		if resp.StatusCode >= 300 && resp.StatusCode < 400 && failure.Error == "" {
			failure.Error = "unexpected_redirect"
		}
		return &Error{Status: resp.StatusCode, Code: failure.Error}
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return errors.New("KoedoDB's answer is not what this version expects; update the CLI")
		}
	}
	return nil
}

func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return "the API"
}

// RequestUpload asks where to put an archive with this hash and size.
func (c *Client) RequestUpload(ctx context.Context, sha256 string, size int64) (Upload, error) {
	var up Upload
	err := c.do(ctx, http.MethodPost, "/site/uploads", map[string]any{"sha256": sha256, "bytes": size}, &up)
	return up, err
}

// Put sends the archive to the presigned URL. The URL carries the right to write; the token is not sent there. The error never contains the URL (it is a credential).
func (c *Client) Put(ctx context.Context, up Upload, file string, size int64) error {
	u, err := url.Parse(up.URL)
	if err != nil || (u.Scheme != "https" && !(u.Scheme == "http" && isLoopback(u.Hostname()))) || u.Host == "" {
		return errors.New("KoedoDB gave an upload address that is not https")
	}
	in, err := os.Open(file)
	if err != nil {
		return err
	}
	defer in.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, up.URL, in)
	if err != nil {
		return errors.New("the upload address is not valid")
	}
	// The size is signed into the URL: the store refuses any other, and needs to be told it (not chunked).
	req.ContentLength = size
	req.Header.Set("Content-Type", "application/gzip")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	// An upload takes as long as the line does: no limit on the body, but the whole of it is bounded by UploadTimeout.
	ctx, cancel := context.WithTimeout(ctx, UploadTimeout)
	defer cancel()
	req = req.WithContext(ctx)
	client := *c.client()
	client.Timeout = 0
	resp, err := client.Do(req)
	if err != nil {
		return &Network{What: u.Host}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &Error{Status: resp.StatusCode, Code: "upload_refused"}
	}
	return nil
}

func isLoopback(host string) bool { return host == "127.0.0.1" || host == "localhost" || host == "::1" }

// Complete says the archive is in the store.
func (c *Client) Complete(ctx context.Context, id string) (Site, error) {
	var site Site
	err := c.do(ctx, http.MethodPost, "/site/uploads/"+url.PathEscape(id)+"/complete", nil, &site)
	return site, err
}

// Site asks what is deployed.
func (c *Client) Site(ctx context.Context) (Site, error) {
	var site Site
	err := c.do(ctx, http.MethodGet, "/site", nil, &site)
	return site, err
}
