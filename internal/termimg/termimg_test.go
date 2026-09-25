package termimg

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func TestDetect(t *testing.T) {
	cases := []struct {
		termProgram, term string
		want              Protocol
	}{
		{"iTerm.app", "xterm-256color", ProtocolITerm2},
		{"WezTerm", "xterm-256color", ProtocolITerm2},
		{"ghostty", "xterm-ghostty", ProtocolKitty},
		{"", "xterm-kitty", ProtocolKitty},
		{"", "xterm-ghostty", ProtocolKitty},
		{"Apple_Terminal", "xterm-256color", ProtocolNone},
		{"", "screen", ProtocolNone},
		{"", "", ProtocolNone},
	}
	for _, c := range cases {
		if got := Detect(c.termProgram, c.term); got != c.want {
			t.Errorf("Detect(%q, %q) = %v, want %v", c.termProgram, c.term, got, c.want)
		}
	}
}

func TestEncodeITerm2(t *testing.T) {
	var buf bytes.Buffer
	data := []byte{0x89, 'P', 'N', 'G', 0, 1, 2, 3}
	if err := Encode(&buf, ProtocolITerm2, "shot.png", data); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.HasPrefix(out, "\x1b]1337;File=") {
		t.Fatalf("missing OSC 1337 prefix: %q", out)
	}
	if !strings.HasSuffix(out, "\a") {
		t.Fatalf("missing BEL terminator: %q", out)
	}
	wantName := base64.StdEncoding.EncodeToString([]byte("shot.png"))
	if !strings.Contains(out, "name="+wantName+";") {
		t.Errorf("name not encoded: %q", out)
	}
	wantSize := "size=8;"
	if !strings.Contains(out, wantSize) {
		t.Errorf("size missing: %q", out)
	}
	wantData := base64.StdEncoding.EncodeToString(data)
	if !strings.Contains(out, ":"+wantData+"\a") {
		t.Errorf("payload not found: %q", out)
	}
}

func TestEncodeITerm2NoName(t *testing.T) {
	var buf bytes.Buffer
	if err := Encode(&buf, ProtocolITerm2, "", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "name=") {
		t.Errorf("unexpected name= with no name given: %q", buf.String())
	}
}

func TestEncodeKittySingleChunk(t *testing.T) {
	var buf bytes.Buffer
	data := []byte("small image bytes")
	if err := Encode(&buf, ProtocolKitty, "shot.png", data); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Count(out, "\x1b_G") != 1 {
		t.Fatalf("expected exactly one kitty sequence, got %q", out)
	}
	if !strings.Contains(out, "a=T,f=100,m=0;") {
		t.Errorf("expected a single-chunk control block, got %q", out)
	}
	wantData := base64.StdEncoding.EncodeToString(data)
	if !strings.Contains(out, ";"+wantData+"\x1b\\") {
		t.Errorf("payload not found: %q", out)
	}
}

func TestEncodeKittyChunked(t *testing.T) {
	var buf bytes.Buffer
	data := bytes.Repeat([]byte{'a'}, 6000)
	if err := Encode(&buf, ProtocolKitty, "", data); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if got := strings.Count(out, "\x1b_G"); got != 2 {
		t.Fatalf("expected 2 chunks for a 6000-byte image, got %d in %q", got, out)
	}
	if !strings.Contains(out, "a=T,f=100,m=1;") {
		t.Errorf("first chunk should announce more data: %q", out)
	}
	if !strings.HasSuffix(out, "\x1b\\") {
		t.Errorf("missing ST terminator: %q", out)
	}

	parts := strings.Split(out, "\x1b_G")[1:]
	var payload strings.Builder
	for _, part := range parts {
		part = strings.TrimSuffix(part, "\x1b\\")
		_, b64, ok := strings.Cut(part, ";")
		if !ok {
			t.Fatalf("malformed chunk %q", part)
		}
		payload.WriteString(b64)
	}
	decoded, err := base64.StdEncoding.DecodeString(payload.String())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, data) {
		t.Error("reassembled payload does not match the original image bytes")
	}
}

func TestEncodeUnsupported(t *testing.T) {
	var buf bytes.Buffer
	err := Encode(&buf, ProtocolNone, "x.png", []byte("x"))
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
	if buf.Len() != 0 {
		t.Errorf("expected nothing written, got %q", buf.String())
	}
}
