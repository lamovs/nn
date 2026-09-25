package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lamovs/nn/internal/config"
)

func htmlHandler(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, body)
	}
}

func fixedStatus(code int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) }
}

const urlTestHTML = `<!doctype html><html><head><title>PGO test page</title>` +
	`<meta name="description" content="How PGO works in Go"></head><body><main><p>` +
	`Profile guided optimization improves Go binaries. Profile guided optimization improves Go binaries. ` +
	`Profile guided optimization improves Go binaries. Profile guided optimization improves Go binaries. ` +
	`Profile guided optimization improves Go binaries. Profile guided optimization improves Go binaries. ` +
	`Profile guided optimization improves Go binaries. Profile guided optimization improves Go binaries. ` +
	`Profile guided optimization improves Go binaries. Profile guided optimization improves Go binaries.` +
	`</p></main></body></html>`

// urlAIConfig fakes ai.tasks.url with consent fixed to "always", so no terminal is ever needed.
func urlAIConfig(t *testing.T, reply string) (requestFile string) {
	t.Helper()
	base := t.TempDir()
	requestFile = filepath.Join(base, "request")
	t.Setenv("NN_URL_AI_REQUEST", requestFile)
	engine := filepath.Join(base, "fake-model")
	script := "#!/bin/sh\n/bin/cat > \"$NN_URL_AI_REQUEST\"\nprintf '%s' '" + strings.ReplaceAll(reply, "'", "'\\''") + "'\n"
	if err := os.WriteFile(engine, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	command := "[" + config.TOMLString(engine) + "]"
	text := "[ai]\nprofile=\"url\"\n[ai.profiles.url]\nengine=\"command\"\ncommand=" + command + "\n[ai.consent]\nurl=\"always\"\n[notify]\nenabled=false\n"
	writeConfig(t, text)
	return requestFile
}

// urlSleepingAIConfig sleeps past any test deadline, so a mid-flight cancellation is deterministic.
func urlSleepingAIConfig(t *testing.T) {
	t.Helper()
	base := t.TempDir()
	engine := filepath.Join(base, "fake-model")
	script := "#!/bin/sh\ncat >/dev/null\nsleep 5\nprintf '%s' '{\"title\":\"t\",\"tags\":[],\"body\":\"b\"}'\n"
	if err := os.WriteFile(engine, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	command := "[" + config.TOMLString(engine) + "]"
	text := "[ai]\nprofile=\"url\"\n[ai.profiles.url]\nengine=\"command\"\ncommand=" + command + "\n[ai.consent]\nurl=\"always\"\n[notify]\nenabled=false\n"
	writeConfig(t, text)
}

func noNoteExists(t *testing.T, root string) {
	t.Helper()
	entries, _ := os.ReadDir(filepath.Join(root, "nn"))
	if len(entries) != 0 {
		t.Fatalf("note created, want none: %v", entries)
	}
}

func oneNote(t *testing.T, root string) string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, "nn"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("ReadDir: %v, entries=%v", err, entries)
	}
	data, err := os.ReadFile(filepath.Join(root, "nn", entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestURLHelpAndIndex(t *testing.T) {
	newTestVault(t)
	stdout, stderr, code := runCmd(t, "", "url", "--help")
	if code != 0 || !strings.Contains(stdout, "nn url") || !strings.Contains(stdout, "--allow-private") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	stdout, _, _ = runCmd(t, "")
	if !strings.Contains(stdout, "url") {
		t.Fatal("url absent from the command index")
	}
}

func TestURLOptionErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"tag missing", []string{"-t"}, "-t needs"},
		{"title missing", []string{"--title"}, "--title needs"},
		{"model missing", []string{"--model"}, "--model needs"},
		{"model empty", []string{"--model="}, "--model needs"},
		{"effort missing", []string{"--effort"}, "--effort needs"},
		{"mode missing", []string{"--ai-mode"}, "--ai-mode needs"},
		{"mode invalid", []string{"--ai-mode", "immediate"}, "--ai-mode"},
		{"mode invalid equals", []string{"--ai-mode=immediate"}, "--ai-mode"},
		{"unknown flag", []string{"--bogus"}, "unknown option"},
		{"two words", []string{"https://e.test/a", "extra"}, "at most one link"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := newTestVault(t)
			stdout, stderr, code := runCmd(t, "", append([]string{"url"}, tc.args...)...)
			if code != 2 || stdout != "" || !strings.Contains(stderr, tc.want) {
				t.Fatalf("code=%d stdout=%q stderr=%q; want %q", code, stdout, stderr, tc.want)
			}
			noNoteExists(t, root)
		})
	}
}

func TestURLArgWinsAndClipboardNotRead(t *testing.T) {
	newTestVault(t)
	calls := withFakeClipboard(t, nil, "", nil, []byte("must not be read"), nil)
	_, stderr, code := runCmd(t, "", "url", "not-a-url")
	if code != 2 || !strings.Contains(stderr, "http") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if *calls != 0 {
		t.Fatalf("clipboard read %d time(s), want 0", *calls)
	}
}

func TestURLStdinNeverConsumed(t *testing.T) {
	root := newTestVault(t)
	pr, pw := io.Pipe()
	defer pw.Close()
	var out, errBuf strings.Builder
	code := runText(context.Background(), []string{"url", "http://127.0.0.1:1/", "--no-ai"}, pr, &out, &errBuf)
	if code != 0 || !strings.HasPrefix(strings.TrimSpace(out.String()), "+ ") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errBuf.String())
	}
	oneNote(t, root)
}

func TestURLClipboardRejectedCases(t *testing.T) {
	for _, tc := range []struct {
		name string
		text []byte
	}{
		{"empty", []byte("   ")},
		{"prose", []byte("just some prose here")},
		{"two tokens", []byte("https://a.test https://b.test")},
		{"markdown", []byte("[title](https://e.test/p)")},
		{"angle brackets", []byte("<https://e.test/p>")},
		{"oversized", []byte("https://e.test/" + strings.Repeat("a", 8200))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := newTestVault(t)
			calls := withFakeClipboard(t, nil, "", nil, tc.text, nil)
			stdout, stderr, code := runCmd(t, "", "url")
			if code != 2 || stdout != "" || strings.TrimSpace(stderr) != "nn: url: clipboard does not contain a single http or https link" {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			if *calls != 1 {
				t.Fatalf("clipboard read %d time(s), want 1", *calls)
			}
			noNoteExists(t, root)
		})
	}
}

func TestURLClipboardReadError(t *testing.T) {
	root := newTestVault(t)
	withFakeClipboard(t, nil, "", nil, nil, os.ErrPermission)
	stdout, stderr, code := runCmd(t, "", "url")
	if code != 2 || stdout != "" || strings.Contains(stderr, "does not contain a single") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	noNoteExists(t, root)
}

func TestURLClipboardValidLinkProceedsPastValidation(t *testing.T) {
	root := newTestVault(t)
	calls := withFakeClipboard(t, nil, "", nil, []byte("  http://127.0.0.1:1/page  \n"), nil)
	stdout, stderr, code := runCmd(t, "", "url", "--no-ai")
	if code != 0 || !strings.HasPrefix(strings.TrimSpace(stdout), "+ ") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if *calls != 1 {
		t.Fatalf("clipboard read %d time(s), want 1", *calls)
	}
	if !strings.Contains(stderr, "fetch 127.0.0.1: blocked address") {
		t.Fatalf("stderr=%q", stderr)
	}
	oneNote(t, root)
}

func TestURLPreNetworkRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"invalid link", []string{"not-a-url"}, "must start with http"},
		{"userinfo", []string{"https://user:pass@e.test/path"}, "user name or password"},
		{"userinfo with allow-secret", []string{"https://user:pass@e.test/path", "--allow-secret"}, "user name or password"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := newTestVault(t)
			stdout, stderr, code := runCmd(t, "", append([]string{"url"}, tc.args...)...)
			if code != 2 || stdout != "" || !strings.Contains(stderr, tc.want) {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			noNoteExists(t, root)
		})
	}
}

func TestURLSensitiveLinkRefusedWithZeroHits(t *testing.T) {
	root := newTestVault(t)
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
	}))
	defer srv.Close()
	stdout, stderr, code := runCmd(t, "", "url", srv.URL+"/page?token=secret123", "--allow-private")
	if code != 2 || stdout != "" || !strings.Contains(stderr, "credential") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if atomic.LoadInt32(&hits) != 0 {
		t.Fatalf("server was contacted: hits=%d", hits)
	}
	noNoteExists(t, root)
}

func TestURLSensitiveLinkAllowSecretProceeds(t *testing.T) {
	root := newTestVault(t)
	var hits int32
	srv := httptest.NewServer(func() http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&hits, 1)
			htmlHandler(urlTestHTML)(w, r)
		}
	}())
	defer srv.Close()
	stdout, stderr, code := runCmd(t, "", "url", srv.URL+"/page?token=secret123", "--allow-private", "--allow-secret", "--no-ai")
	if code != 0 || !strings.HasPrefix(strings.TrimSpace(stdout), "+ ") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("hits=%d, want 1", hits)
	}
	oneNote(t, root)
}

func TestURLFetchClassFailures(t *testing.T) {
	for _, tc := range []struct {
		name       string
		handler    http.HandlerFunc
		wantReason string
		wantHint   bool
	}{
		{"403", fixedStatus(403), "HTTP 403", false},
		{"500", fixedStatus(500), "HTTP 500", false},
		{"unsupported content", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/pdf")
			io.WriteString(w, "%PDF-1.4 fake")
		}, "unsupported content type application/pdf", true},
		{"unsupported encoding", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-16")
			io.WriteString(w, "<html><body>plain ascii body</body></html>")
		}, "unsupported encoding utf-16", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := newTestVault(t)
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			stdout, stderr, code := runCmd(t, "", "url", srv.URL, "--allow-private", "--no-ai")
			if code != 0 || !strings.HasPrefix(strings.TrimSpace(stdout), "+ ") {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			if !strings.Contains(stderr, "fetch 127.0.0.1: "+tc.wantReason) {
				t.Fatalf("stderr=%q", stderr)
			}
			if hinted := strings.Contains(stderr, "use nn add LINK to keep it as plain text"); hinted != tc.wantHint {
				t.Fatalf("stderr=%q, wantHint=%v", stderr, tc.wantHint)
			}
			body := oneNote(t, root)
			if !strings.Contains(body, "Page not fetched: "+tc.wantReason) {
				t.Fatalf("note body=%s", body)
			}
		})
	}
}

func TestURLLoopbackBlockedWithoutAllowPrivate(t *testing.T) {
	root := newTestVault(t)
	stdout, stderr, code := runCmd(t, "", "url", "http://127.0.0.1:1/", "--no-ai")
	if code != 0 || !strings.HasPrefix(strings.TrimSpace(stdout), "+ ") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "fetch 127.0.0.1: blocked address") {
		t.Fatalf("stderr=%q", stderr)
	}
	body := oneNote(t, root)
	if !strings.Contains(body, "Page not fetched: blocked address") {
		t.Fatalf("note body=%s", body)
	}
}

func TestURLLoopbackWorksWithAllowPrivate(t *testing.T) {
	root := newTestVault(t)
	srv := httptest.NewServer(htmlHandler(urlTestHTML))
	defer srv.Close()
	stdout, stderr, code := runCmd(t, "", "url", srv.URL+"/blog/pgo", "--allow-private", "--no-ai")
	if code != 0 || !strings.HasPrefix(strings.TrimSpace(stdout), "+ ") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	body := oneNote(t, root)
	for _, want := range []string{"Source: <" + srv.URL + "/blog/pgo>", "Page title: PGO test page", "Description: How PGO works in Go"} {
		if !strings.Contains(body, want) {
			t.Fatalf("note missing %q; body=%s", want, body)
		}
	}
	_ = stderr
}

func TestURLWaitEndToEnd(t *testing.T) {
	root := newTestVault(t)
	srv := httptest.NewServer(htmlHandler(urlTestHTML))
	defer srv.Close()
	requestFile := urlAIConfig(t, `{"title":"Profile guided optimization","tags":["golang","performance"],"body":"Go 1.21 added PGO.\n\n- Faster builds\n- Smaller code"}`)

	stdout, _, code := runCmd(t, "", "url", srv.URL+"/blog/pgo", "--allow-private", "--ai-mode", "wait")
	if code != 0 || !strings.HasPrefix(strings.TrimSpace(stdout), "+ ") {
		t.Fatalf("code=%d stdout=%q", code, stdout)
	}
	body := oneNote(t, root)
	for _, want := range []string{
		"Source: <" + srv.URL + "/blog/pgo>",
		"Page title: PGO test page",
		"Description: How PGO works in Go",
		"## Summary",
		"Go 1.21 added PGO.",
		"#golang", "#performance",
		"aliases: [Profile guided optimization]",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("note missing %q; body=%s", want, body)
		}
	}
	req, err := os.ReadFile(requestFile)
	if err != nil || !strings.Contains(string(req), srv.URL+"/blog/pgo") {
		t.Fatalf("request=%s err=%v", req, err)
	}
}

func TestURLPageSecretSkipsAIWithoutAllowSecret(t *testing.T) {
	root := newTestVault(t)
	// Fixture is split so secret scanners do not flag it.
	secretHTML := `<!doctype html><html><head><title>Leaky page</title>` +
		`<meta name="description" content="Key AK` + `IAABCDEFGHIJKLMNOP leaked"></head>` +
		`<body><main><p>filler text so the page reads as a real article. filler text so the page reads as a real article.</p></main></body></html>`
	srv := httptest.NewServer(htmlHandler(secretHTML))
	defer srv.Close()
	requestFile := urlAIConfig(t, `{"title":"t","tags":[],"body":"b"}`)

	stdout, stderr, code := runCmd(t, "", "url", srv.URL, "--allow-private", "--ai-mode", "wait")
	if code != 0 || !strings.HasPrefix(strings.TrimSpace(stdout), "+ ") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "AI skipped: page text may contain credentials") {
		t.Fatalf("stderr=%q", stderr)
	}
	body := oneNote(t, root)
	if strings.Contains(body, "## Summary") {
		t.Fatalf("summary present despite skipped AI: %s", body)
	}
	if _, err := os.Stat(requestFile); err == nil {
		t.Fatal("model was called despite skipped AI")
	}
}

func TestURLPageSecretSentWithAllowSecret(t *testing.T) {
	root := newTestVault(t)
	secretHTML := `<!doctype html><html><head><title>Leaky page</title>` +
		`<meta name="description" content="Key AK` + `IAABCDEFGHIJKLMNOP leaked"></head>` +
		`<body><main><p>filler text so the page reads as a real article. filler text so the page reads as a real article.</p></main></body></html>`
	srv := httptest.NewServer(htmlHandler(secretHTML))
	defer srv.Close()
	requestFile := urlAIConfig(t, `{"title":"t","tags":[],"body":"summarized despite the key"}`)

	stdout, stderr, code := runCmd(t, "", "url", srv.URL, "--allow-private", "--allow-secret", "--ai-mode", "wait")
	if code != 0 || !strings.HasPrefix(strings.TrimSpace(stdout), "+ ") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	body := oneNote(t, root)
	if !strings.Contains(body, "## Summary") {
		t.Fatalf("summary missing: %s", body)
	}
	req, err := os.ReadFile(requestFile)
	if err != nil || !strings.Contains(string(req), "AK"+"IAABCDEFGHIJKLMNOP") {
		t.Fatalf("request=%s err=%v", req, err)
	}
}

func TestURLConsentMatrix(t *testing.T) {
	for _, tc := range []struct {
		name     string
		config   string
		args     []string
		wantHint bool
		wantErr  bool
	}{
		{"never is silent", "[ai.consent]\nclaude = \"never\"\n", nil, false, false},
		{"ask without tty hints", "[ai.consent]\nclaude = \"ask\"\n", nil, true, false},
		{"explicit ai with never errors", "[ai.consent]\nclaude = \"never\"\n", []string{"--ai"}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := newTestVault(t)
			writeConfig(t, tc.config)
			fakeEngines(t, "claude", "codex")
			srv := httptest.NewServer(htmlHandler(urlTestHTML))
			defer srv.Close()
			args := append([]string{"url", srv.URL, "--allow-private"}, tc.args...)
			stdout, stderr, code := runCmd(t, "", args...)
			if tc.wantErr {
				if code != 2 || stdout != "" {
					t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
				}
				noNoteExists(t, root)
				return
			}
			if code != 0 || !strings.HasPrefix(strings.TrimSpace(stdout), "+ ") {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			if hinted := strings.Contains(stderr, "AI needs consent"); hinted != tc.wantHint {
				t.Fatalf("stderr=%q, wantHint=%v", stderr, tc.wantHint)
			}
			body := oneNote(t, root)
			if strings.Contains(body, "## Summary") {
				t.Fatalf("summary present without consent: %s", body)
			}
		})
	}
}

func TestURLAIModeBackgroundFlagAccepted(t *testing.T) {
	root := newTestVault(t)
	stdout, stderr, code := runCmd(t, "", "url", "http://127.0.0.1:1/", "--ai-mode", "background", "--no-ai")
	if code != 0 || !strings.HasPrefix(strings.TrimSpace(stdout), "+ ") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	oneNote(t, root)
}

func TestURLCancelDuringFetch(t *testing.T) {
	root := newTestVault(t)
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	defer srv.Close()
	defer close(block)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	var out, errBuf strings.Builder
	code := runText(ctx, []string{"url", srv.URL, "--allow-private", "--no-ai"}, strings.NewReader(""), &out, &errBuf)
	if code != 130 || out.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errBuf.String())
	}
	noNoteExists(t, root)
}

func TestURLCancelDuringWaitModelCall(t *testing.T) {
	root := newTestVault(t)
	srv := httptest.NewServer(htmlHandler(urlTestHTML))
	defer srv.Close()
	urlSleepingAIConfig(t)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()
	var out, errBuf strings.Builder
	code := runText(ctx, []string{"url", srv.URL, "--allow-private", "--ai-mode", "wait"}, strings.NewReader(""), &out, &errBuf)
	if code != 0 || !strings.HasPrefix(strings.TrimSpace(out.String()), "+ ") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errBuf.String())
	}
	body := oneNote(t, root)
	if !strings.Contains(body, "Link summary did not finish. Original retained at:") {
		t.Fatalf("note missing recovery text: %s", body)
	}
}

func TestURLJSONOutput(t *testing.T) {
	newTestVault(t)
	stdout, stderr, code := runCmd(t, "", "url", "http://127.0.0.1:1/", "--no-ai", "--json")
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	rows := decodeJSONRows[addRow](t, stdout)
	if len(rows) != 1 || rows[0].Action != "create" || rows[0].Path == "" {
		t.Fatalf("rows=%+v", rows)
	}
}

func TestURLMissingVaultFailsExitTwo(t *testing.T) {
	newTestVault(t)
	t.Setenv("NN_ROOT", "")
	stdout, stderr, code := runCmd(t, "", "url", "http://127.0.0.1:1/", "--no-ai")
	if code != 2 || stdout != "" || !strings.Contains(stderr, "vault.root") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}
