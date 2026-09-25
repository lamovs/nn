package webpage

import (
	"errors"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/lamovs/nn/internal/capture"
)

const maxLinkBytes = 8 << 10

type Link struct {
	Raw string   // trimmed user text, stored and sent
	URL *url.URL // parsed Raw
}

// ParseLink refuses whitespace, control and format (Cf) characters and
// < > " ` \: urlLinkBody writes Raw unescaped inside <...>. Errors never
// quote s, which can be clipboard content.
func ParseLink(s string) (Link, error) {
	raw := strings.TrimSpace(s)
	switch {
	case raw == "":
		return Link{}, errors.New("empty link")
	case len(raw) > maxLinkBytes:
		return Link{}, errors.New("link is longer than 8 KiB")
	case !utf8.ValidString(raw):
		return Link{}, errors.New("link is not valid UTF-8")
	case strings.Contains(raw, "[[") || strings.Contains(raw, "]]"):
		return Link{}, errors.New("link contains [[ or ]]")
	}
	for _, r := range raw {
		if unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return Link{}, errors.New("link contains whitespace, control or format characters")
		}
		if strings.ContainsRune("<>\"`\\", r) {
			return Link{}, errors.New("link contains one of < > \" ` \\")
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return Link{}, errors.New("invalid link")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return Link{}, errors.New("link must start with http:// or https://")
	}
	if u.User != nil {
		return Link{}, errors.New("link must not contain a user name or password")
	}
	if u.Opaque != "" || u.Hostname() == "" {
		return Link{}, errors.New("link has no host")
	}
	// url.Parse decodes %XX, so the host needs its own character check.
	if host := u.Hostname(); !utf8.ValidString(host) || strings.ContainsFunc(host, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
	}) {
		return Link{}, errors.New("link host contains control or format characters")
	}
	return Link{Raw: raw, URL: u}, nil
}

func (l Link) Host() string {
	if l.URL == nil {
		return ""
	}
	return strings.ToLower(l.URL.Hostname())
}

// Sensitive names credential kinds only, never values or parameter names.
func (l Link) Sensitive() []string {
	var kinds []string
	add := func(kind string) {
		for _, k := range kinds {
			if k == kind {
				return
			}
		}
		kinds = append(kinds, kind)
	}
	for _, s := range capture.FindSecrets(l.Raw) {
		add(s.Kind)
	}
	if l.URL == nil {
		return kinds
	}
	for _, seg := range strings.Split(l.URL.EscapedPath(), "/") {
		if _, params, ok := strings.Cut(seg, ";"); ok && sensitiveParams(params) {
			add("path-parameter")
			break
		}
	}
	if sensitiveParams(l.URL.RawQuery) {
		add("query-parameter")
	}
	if sensitiveParams(l.URL.EscapedFragment()) {
		add("fragment-parameter")
	}
	return kinds
}

var secretSegments = map[string]bool{
	"token": true, "secret": true, "password": true, "passwd": true, "pwd": true,
	"pass": true, "signature": true, "sig": true, "credential": true,
	"credentials": true, "apikey": true, "key": true, "auth": true, "otp": true,
	"jwt": true, "sid": true, "session": true, "sessionid": true, "code": true,
}

var secretSubstrings = []string{"token", "secret", "password", "signature", "credential", "apikey", "api_key", "sessionid", "sessid"}

func sensitiveParams(s string) bool {
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == '&' || r == ';' || r == '?' })
	for _, part := range parts {
		name, _, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		if decoded, err := url.QueryUnescape(name); err == nil {
			name = decoded
		}
		if sensitiveName(strings.ToLower(name)) {
			return true
		}
	}
	return false
}

func sensitiveName(name string) bool {
	for _, sub := range secretSubstrings {
		if strings.Contains(name, sub) {
			return true
		}
	}
	segments := strings.FieldsFunc(name, func(r rune) bool { return r == '_' || r == '-' || r == '.' })
	for _, seg := range segments {
		if secretSegments[seg] {
			return true
		}
	}
	return false
}
