// Package webpage fetches a web page and extracts its readable text.
package webpage

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const FetchTimeout = 30 * time.Second

const (
	dialTimeout    = 10 * time.Second
	tlsTimeout     = 10 * time.Second
	headerTimeout  = 20 * time.Second
	maxHeaderBytes = 64 << 10
	maxBodyBytes   = 4 << 20 // after gzip decoding; longer bodies are truncated
	maxRedirects   = 5
	acceptHeader   = "text/html, application/xhtml+xml, text/plain;q=0.9, */*;q=0.1"
)

type Fetcher struct {
	UserAgent    string
	AllowPrivate bool // lifts the address policy for every hop of a fetch

	lookup  func(ctx context.Context, host string) ([]netip.Addr, error) // nil: the dialer resolves
	dialer  *net.Dialer                                                  // template; its Control is always replaced by the address policy
	roots   *x509.CertPool                                               // nil: system roots
	now     func() time.Time                                             // nil: time.Now
	timeout time.Duration                                                // 0: FetchTimeout
	public  func(netip.AddrPort) bool                                    // nil: publicAddr
}

type Page struct {
	Title, Description, Text string
	Truncated                bool   // body or text cap reached
	Kind                     string // "html" or "text"
}

// Fetch never sends the link fragment.
func (f *Fetcher) Fetch(ctx context.Context, l Link) (Page, error) {
	if err := ctx.Err(); err != nil {
		return Page{}, err
	}
	if l.URL == nil || l.URL.Scheme != "http" && l.URL.Scheme != "https" || l.URL.User != nil || l.URL.Hostname() == "" {
		return Page{}, errors.New("webpage: fetch needs a link from ParseLink")
	}
	host := l.Host()
	now, timeout := time.Now, FetchTimeout
	if f.now != nil {
		now = f.now
	}
	if f.timeout > 0 {
		timeout = f.timeout
	}
	fctx, cancel := context.WithDeadline(ctx, now().Add(timeout))
	defer cancel()

	tr := f.transport()
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: redirectGuard{tr}, CheckRedirect: checkRedirect}
	target := *l.URL
	target.Fragment, target.RawFragment = "", ""
	req, err := http.NewRequestWithContext(fctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return Page{}, fetchError(host, ErrConnect, "")
	}
	ua := f.UserAgent
	if ua == "" {
		ua = "nn"
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", acceptHeader)

	resp, err := client.Do(req)
	if err != nil {
		return Page{}, classify(ctx, fctx, host, err, false)
	}
	defer resp.Body.Close()
	page, err := readPage(ctx, fctx, host, resp)
	if err != nil {
		return Page{}, err
	}
	if err := ctx.Err(); err != nil {
		return Page{}, err
	}
	return page, nil
}

func readPage(ctx, fctx context.Context, host string, resp *http.Response) (Page, error) {
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		reason := "HTTP status"
		if resp.StatusCode >= 100 && resp.StatusCode <= 999 {
			reason = "HTTP " + strconv.Itoa(resp.StatusCode)
		}
		return Page{}, fetchError(host, ErrStatus, reason)
	}
	// Any coding beyond the transport's own gzip reaches us compressed.
	if ce, ok := unrequestedEncoding(resp.Header); ok {
		return Page{}, fetchError(host, ErrUnsupportedContent, withToken("unsupported content encoding", ce))
	}
	kind, charset, mediaType, known := contentKind(resp.Header.Get("Content-Type"))
	if known && kind == "" {
		return Page{}, fetchError(host, ErrUnsupportedContent, withToken("unsupported content type", mediaType))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return Page{}, classify(ctx, fctx, host, err, true)
	}
	cut := len(body) > maxBodyBytes
	if cut {
		body = body[:maxBodyBytes]
	}
	if !known {
		kind, _, mediaType, _ = contentKind(http.DetectContentType(body))
		charset = ""
		if kind == "" {
			return Page{}, fetchError(host, ErrUnsupportedContent, withToken("unsupported content type", mediaType))
		}
	}

	text, err := decodeBody(body, charset, kind == "html", cut)
	if err != nil {
		var ue *unsupportedEncoding
		errors.As(err, &ue)
		label := ""
		if ue != nil {
			label = ue.label
		}
		return Page{}, fetchError(host, ErrUnsupportedEncoding, withToken("unsupported encoding", label))
	}
	page := Page{Kind: kind}
	var truncated bool
	if kind == "html" {
		page.Title, page.Description, page.Text, truncated = extractHTML(text)
	} else {
		page.Text, truncated = extractPlain(text)
	}
	page.Truncated = cut || truncated
	return page, nil
}

func contentKind(value string) (kind, charset, mediaType string, known bool) {
	if strings.TrimSpace(value) == "" {
		return "", "", "", false
	}
	mt, params, err := mime.ParseMediaType(value)
	if err != nil && !errors.Is(err, mime.ErrInvalidMediaParameter) {
		return "", "", "", false
	}
	switch mt {
	case "text/html", "application/xhtml+xml":
		kind = "html"
	case "text/plain", "text/markdown":
		kind = "text"
	}
	return kind, params["charset"], mt, true
}

func unrequestedEncoding(h http.Header) (string, bool) {
	for _, v := range h.Values("Content-Encoding") {
		for _, c := range strings.Split(v, ",") {
			c = strings.TrimSpace(c)
			if c != "" && !strings.EqualFold(c, "identity") {
				return c, true
			}
		}
	}
	return "", false
}

func (f *Fetcher) transport() *http.Transport {
	d := net.Dialer{Timeout: dialTimeout}
	if f.dialer != nil {
		d = *f.dialer
	}
	d.ControlContext = nil
	d.Control = f.control
	return &http.Transport{
		Proxy:                  nil, // through a proxy, control would vet the proxy's address, not the page's
		DialContext:            f.dialContext(&d),
		TLSClientConfig:        &tls.Config{RootCAs: f.roots},
		TLSHandshakeTimeout:    tlsTimeout,
		ResponseHeaderTimeout:  headerTimeout,
		MaxResponseHeaderBytes: maxHeaderBytes,
		DisableKeepAlives:      true,
	}
}

// control applies the address policy after DNS and every redirect hop.
func (f *Fetcher) control(network, address string, _ syscall.RawConn) error {
	if f.AllowPrivate {
		return nil
	}
	public := f.public
	if public == nil {
		public = publicAddr
	}
	ap, err := netip.ParseAddrPort(address)
	if err != nil || !public(ap) {
		return ErrBlockedAddress
	}
	return nil
}

func (f *Fetcher) dialContext(d *net.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	if f.lookup == nil {
		return d.DialContext
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		if _, err := netip.ParseAddr(host); err == nil {
			return d.DialContext(ctx, network, addr)
		}
		addrs, err := f.lookup(ctx, host)
		if err != nil {
			return nil, err
		}
		if len(addrs) == 0 {
			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		}
		var first error
		for _, a := range addrs {
			conn, err := d.DialContext(ctx, network, net.JoinHostPort(a.String(), port))
			if err == nil {
				return conn, nil
			}
			if first == nil {
				first = err
			}
		}
		return nil, first
	}
}

// checkRedirect blocks userinfo, https -> http downgrades and excess hops.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > maxRedirects {
		return ErrTooManyRedirects
	}
	u := req.URL
	prev := via[len(via)-1].URL
	if u.Scheme != "http" && u.Scheme != "https" || u.User != nil || u.Hostname() == "" ||
		prev.Scheme == "https" && u.Scheme == "http" {
		return ErrRedirectRefused
	}
	req.Header.Del("Referer")
	u.Fragment, u.RawFragment = "", ""
	return nil
}

// redirectGuard rejects an unparsable Location before net/http quotes it.
type redirectGuard struct{ rt http.RoundTripper }

func (g redirectGuard) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := g.rt.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	switch resp.StatusCode {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		if loc := resp.Header.Get("Location"); loc != "" {
			if _, err := req.URL.Parse(loc); err != nil {
				resp.Body.Close()
				return nil, ErrRedirectRefused
			}
		}
	}
	return resp, nil
}

// classify never uses the inner error text: it can quote the URL.
func classify(parent, fctx context.Context, host string, err error, body bool) error {
	if perr := parent.Err(); perr != nil {
		return perr
	}
	var dnsErr *net.DNSError
	switch {
	case errors.Is(err, ErrBlockedAddress):
		return fetchError(host, ErrBlockedAddress, "")
	case errors.Is(err, ErrTooManyRedirects):
		return fetchError(host, ErrTooManyRedirects, "")
	case errors.Is(err, ErrRedirectRefused):
		return fetchError(host, ErrRedirectRefused, "")
	case fctx.Err() != nil, errors.Is(err, context.DeadlineExceeded), isTimeout(err):
		return fetchError(host, ErrTimeout, "")
	case body:
		return fetchError(host, ErrBodyRead, "")
	case errors.As(err, &dnsErr):
		return fetchError(host, ErrLookup, "")
	case isTLS(err):
		return fetchError(host, ErrTLS, "")
	case errors.Is(err, syscall.ECONNREFUSED):
		return fetchError(host, ErrConnect, "connection refused")
	}
	return fetchError(host, ErrConnect, "")
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func isTLS(err error) bool {
	var (
		verify    *tls.CertificateVerificationError
		record    tls.RecordHeaderError
		alert     tls.AlertError
		authority x509.UnknownAuthorityError
		hostname  x509.HostnameError
		invalid   x509.CertificateInvalidError
	)
	return errors.As(err, &verify) || errors.As(err, &record) || errors.As(err, &alert) ||
		errors.As(err, &authority) || errors.As(err, &hostname) || errors.As(err, &invalid)
}
