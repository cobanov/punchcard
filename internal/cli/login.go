package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// loginTimeout bounds how long the CLI waits for the browser round trip.
const loginTimeout = 3 * time.Minute

// urlUnescape is exposed for the test's helper; the CLI itself never needs it.
func urlUnescape(s string) (string, error) { return url.QueryUnescape(s) }

// Login signs in through the browser and returns a bearer token.
//
// The flow: bind a loopback listener, open the server's GitHub sign-in with
// `redirect_to` pointing at it, and wait. punchcard authenticates the user,
// mints a device token, stashes it behind a single-use code and redirects back
// to the listener with only that code. The CLI trades the code for the token.
//
// The token itself never travels through the browser, and therefore never
// reaches shell history, browser history or OS logs — which is the whole reason
// the exchange step exists rather than putting the token in the redirect.
//
// The sign-in asks for the `repo` scope, so the same authorization that signs
// you in also connects commit matching. Asking twice for the same provider
// would be worse than asking once for a little more.
func Login(baseURL string) (string, error) {
	return runLogin(baseURL, openBrowser)
}

// runLogin is Login with the browser step injected, so the flow can be tested
// without a browser.
func runLogin(baseURL string, open func(string) error) (string, error) {
	// Port 0: the OS picks a free port. A fixed port would collide with
	// whatever else the developer is running, on the one machine where that is
	// most likely.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("cannot open a local listener: %w", err)
	}
	defer func() { _ = listener.Close() }()

	redirectTo := "http://127.0.0.1:" + portOf(listener) + "/callback"
	codes := make(chan string, 1)
	fail := make(chan error, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if code := q.Get("code"); code != "" {
			_, _ = w.Write([]byte(loginDonePage))
			codes <- code
			return
		}
		reason := q.Get("error")
		if reason == "" {
			reason = "the sign-in did not complete"
		}
		// The reason goes to the terminal, not into this page. Reflecting a
		// provider-supplied string into HTML is a bug waiting to happen, and it
		// buys nothing: the user is looking at the terminal, which is where the
		// CLI is about to print the same sentence.
		_, _ = w.Write([]byte(loginFailedPage))
		fail <- errors.New(reason)
	})

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(listener) }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	authURL := strings.TrimRight(baseURL, "/") + "/v1/auth/oauth/github?scope=repo&client=cli&redirect_to=" +
		url.QueryEscape(redirectTo)
	if err := open(authURL); err != nil {
		return "", fmt.Errorf("could not open a browser — open this yourself:\n\n  %s\n\n%w", authURL, err)
	}

	select {
	case code := <-codes:
		return exchange(baseURL, code)
	case err := <-fail:
		return "", err
	case <-time.After(loginTimeout):
		return "", errors.New("timed out waiting for the browser")
	}
}

// exchange trades the single-use code for the device token.
func exchange(baseURL, code string) (string, error) {
	var out struct {
		Token string `json:"token"`
	}
	c := New(baseURL, "")
	if err := c.do(http.MethodPost, "/v1/auth/native/exchange", map[string]any{"code": code}, &out); err != nil {
		return "", err
	}
	if out.Token == "" {
		return "", errors.New("the server returned no token")
	}
	return out.Token, nil
}

func portOf(l net.Listener) string {
	return fmt.Sprint(l.Addr().(*net.TCPAddr).Port)
}

// openBrowser opens a URL in the user's default browser.
func openBrowser(target string) error {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "windows":
		cmd, args = "rundll32", []string{"url.dll,FileProtocolHandler"}
	default:
		cmd = "xdg-open"
	}
	// #nosec G204 -- target is built from the configured base URL and a
	// loopback address, never from user input.
	return exec.Command(cmd, append(args, target)...).Start()
}

const loginDonePage = `<!doctype html><meta charset="utf-8"><title>punchcard</title>
<style>body{font:16px/1.6 ui-serif,Georgia,serif;margin:0;display:grid;place-items:center;
height:100vh;background:#fbfaf8;color:#1b1a17}
@media(prefers-color-scheme:dark){body{background:#14130f;color:#eceae4}}
p{margin:0;text-align:center}small{color:#6b675f}</style>
<p>Signed in.<br><small>You can close this tab and go back to the terminal.</small></p>`

const loginFailedPage = `<!doctype html><meta charset="utf-8"><title>punchcard</title>
<style>body{font:16px/1.6 ui-serif,Georgia,serif;margin:0;display:grid;place-items:center;
height:100vh;background:#fbfaf8;color:#1b1a17}
@media(prefers-color-scheme:dark){body{background:#14130f;color:#eceae4}}
p{margin:0;text-align:center}small{color:#6b675f}</style>
<p>Sign-in did not complete.<br><small>Go back to the terminal — it says why.</small></p>`
