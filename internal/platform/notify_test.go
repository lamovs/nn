package platform

import (
	"context"
	"errors"
	"html"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lamovs/nn/internal/config"
)

func notifyConfig(enabled bool) config.Config {
	cfg := config.Default()
	cfg.Notify.Enabled = enabled
	return cfg
}

// awkwardTitle and awkwardBody would break out of a pasted AppleScript string.
const (
	awkwardTitle = `-e "quit" \" & (do shell script "touch /tmp/nn-owned") & "`
	awkwardBody  = "line one\\\nsay \"it's\" -- done\\"
)

func TestNotifyDarwinPassesTextAsArguments(t *testing.T) {
	f := newFakeSystem(t, "darwin", map[string]string{}, "osascript")
	if err := Notify(context.Background(), notifyConfig(true), awkwardTitle, awkwardBody); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("calls = %q", f.commands())
	}
	c := f.calls[0]
	want := []string{
		"-e", "on run argv",
		"-e", "display notification (item 2 of argv) with title (item 1 of argv)",
		"-e", "end run",
		"--", awkwardTitle, awkwardBody,
	}
	if c.name != "osascript" || !slices.Equal(c.args, want) {
		t.Fatalf("ran %s with %q\nwant %q", c.name, c.args, want)
	}
	for _, arg := range c.args[:len(c.args)-2] {
		if strings.Contains(arg, "touch") || strings.Contains(arg, "line one") {
			t.Errorf("the text leaked into the script: %q", arg)
		}
	}
	if c.stdin != "" {
		t.Errorf("stdin = %q, want none", c.stdin)
	}
}

func TestNotifyLinuxPassesTextAsArguments(t *testing.T) {
	f := newFakeSystem(t, "linux", map[string]string{"WAYLAND_DISPLAY": "wayland-1"}, "notify-send")
	if err := Notify(context.Background(), notifyConfig(true), awkwardTitle, awkwardBody); err != nil {
		t.Fatal(err)
	}
	want := []string{"--", awkwardTitle, strings.ReplaceAll(awkwardBody, `\`, `\\`)}
	if len(f.calls) != 1 || f.calls[0].name != "notify-send" || !slices.Equal(f.calls[0].args, want) {
		t.Fatalf("calls = %+v, want notify-send %q", f.calls, want)
	}
}

func TestNotifyLinuxEscapesTheBody(t *testing.T) {
	const title = `<b>T</b> \n & \101`
	for _, tc := range []struct{ body, want string }{
		{`C:\new\temp`, `C:\\new\\temp`},
		{`octal \101 and \n`, `octal \\101 and \\n`},
		{`trailing \`, `trailing \\`},
		{"a & b <b>bold</b> > c", "a &amp; b &lt;b&gt;bold&lt;/b&gt; &gt; c"},
		{`&amp; \& <\>`, `&amp;amp; \\&amp; &lt;\\&gt;`},
		{"line one\nline two", "line one\nline two"},
	} {
		f := newFakeSystem(t, "linux", map[string]string{}, "notify-send")
		if err := Notify(context.Background(), notifyConfig(true), title, tc.body); err != nil {
			t.Fatal(err)
		}
		want := []string{"--", title, tc.want}
		if len(f.calls) != 1 || !slices.Equal(f.calls[0].args, want) {
			t.Errorf("body %q: calls = %+v, want notify-send %q", tc.body, f.calls, want)
			continue
		}
		if shown := html.UnescapeString(gStrcompress(tc.want)); shown != tc.body {
			t.Errorf("body %q reaches the server as %q", tc.body, shown)
		}
	}
}

// gStrcompress undoes escapes as GLib's g_strcompress does.
func gStrcompress(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			b.WriteByte(s[i])
			continue
		}
		i++
		if i == len(s) {
			break // a trailing backslash: GLib warns and stops there
		}
		switch c := s[i]; {
		case c >= '0' && c <= '7':
			v := 0
			for n := 0; n < 3 && i < len(s) && s[i] >= '0' && s[i] <= '7'; n++ {
				v = v*8 + int(s[i]-'0')
				i++
			}
			i--
			b.WriteByte(byte(v))
		case strings.IndexByte("bfnrtv", c) >= 0:
			b.WriteByte("\b\f\n\r\t\v"[strings.IndexByte("bfnrtv", c)])
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

func TestNotifyQuietWithoutToolOrWhenDisabled(t *testing.T) {
	for _, tc := range []struct {
		name, goos string
		enabled    bool
		bins       []string
	}{
		{"darwin without osascript", "darwin", true, nil},
		{"linux without notify-send", "linux", true, nil},
		{"darwin disabled", "darwin", false, []string{"osascript"}},
		{"linux disabled", "linux", false, []string{"notify-send"}},
		{"other OS", "windows", true, []string{"osascript", "notify-send"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeSystem(t, tc.goos, map[string]string{}, tc.bins...)
			if err := Notify(context.Background(), notifyConfig(tc.enabled), "title", "body"); err != nil {
				t.Errorf("err = %v, want nil", err)
			}
			if len(f.calls) != 0 {
				t.Errorf("ran %q, want nothing", f.commands())
			}
		})
	}
}

func TestNotifyNeverHangs(t *testing.T) {
	prevTimeout := notifyTimeout
	notifyTimeout = 50 * time.Millisecond
	t.Cleanup(func() { notifyTimeout = prevTimeout })

	newFakeSystem(t, "darwin", map[string]string{}, "osascript")
	var deadline time.Time
	runCommand = func(ctx context.Context, c command) ([]byte, []byte, error) {
		deadline, _ = ctx.Deadline()
		<-ctx.Done()
		return nil, nil, ctx.Err()
	}

	start := time.Now()
	err := Notify(context.Background(), notifyConfig(true), "title", "body")
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Notify took %v with a %v timeout", elapsed, notifyTimeout)
	}
	if deadline.IsZero() || deadline.Sub(start) > notifyTimeout+100*time.Millisecond {
		t.Errorf("tool ran with deadline %v, want within %v of the start", deadline, notifyTimeout)
	}
	if err == nil || errors.Is(err, ErrCancelled) || !strings.Contains(err.Error(), "no answer within") {
		t.Errorf("err = %v, want a timeout that is not ErrCancelled", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Notify(ctx, notifyConfig(true), "title", "body"); !errors.Is(err, ErrCancelled) {
		t.Errorf("cancelled by the caller: err = %v, want ErrCancelled", err)
	}
}

func TestNotifyReportsAFailedTool(t *testing.T) {
	f := newFakeSystem(t, "linux", map[string]string{}, "notify-send")
	f.handle = func(c call) (string, string, error) {
		return "", "GDBus.Error: no notification daemon\n", exitError(t, 1)
	}
	err := Notify(context.Background(), notifyConfig(true), "title", "body")
	if err == nil || !strings.Contains(err.Error(), "notify-send: GDBus.Error") {
		t.Errorf("err = %v", err)
	}
}
