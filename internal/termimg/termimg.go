// Package termimg encodes an image as a terminal's inline-image escape
// sequence: iTerm2's OSC 1337 or the kitty graphics protocol.
package termimg

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// Protocol's zero value, ProtocolNone, means no inline-image support.
type Protocol int

const (
	ProtocolNone Protocol = iota
	ProtocolITerm2
	ProtocolKitty
)

func (p Protocol) String() string {
	switch p {
	case ProtocolITerm2:
		return "iterm2"
	case ProtocolKitty:
		return "kitty"
	default:
		return "none"
	}
}

func Detect(termProgram, term string) Protocol {
	switch termProgram {
	case "iTerm.app", "WezTerm":
		return ProtocolITerm2
	case "ghostty":
		return ProtocolKitty
	}
	if term == "xterm-kitty" || strings.Contains(term, "kitty") || strings.Contains(term, "ghostty") {
		return ProtocolKitty
	}
	return ProtocolNone
}

func DetectEnv() Protocol {
	return Detect(os.Getenv("TERM_PROGRAM"), os.Getenv("TERM"))
}

// ErrUnsupported is Encode's error for ProtocolNone.
var ErrUnsupported = errors.New("termimg: no inline-image protocol")

// Encode: kitty only decodes PNG, iTerm2 decodes any format in data.
func Encode(w io.Writer, p Protocol, name string, data []byte) error {
	switch p {
	case ProtocolITerm2:
		return encodeITerm2(w, name, data)
	case ProtocolKitty:
		return encodeKitty(w, data)
	default:
		return ErrUnsupported
	}
}

// encodeITerm2 is BEL-terminated, as iTerm2 expects.
func encodeITerm2(w io.Writer, name string, data []byte) error {
	var b strings.Builder
	b.WriteString("\x1b]1337;File=")
	if name != "" {
		fmt.Fprintf(&b, "name=%s;", base64.StdEncoding.EncodeToString([]byte(name)))
	}
	fmt.Fprintf(&b, "size=%d;inline=1:", len(data))
	b.WriteString(base64.StdEncoding.EncodeToString(data))
	b.WriteByte('\a')
	_, err := io.WriteString(w, b.String())
	return err
}

// kittyChunk is kitty's max base64 payload per escape sequence.
const kittyChunk = 4096

func encodeKitty(w io.Writer, data []byte) error {
	payload := base64.StdEncoding.EncodeToString(data)
	for i := 0; i < len(payload) || i == 0; i += kittyChunk {
		end := min(i+kittyChunk, len(payload))
		more := 0
		if end < len(payload) {
			more = 1
		}
		ctrl := fmt.Sprintf("m=%d", more)
		if i == 0 {
			ctrl = fmt.Sprintf("a=T,f=100,%s", ctrl)
		}
		if _, err := fmt.Fprintf(w, "\x1b_G%s;%s\x1b\\", ctrl, payload[i:end]); err != nil {
			return err
		}
		if end == len(payload) {
			break
		}
	}
	return nil
}
