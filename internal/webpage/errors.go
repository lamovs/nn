package webpage

import (
	"errors"
	"regexp"
	"strings"
)

var (
	ErrTimeout             = errors.New("timeout")
	ErrLookup              = errors.New("DNS lookup failed")
	ErrConnect             = errors.New("connection failed")
	ErrTLS                 = errors.New("TLS verification failed")
	ErrBlockedAddress      = errors.New("blocked address")
	ErrTooManyRedirects    = errors.New("too many redirects")
	ErrRedirectRefused     = errors.New("redirect refused")
	ErrStatus              = errors.New("HTTP status")
	ErrBodyRead            = errors.New("body read failed")
	ErrUnsupportedContent  = errors.New("unsupported content type")
	ErrUnsupportedEncoding = errors.New("unsupported encoding")
)

// FetchError's text never includes the link path/query/fragment.
type FetchError struct {
	Host   string
	Reason string
	Err    error
}

func (e *FetchError) Error() string { return "fetch " + e.Host + ": " + e.Reason }

func (e *FetchError) Unwrap() error { return e.Err }

func fetchError(host string, sentinel error, reason string) *FetchError {
	if reason == "" {
		reason = sentinel.Error()
	}
	return &FetchError{Host: host, Reason: reason, Err: sentinel}
}

// safeToken is the only shape a server-supplied label may show in a Reason.
var safeToken = regexp.MustCompile(`^[a-z0-9.+/_-]{1,64}$`)

func withToken(reason, token string) string {
	token = strings.ToLower(strings.TrimSpace(token))
	if !safeToken.MatchString(token) {
		return reason
	}
	return reason + " " + token
}
