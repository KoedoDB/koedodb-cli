package main

// The `branch` commands: an empty copy of a Project's database, made for a pull request or a try and deleted when it is done. They use the customer API with the access token of a person's
// login (KOEDODB_ACCESS_TOKEN), not the deploy token: a deploy token can deploy one site and nothing else.

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/koedodb/koedodb-cli/internal/api"
)

const (
	accessTokenVarName = "KOEDODB_ACCESS_TOKEN"
	organizationVar    = "KOEDODB_ORGANIZATION"
	projectVar         = "KOEDODB_PROJECT"
)

const branchUsage = `koedodb branch makes and deletes branches of a project: an empty database of its own, for a pull
request or a try.

Usage:
  koedodb branch create <name> [--days N] [--json] [--organization ID] [--project ID] [--api URL]
  koedodb branch list           [--json] [--organization ID] [--project ID] [--api URL]
  koedodb branch get <name|id>  [--json] [--organization ID] [--project ID] [--api URL]
  koedodb branch delete <name|id> --yes [--json] [--organization ID] [--project ID] [--api URL]

A branch name is lower-case letters, digits and hyphens (40 characters at most; not "main"). create
prints where the branch's database answers and the password of its user: the password is shown
once, so keep it (--json has it in the "password" field). --days N makes the branch go away by
itself after N days; without it, it stays until you delete it. The main branch is the project's own
database: it cannot be deleted here.

delete removes the branch and its database at once, and never asks: it needs --yes.

These commands use the access token of your KoedoDB login, in the environment variable
` + accessTokenVarName + ` (not the deploy token of "koedodb deploy"). The project is named by the ids of its
organization and of itself: --organization (or ` + organizationVar + `) and --project (or ` + projectVar + `).

Exit status: 0 done, 2 used wrongly, 3 no token or not accepted, 4 KoedoDB refused the request (the
message says why: a name in use, the limit of branches, main), 5 KoedoDB or the network did not
answer as it should (try again), 1 anything else.
`

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func runBranch(ctx context.Context, args []string, env func(string) string, stdout, stderr io.Writer, httpClient *http.Client) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprint(stdout, branchUsage)
		if len(args) == 0 {
			return exitUsage
		}
		return exitOK
	}
	sub := args[0]
	if sub != "create" && sub != "list" && sub != "get" && sub != "delete" {
		fmt.Fprintf(stderr, "koedodb branch: %q is not a branch command. See `koedodb branch help`.\n", sub)
		return exitUsage
	}
	fs := flag.NewFlagSet("branch "+sub, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var asJSON, yes bool
	var apiURL, organization, project string
	days := 0
	fs.BoolVar(&asJSON, "json", false, "")
	fs.StringVar(&apiURL, "api", "", "")
	fs.StringVar(&organization, "organization", "", "")
	fs.StringVar(&project, "project", "", "")
	if sub == "create" {
		fs.IntVar(&days, "days", 0, "")
	}
	if sub == "delete" {
		fs.BoolVar(&yes, "yes", false, "")
	}
	// Flags may come after the name (`koedodb branch create pr-1 --days 3`): the standard package stops at the first argument that is not a flag.
	var names []string
	rest := args[1:]
	for {
		if err := fs.Parse(rest); err != nil {
			fmt.Fprintf(stderr, "koedodb branch %s: %v. See `koedodb branch help`.\n", sub, err)
			return exitUsage
		}
		if fs.NArg() == 0 {
			break
		}
		names = append(names, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	fail := func(code int, kind, message string) int {
		if asJSON {
			_ = json.NewEncoder(stdout).Encode(map[string]any{"error": map[string]string{"code": kind, "message": message}})
		} else {
			fmt.Fprintf(stderr, "koedodb: %s\n", message)
		}
		return code
	}
	want := 1
	if sub == "list" {
		want = 0
	}
	if len(names) != want {
		if want == 0 {
			return fail(exitUsage, "usage", "list takes no name.")
		}
		return fail(exitUsage, "usage", "give one branch: koedodb branch "+sub+" <name>.")
	}
	if sub == "create" && (days < 0 || days > 3650) {
		return fail(exitUsage, "usage", "--days is a number of days from 0 (no end) to 3650.")
	}
	if sub == "delete" && !yes {
		return fail(exitUsage, "usage", "this deletes the branch and its database, and cannot be undone. It never asks: run it again with --yes.")
	}
	if organization == "" {
		organization = strings.TrimSpace(env(organizationVar))
	}
	if project == "" {
		project = strings.TrimSpace(env(projectVar))
	}
	if !uuidPattern.MatchString(organization) || !uuidPattern.MatchString(project) {
		return fail(exitUsage, "usage", "name the project: --organization and --project (or "+organizationVar+" and "+projectVar+") are ids like 3f2c9a52-0d7e-4b1f-9a64-5c2e8d1b7f30. They are in the address of the project in the console.")
	}
	token := strings.TrimSpace(env(accessTokenVarName))
	if token == "" {
		return fail(exitAuth, "no_token", accessTokenVarName+" is not set. It is the access token of your KoedoDB login (not a deploy token).")
	}
	if !validAccessToken(token) {
		// Not echoed: a line break in it would make a header of the request.
		return fail(exitAuth, "invalid_token", accessTokenVarName+" has a character a token does not have (a space or a line break inside, or something that is not plain text). Copy the token again.")
	}
	apiURL, u, loopback, err := checkAPI(apiURL, env)
	if err != nil {
		return fail(exitUsage, "usage", err.Error())
	}
	if !loopback && !trustedHost(u.Hostname()) && !asJSON {
		fmt.Fprintf(stderr, "koedodb: the token is sent to %s, which is not a koedodb.com address. If you did not set %s or --api yourself, stop and check.\n", u.Host, "KOEDODB_API_URL")
	}
	client := &api.Client{BaseURL: apiURL, Token: token, UserAgent: "koedodb-cli/" + versionString(), HTTP: httpClient}
	scope := api.Scope{Organization: organization, Project: project}
	failErr := func(err error) int {
		code, kind, message := classifyBranch(err)
		return fail(code, kind, message)
	}
	switch sub {
	case "create":
		created, err := client.CreateBranch(ctx, scope, names[0], days)
		if err != nil {
			return failErr(err)
		}
		if asJSON {
			_ = json.NewEncoder(stdout).Encode(created)
			return exitOK
		}
		printCreated(stdout, stderr, created)
	case "list":
		branches, err := client.ListBranches(ctx, scope)
		if err != nil {
			return failErr(err)
		}
		if asJSON {
			_ = json.NewEncoder(stdout).Encode(map[string]any{"branches": nonNil(branches)})
			return exitOK
		}
		printBranches(stdout, branches)
	case "get":
		b, err := resolveBranch(ctx, client, scope, names[0])
		if err != nil {
			return failErr(err)
		}
		if asJSON {
			_ = json.NewEncoder(stdout).Encode(b)
			return exitOK
		}
		printBranch(stdout, b)
	case "delete":
		b, err := resolveBranch(ctx, client, scope, names[0])
		if err != nil {
			return failErr(err)
		}
		if err := client.DeleteBranch(ctx, scope, b.ID); err != nil {
			return failErr(err)
		}
		if asJSON {
			_ = json.NewEncoder(stdout).Encode(map[string]any{"deleted": true, "id": b.ID, "name": b.Name})
			return exitOK
		}
		fmt.Fprintf(stdout, "deleted %s\n", safeText(b.Name))
	}
	return exitOK
}

func nonNil(b []api.Branch) []api.Branch {
	if b == nil {
		return []api.Branch{}
	}
	return b
}

// notFoundBranch is what resolveBranch says when there is no such branch (the same as the API's 404).
var notFoundBranch = &api.Error{Status: http.StatusNotFound, Code: "branch_not_found"}

// resolveBranch finds a branch of the Project by its name, or by its id.
func resolveBranch(ctx context.Context, c *api.Client, s api.Scope, ref string) (api.Branch, error) {
	branches, err := c.ListBranches(ctx, s)
	if err != nil {
		return api.Branch{}, err
	}
	for _, b := range branches {
		if b.Name == ref {
			return b, nil
		}
	}
	if uuidPattern.MatchString(ref) {
		for _, b := range branches {
			if strings.EqualFold(b.ID, ref) {
				return b, nil
			}
		}
	}
	return api.Branch{}, notFoundBranch
}

// classifyBranch turns an error into an exit status, a stable kind and a message that says what to do.
func classifyBranch(err error) (int, string, string) {
	var apiErr *api.Error
	var network *api.Network
	switch {
	case errors.As(err, &network):
		return exitServer, "network", err.Error() + ". Check the connection and run it again."
	case errors.As(err, &apiErr):
		switch apiErr.Status {
		case http.StatusUnauthorized:
			return exitAuth, "token_not_accepted", "the access token was not accepted. It may be wrong or expired: get a new one."
		case http.StatusForbidden:
			if apiErr.Code == "branch_limit_reached" {
				return exitInput, "branch_limit_reached", "this project already has the most branches the beta allows (2 besides main). Delete one first: koedodb branch delete <name> --yes."
			}
			return exitAuth, "forbidden", "you cannot do this in this project: making and deleting branches needs the owner or an admin of the project."
		case http.StatusNotFound:
			if apiErr.Code == "branch_not_found" {
				return exitInput, "branch_not_found", "there is no such branch in this project. See: koedodb branch list."
			}
			return exitInput, "project_not_found", "KoedoDB does not know this project (or you are not in it). Check --organization and --project."
		case http.StatusBadRequest:
			if apiErr.Code == "invalid_expiry" {
				return exitInput, "invalid_expiry", "the number of days is not accepted (0 to 3650)."
			}
			return exitInput, "invalid_branch", "the branch name is not accepted: lower-case letters, digits and hyphens, 40 characters at most, and not \"main\"."
		case http.StatusConflict:
			switch apiErr.Code {
			case "main_branch_cannot_be_deleted":
				return exitInput, "main_branch_cannot_be_deleted", "main is the project's own database: it cannot be deleted as a branch. Delete the project in the console to remove it."
			case "branch_name_in_use":
				return exitInput, "branch_name_in_use", "a branch with this name exists already. Choose another name, or delete that one first."
			case "project_not_ready":
				return exitServer, "project_not_ready", "the project is not active yet (still being made, or unavailable). Try again in a minute."
			}
		case http.StatusServiceUnavailable:
			if apiErr.Code == "no_capacity" {
				return exitServer, "no_capacity", "the host has no room for another database right now. Nothing was queued and nothing was made: try again later."
			}
		case http.StatusTooManyRequests:
			return exitServer, "rate_limited", "too many requests. Wait a little and run it again."
		}
		if apiErr.Status >= 500 {
			return exitServer, "server_error", "KoedoDB had a problem. Run it again in a minute."
		}
		return exitFailed, "api_error", err.Error()
	}
	return exitFailed, "error", err.Error()
}

// validAccessToken is plain visible ASCII with no space, at most 4096 characters (a login's token is longer than a deploy token). A line break in it would make a header of the request.
func validAccessToken(token string) bool {
	if len(token) == 0 || len(token) > 4096 {
		return false
	}
	for i := 0; i < len(token); i++ {
		if token[i] < 0x21 || token[i] > 0x7e {
			return false
		}
	}
	return true
}

// safeText is a word from the server as it is shown on a terminal: a terminal obeys the escape sequences in what it is shown, so anything but plain printable text becomes "?" (and it is cut).
func safeText(s string) string {
	if len(s) > 200 {
		s = s[:200]
	}
	out := []rune(s)
	for i, r := range out {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			out[i] = '?'
		}
	}
	return string(out)
}

func expiryText(t *time.Time) string {
	if t == nil || t.IsZero() {
		return "-"
	}
	return t.UTC().Format("2006-01-02 15:04 UTC")
}

// connectionURL is the address of the database with no password in it (the password is given apart: a program or a shell history may keep an address).
func connectionURL(c api.Connection) string {
	u := url.URL{Scheme: "postgresql", User: url.User(c.User), Host: c.Host + ":" + strconv.Itoa(c.Port), Path: "/" + c.Database}
	if c.SSLMode != "" {
		u.RawQuery = "sslmode=" + url.QueryEscape(c.SSLMode)
	}
	return u.String()
}

func printBranches(w io.Writer, branches []api.Branch) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tKIND\tSTATUS\tEXPIRES\tID")
	for _, b := range branches {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", safeText(b.Name), safeText(b.Kind), safeText(b.Status), expiryText(b.ExpiresAt), safeText(b.ID))
	}
	_ = tw.Flush()
}

func printBranch(w io.Writer, b api.Branch) {
	fmt.Fprintf(w, "name      %s\n", safeText(b.Name))
	fmt.Fprintf(w, "kind      %s\n", safeText(b.Kind))
	fmt.Fprintf(w, "status    %s\n", safeText(b.Status))
	fmt.Fprintf(w, "created   %s\n", b.CreatedAt.UTC().Format("2006-01-02 15:04 UTC"))
	fmt.Fprintf(w, "expires   %s\n", expiryText(b.ExpiresAt))
	fmt.Fprintf(w, "id        %s\n", safeText(b.ID))
	printConnection(w, b.Connection)
}

func printConnection(w io.Writer, c api.Connection) {
	c.Host, c.Database, c.User, c.SSLMode = safeText(c.Host), safeText(c.Database), safeText(c.User), safeText(c.SSLMode)
	fmt.Fprintf(w, "host      %s\n", c.Host)
	fmt.Fprintf(w, "port      %d\n", c.Port)
	fmt.Fprintf(w, "database  %s\n", c.Database)
	fmt.Fprintf(w, "user      %s\n", c.User)
	fmt.Fprintf(w, "sslmode   %s\n", c.SSLMode)
	fmt.Fprintf(w, "address   %s\n", connectionURL(c))
}

func printCreated(stdout, stderr io.Writer, c api.CreatedBranch) {
	fmt.Fprintf(stdout, "branch %s is being made (status: %s). The database is ready in a minute or so.\n", safeText(c.Name), safeText(c.Status))
	printBranch(stdout, c.Branch)
	if c.Password == "" {
		fmt.Fprintln(stderr, "koedodb: no password was made with the branch. Ask for one in the console.")
		return
	}
	fmt.Fprintf(stdout, "password  %s\n", safeText(c.Password))
	fmt.Fprintln(stderr, "koedodb: the password above is shown once and cannot be shown again. Keep it now.")
}
