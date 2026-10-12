package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A redirect is never followed: the token would go where it points. Two servers on two ports of one host: the second must hear nothing.
func TestARedirectIsAnErrorAndTheTokenGoesNowhere(t *testing.T) {
	var elsewhere []string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere = append(elsewhere, r.Method+" "+r.Header.Get("Authorization"))
	}))
	defer target.Close()
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/v1/site", http.StatusTemporaryRedirect) // 307: keeps the method and the body
	}))
	defer front.Close()
	c := &Client{BaseURL: front.URL + "/v1", Token: "kdb_secret_token"}
	_, err := c.RequestUpload(context.Background(), strings.Repeat("a", 64), 10)
	apiErr, ok := err.(*Error)
	if !ok || apiErr.Status != http.StatusTemporaryRedirect || apiErr.Code != "unexpected_redirect" {
		t.Fatalf("the error: %#v", err)
	}
	if len(elsewhere) != 0 {
		t.Errorf("the redirect was followed: %v", elsewhere)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Errorf("the error has the token: %v", err)
	}
	// An injected client (the tests of the program use one) is held to the same rule.
	c.HTTP = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return nil }}
	if _, err := c.Site(context.Background()); err == nil || len(elsewhere) != 0 {
		t.Errorf("an injected client followed it: %v %v", err, elsewhere)
	}
}

func TestTheUploadIsNotRedirectedEitherAndCarriesNoToken(t *testing.T) {
	var elsewhere int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { elsewhere++ }))
	defer target.Close()
	store := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("the token was sent to the store")
		}
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer store.Close()
	file := filepath.Join(t.TempDir(), "a.tar.gz")
	if err := os.WriteFile(file, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &Client{BaseURL: "https://api.example.test/v1", Token: "kdb_secret_token"}
	err := c.Put(context.Background(), Upload{URL: store.URL + "/x?X-Amz-Signature=signature-secret"}, file, 10)
	if apiErr, ok := err.(*Error); !ok || apiErr.Code != "upload_refused" || apiErr.Status != http.StatusTemporaryRedirect {
		t.Fatalf("the error: %#v", err)
	}
	if elsewhere != 0 {
		t.Errorf("the upload was sent where the redirect pointed")
	}
	if strings.Contains(err.Error(), "signature-secret") {
		t.Errorf("the error has the address: %v", err)
	}
}

// What the server says is shown to the person: a code is a code, or it is nothing (a terminal obeys the escape sequences in what it is shown).
func TestACodeThatIsNotACodeIsDropped(t *testing.T) {
	answer := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(answer))
	}))
	defer server.Close()
	c := &Client{BaseURL: server.URL + "/v1", Token: "kdb_x"}
	for body, want := range map[string]string{
		`{"error":"project_not_active"}`:              "project_not_active",
		`{"error":"\u001b[2Jhacked"}`:                 "",
		`{"error":"Has Spaces"}`:                      "",
		`{"error":"` + strings.Repeat("a", 65) + `"}`: "",
		`{"error":""}`:                                "",
		`not json at all`:                             "",
		`{"error":"archive_size_mismatch\n"}`:         "",
	} {
		answer = body
		_, err := c.Site(context.Background())
		apiErr, ok := err.(*Error)
		if !ok || apiErr.Code != want {
			t.Errorf("%q: %#v", body, err)
		}
	}
}

func TestOnlyAnHTTPSAddressOrALoopbackOneIsAnUploadAddress(t *testing.T) {
	c := &Client{}
	file := filepath.Join(t.TempDir(), "a")
	_ = os.WriteFile(file, []byte("x"), 0o600)
	for _, bad := range []string{"http://store.example/x", "ftp://store.example/x", "file:///etc/passwd", "//store.example/x", "", "https:///x"} {
		if err := c.Put(context.Background(), Upload{URL: bad}, file, 1); err == nil || strings.Contains(err.Error(), bad) && bad != "" && len(bad) > 12 {
			t.Errorf("%q: %v", bad, err)
		}
	}
}
