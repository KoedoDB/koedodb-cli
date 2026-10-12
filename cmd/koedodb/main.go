// koedodb is the command line of KoedoDB. For now it does one thing: deploy a static site.
//
//	KOEDODB_TOKEN=... koedodb deploy ./dist
//
// Exit status: 0 done, 1 something else went wrong, 2 the command was used wrongly, 3 the token is missing or was not accepted, 4 the directory cannot be deployed (the message says why),
// 5 KoedoDB or the network did not answer as it should (try again).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"

	"github.com/koedodb/koedodb-cli/internal/api"
	"github.com/koedodb/koedodb-cli/internal/deploy"
	"github.com/koedodb/koedodb-cli/internal/pack"
)

// version is set at build time (-ldflags "-X main.version=..."); a build with `go install ...@v1.2.3` has it from the module's build information.
var version = "dev"

func versionString() string {
	if version != "dev" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return version
}

const defaultAPI = "https://api.koedodb.com/v1"

const (
	exitOK       = 0
	exitFailed   = 1
	exitUsage    = 2
	exitAuth     = 3
	exitInput    = 4
	exitServer   = 5
	tokenVarName = "KOEDODB_TOKEN"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	env := func(k string) string { return os.Getenv(k) }
	os.Exit(run(ctx, os.Args[1:], env, os.Stdout, os.Stderr, nil))
}

const usage = `koedodb deploys a static site to KoedoDB.

Usage:
  koedodb deploy [directory] [--json] [--force] [--allow-no-index] [--api URL]
  koedodb version

deploy packs the directory the build writes the site to (dist, build or out; with no directory
given, the first of those that exists), uploads it, and publishes it. Everything in the
directory is published on the internet.

The deploy token is read from the environment variable KOEDODB_TOKEN. Make one for the project in
the KoedoDB console.

Flags of deploy:
  --json            print one JSON object (the result, or the error) and nothing else
  --force           deploy a directory that looks like a project, or has .env files
  --allow-no-index  deploy a directory that has no index.html at its top
  --api URL         the API to use (or KOEDODB_API_URL; default ` + defaultAPI + `)

Exit status: 0 done, 2 used wrongly, 3 no token or not accepted, 4 the directory cannot be
deployed, 5 KoedoDB or the network did not answer as it should (try again), 1 anything else.
`

func run(ctx context.Context, args []string, env func(string) string, stdout, stderr io.Writer, httpClient *http.Client) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprint(stdout, usage)
		if len(args) == 0 {
			return exitUsage
		}
		return exitOK
	}
	switch args[0] {
	case "version", "--version":
		fmt.Fprintf(stdout, "koedodb %s\n", versionString())
		return exitOK
	case "deploy":
		return runDeploy(ctx, args[1:], env, stdout, stderr, httpClient)
	}
	fmt.Fprintf(stderr, "koedodb: %q is not a command. See `koedodb help`.\n", args[0])
	return exitUsage
}

func runDeploy(ctx context.Context, args []string, env func(string) string, stdout, stderr io.Writer, httpClient *http.Client) int {
	fs := flag.NewFlagSet("deploy", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var asJSON, force, noIndex bool
	var apiURL string
	fs.BoolVar(&asJSON, "json", false, "")
	fs.BoolVar(&force, "force", false, "")
	fs.BoolVar(&noIndex, "allow-no-index", false, "")
	fs.StringVar(&apiURL, "api", "", "")
	// Flags may come after the directory (`koedodb deploy ./dist --json`): the standard package stops at the first argument that is not a flag.
	var dirs []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			fmt.Fprintf(stderr, "koedodb deploy: %v. See `koedodb help`.\n", err)
			return exitUsage
		}
		if fs.NArg() == 0 {
			break
		}
		dirs = append(dirs, fs.Arg(0))
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
	if len(dirs) > 1 {
		return fail(exitUsage, "usage", "give one directory.")
	}
	dir := ""
	if len(dirs) == 1 {
		dir = dirs[0]
	} else {
		for _, candidate := range []string{"dist", "build", "out"} {
			if info, err := os.Stat(candidate); err == nil && info.IsDir() {
				dir = candidate
				break
			}
		}
		if dir == "" {
			return fail(exitInput, "no_directory", "no directory given, and there is no dist, build or out here. Build the site, then run: koedodb deploy <directory>")
		}
	}
	token := strings.TrimSpace(env(tokenVarName))
	if token == "" {
		return fail(exitAuth, "no_token", tokenVarName+" is not set. Make a deploy token for the project in the KoedoDB console and set it in the environment.")
	}
	if apiURL == "" {
		apiURL = env("KOEDODB_API_URL")
	}
	if apiURL == "" {
		apiURL = defaultAPI
	}
	if u, err := url.Parse(apiURL); err != nil || u.Host == "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost"))) {
		return fail(exitUsage, "usage", "the API address must be an https address.")
	}
	client := &api.Client{BaseURL: apiURL, Token: token, UserAgent: "koedodb-cli/" + versionString(), HTTP: httpClient}
	var progress io.Writer = stderr
	if asJSON {
		progress = nil
	}
	result, err := deploy.Run(ctx, client, deploy.Options{Dir: dir, Force: force, AllowNoIndex: noIndex, Progress: progress})
	if err != nil {
		code, kind, message := classify(err)
		return fail(code, kind, message)
	}
	if asJSON {
		_ = json.NewEncoder(stdout).Encode(result)
		return exitOK
	}
	if result.URL != "" {
		fmt.Fprintf(stdout, "%s\n", result.URL)
	}
	return exitOK
}

// classify turns an error into an exit status, a stable kind and a message that says what to do.
func classify(err error) (int, string, string) {
	var problem *pack.Problem
	var apiErr *api.Error
	var network *api.Network
	switch {
	case errors.As(err, &problem):
		return exitInput, "invalid_directory", problem.Msg
	case errors.As(err, &network):
		return exitServer, "network", err.Error() + ". Check the connection and run it again."
	case errors.As(err, &apiErr) && apiErr.Code == "upload_refused":
		// The store said no to the PUT (the address expired, or the size was not the one asked for). It is not the token: the token is never sent there.
		return exitServer, "upload_refused", fmt.Sprintf("the storage refused the upload (%d). The upload address may have expired: run it again.", apiErr.Status)
	case errors.As(err, &apiErr):
		switch apiErr.Status {
		case http.StatusUnauthorized, http.StatusForbidden:
			return exitAuth, "token_not_accepted", "the deploy token was not accepted. It may be wrong, expired or revoked: make a new one in the KoedoDB console."
		case http.StatusNotFound:
			return exitAuth, "not_found", "KoedoDB does not know this project for the token. Is it the token of the project you mean?"
		case http.StatusRequestEntityTooLarge:
			return exitInput, "archive_too_large", "the site is larger than a site may be (100 MiB packed)."
		case http.StatusConflict:
			if apiErr.Code == "project_not_active" {
				return exitServer, "project_not_active", "the project is not active yet (still being made, or unavailable). Try again in a minute."
			}
		case http.StatusUnprocessableEntity:
			return exitServer, apiErr.Code, "the upload did not arrive as it was sent (" + apiErr.Code + "). Run it again."
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
