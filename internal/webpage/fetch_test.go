package webpage

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type testServer struct {
	*httptest.Server
	port uint16
	hits atomic.Int32
	mu   sync.Mutex
	last *http.Request
}

func newServer(t *testing.T, h http.HandlerFunc) *testServer {
	return startServer(t, h, false)
}

func newTLSServer(t *testing.T, h http.HandlerFunc) *testServer {
	return startServer(t, h, true)
}

func startServer(t *testing.T, h http.HandlerFunc, useTLS bool) *testServer {
	t.Helper()
	s := &testServer{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		s.mu.Lock()
		s.last = r.Clone(context.Background())
		s.mu.Unlock()
		h(w, r)
	})
	if useTLS {
		s.Server = httptest.NewTLSServer(handler)
	} else {
		s.Server = httptest.NewServer(handler)
	}
	t.Cleanup(s.Close)
	ap := netip.MustParseAddrPort(s.Listener.Addr().String())
	s.port = ap.Port()
	return s
}

func (s *testServer) at(host, rest string) string {
	return "http://" + host + ":" + strconv.Itoa(int(s.port)) + rest
}

func (s *testServer) lastRequest() *http.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

func (s *testServer) roots() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(s.Certificate())
	return pool
}

func lookupTest(_ context.Context, host string) ([]netip.Addr, error) {
	if strings.HasSuffix(host, ".test") {
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

func testFetcher(public ...*testServer) *Fetcher {
	ports := map[uint16]bool{}
	for _, s := range public {
		ports[s.port] = true
	}
	return &Fetcher{
		UserAgent: "nn/test",
		lookup:    lookupTest,
		timeout:   5 * time.Second,
		public: func(ap netip.AddrPort) bool {
			return ap.Addr().Unmap().IsLoopback() && ports[ap.Port()]
		},
	}
}

func blockUntilDone(t *testing.T) func(r *http.Request) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	return func(r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}
}

// "" sends no Content-Type at all.
func setContentType(w http.ResponseWriter, value string) {
	if value == "" {
		w.Header()["Content-Type"] = nil
		return
	}
	w.Header()["Content-Type"] = []string{value}
}

func htmlPage(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, body)
	}
}

func wantFetchError(t *testing.T, err error, sentinel error, reason string) *FetchError {
	t.Helper()
	var fe *FetchError
	if !errors.As(err, &fe) {
		t.Fatalf("err = %v (%T), want *FetchError", err, err)
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want %v", err, sentinel)
	}
	if reason != "" && fe.Reason != reason {
		t.Errorf("reason = %q, want %q", fe.Reason, reason)
	}
	if fe.Error() != "fetch "+fe.Host+": "+fe.Reason {
		t.Errorf("Error() = %q", fe.Error())
	}
	for _, leak := range []string{"TOKEN", "?", "#", "%", "Location"} {
		if strings.Contains(fe.Error(), leak) {
			t.Errorf("error text %q contains %q", fe.Error(), leak)
		}
	}
	if _, err := netip.ParseAddr(fe.Host); err != nil && strings.ContainsAny(fe.Host, ":/") || fe.Host != strings.ToLower(fe.Host) {
		t.Errorf("host %q has a port or path", fe.Host)
	}
	return fe
}

const secretRest = "/p-PTOKEN?q=QTOKEN#FTOKEN"

func TestFetchHTMLAndText(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page":
			htmlPage(`<html><head><title>Hello</title><meta name="description" content="Desc"></head>`+
				`<body><nav>menu</nav><p>Body text</p></body></html>`)(w, r)
		case "/notes.md":
			w.Header().Set("Content-Type", "text/markdown")
			fmt.Fprint(w, "# Notes\r\nline\r\n")
		}
	})
	f := testFetcher(srv)
	page, err := f.Fetch(context.Background(), mustParse(t, srv.at("page.test", "/page#frag")))
	if err != nil {
		t.Fatal(err)
	}
	if page != (Page{Title: "Hello", Description: "Desc", Text: "Body text", Kind: "html"}) {
		t.Errorf("page = %+v", page)
	}
	r := srv.lastRequest()
	if r.UserAgent() != "nn/test" || r.Header.Get("Accept") != acceptHeader {
		t.Errorf("headers UA %q Accept %q", r.UserAgent(), r.Header.Get("Accept"))
	}
	if r.RequestURI != "/page" || r.Host != srv.at("page.test", "")[len("http://"):] {
		t.Errorf("request URI %q host %q", r.RequestURI, r.Host)
	}
	if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Referer") != "" {
		t.Errorf("unexpected headers %v", r.Header)
	}

	page, err = f.Fetch(context.Background(), mustParse(t, srv.at("page.test", "/notes.md")))
	if err != nil {
		t.Fatal(err)
	}
	if page != (Page{Text: "# Notes\nline", Kind: "text"}) {
		t.Errorf("text page = %+v", page)
	}

	f.UserAgent = ""
	if _, err := f.Fetch(context.Background(), mustParse(t, srv.at("page.test", "/page"))); err != nil {
		t.Fatal(err)
	}
	if ua := srv.lastRequest().UserAgent(); ua != "nn" {
		t.Errorf("default UA = %q", ua)
	}
}

func TestFetchBlocksPrivateAddresses(t *testing.T) {
	srv := newServer(t, htmlPage("<p>local</p>"))
	port := strconv.Itoa(int(srv.port))
	links := []string{
		"http://127.0.0.1:" + port + secretRest,
		"http://localhost.test:" + port + secretRest,
		"http://[::1]:" + port + secretRest,
		"http://[::ffff:127.0.0.1]:" + port + secretRest,
	}
	for _, s := range links {
		f := &Fetcher{UserAgent: "nn/test", lookup: lookupTest, timeout: 5 * time.Second}
		_, err := f.Fetch(context.Background(), mustParse(t, s))
		wantFetchError(t, err, ErrBlockedAddress, "blocked address")
	}
	if n := srv.hits.Load(); n != 0 {
		t.Fatalf("blocked fetches reached the server %d times", n)
	}

	f := &Fetcher{UserAgent: "nn/test", AllowPrivate: true, lookup: lookupTest, timeout: 5 * time.Second}
	for _, s := range links[:2] {
		page, err := f.Fetch(context.Background(), mustParse(t, s))
		if err != nil || page.Text != "local" {
			t.Errorf("AllowPrivate fetch %s = %+v, %v", s, page, err)
		}
	}
}

func TestFetchRedirects(t *testing.T) {
	target := newServer(t, htmlPage("<p>target</p>"))
	hops := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/hop/"))
		end, _ := strconv.Atoi(r.URL.Query().Get("end"))
		if n < end {
			http.Redirect(w, r, fmt.Sprintf("/hop/%d?end=%d", n+1, end), http.StatusFound)
			return
		}
		htmlPage("<p>arrived</p>")(w, r)
	})
	f := testFetcher(hops, target)

	page, err := f.Fetch(context.Background(), mustParse(t, hops.at("a.test", "/hop/0?end=5")))
	if err != nil || page.Text != "arrived" {
		t.Fatalf("5 redirects = %+v, %v", page, err)
	}
	_, err = f.Fetch(context.Background(), mustParse(t, hops.at("a.test", "/hop/0?end=6#FTOKEN")))
	wantFetchError(t, err, ErrTooManyRedirects, "too many redirects")

	cases := []struct {
		name, location string
		sentinel       error
	}{
		{"scheme", "ftp://a.test/PTOKEN", ErrRedirectRefused},
		{"javascript", "javascript:alert(1)", ErrRedirectRefused},
		{"userinfo", "http://user:QTOKEN@t.test:" + strconv.Itoa(int(target.port)) + "/", ErrRedirectRefused},
		{"invalid location", "http://a.test/%zz?token=QTOKEN", ErrRedirectRefused},
		{"private target", "http://private.test:" + strconv.Itoa(int(target.port)) + "/PTOKEN", ErrBlockedAddress},
	}
	for _, c := range cases {
		loc := c.location
		src := newServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", loc)
			w.WriteHeader(http.StatusMovedPermanently)
		})
		before := target.hits.Load()
		strict := testFetcher(src)
		_, err := strict.Fetch(context.Background(), mustParse(t, src.at("a.test", secretRest)))
		fe := wantFetchError(t, err, c.sentinel, "")
		if fe.Host != "a.test" {
			t.Errorf("%s: host %q", c.name, fe.Host)
		}
		if target.hits.Load() != before {
			t.Errorf("%s: redirect target was reached", c.name)
		}
	}

	secure := newTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.at("t.test", "/"), http.StatusFound)
	})
	tf := testFetcher(secure, target)
	tf.roots = secure.roots()
	before := target.hits.Load()
	_, err = tf.Fetch(context.Background(), mustParse(t, "https://127.0.0.1:"+strconv.Itoa(int(secure.port))+secretRest))
	wantFetchError(t, err, ErrRedirectRefused, "redirect refused")
	if target.hits.Load() != before {
		t.Error("downgrade target was reached")
	}

	secureHTML := newTLSServer(t, htmlPage("<p>secure</p>"))
	up := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://127.0.0.1:"+strconv.Itoa(int(secureHTML.port))+"/", http.StatusFound)
	})
	uf := testFetcher(up, secureHTML)
	uf.roots = secureHTML.roots()
	page, err = uf.Fetch(context.Background(), mustParse(t, up.at("a.test", "/")))
	if err != nil || page.Text != "secure" {
		t.Errorf("upgrade = %+v, %v", page, err)
	}
}

func TestFetchRedirectCarriesNothing(t *testing.T) {
	second := newServer(t, htmlPage("<p>second</p>"))
	first := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: "CTOKEN"})
		w.Header().Set("Location", second.at("b.test", "/next?x=1#hop2"))
		w.WriteHeader(http.StatusTemporaryRedirect)
	})
	f := testFetcher(first, second)
	page, err := f.Fetch(context.Background(), mustParse(t, first.at("a.test", "/doc?token=QTOKEN#frag=FTOKEN")))
	if err != nil || page.Text != "second" {
		t.Fatalf("fetch = %+v, %v", page, err)
	}
	if uri := first.lastRequest().RequestURI; uri != "/doc?token=QTOKEN" {
		t.Errorf("first hop URI %q", uri)
	}
	r := second.lastRequest()
	if r.RequestURI != "/next?x=1" {
		t.Errorf("second hop URI %q", r.RequestURI)
	}
	for _, name := range []string{"Referer", "Cookie", "Authorization"} {
		if v := r.Header.Get(name); v != "" {
			t.Errorf("second hop %s = %q", name, v)
		}
	}
	for name, values := range r.Header {
		for _, v := range values {
			if strings.Contains(v, "TOKEN") {
				t.Errorf("second hop header %s carries %q", name, v)
			}
		}
	}
	if r.UserAgent() != "nn/test" {
		t.Errorf("second hop UA %q", r.UserAgent())
	}
}

func TestFetchDeadline(t *testing.T) {
	wait := blockUntilDone(t)
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/body" {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, "<p>partial")
			w.(http.Flusher).Flush()
		}
		wait(r)
	})
	for _, path := range []string{"/headers", "/body"} {
		f := testFetcher(srv)
		f.timeout = 200 * time.Millisecond
		start := time.Now()
		_, err := f.Fetch(context.Background(), mustParse(t, srv.at("slow.test", path+"?q=QTOKEN")))
		wantFetchError(t, err, ErrTimeout, "timeout")
		if d := time.Since(start); d > 3*time.Second {
			t.Errorf("%s: timeout took %v", path, d)
		}
	}

	f := testFetcher(srv)
	f.timeout = 0
	f.now = func() time.Time { return time.Now().Add(-FetchTimeout + 200*time.Millisecond) }
	start := time.Now()
	_, err := f.Fetch(context.Background(), mustParse(t, srv.at("slow.test", "/headers")))
	wantFetchError(t, err, ErrTimeout, "timeout")
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("clock seam deadline took %v", d)
	}
}

func TestFetchParentCancel(t *testing.T) {
	wait := blockUntilDone(t)
	started := make(chan struct{}, 4)
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/body" {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, "<p>partial")
			w.(http.Flusher).Flush()
		}
		started <- struct{}{}
		wait(r)
	})
	for _, path := range []string{"/headers", "/body"} {
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			<-started
			cancel()
		}()
		_, err := testFetcher(srv).Fetch(ctx, mustParse(t, srv.at("slow.test", path)))
		if err != context.Canceled {
			t.Errorf("%s: err = %v (%T), want context.Canceled unwrapped", path, err, err)
		}
		cancel()
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := srv.hits.Load()
	if _, err := testFetcher(srv).Fetch(ctx, mustParse(t, srv.at("slow.test", "/"))); err != context.Canceled {
		t.Errorf("cancelled before start: %v", err)
	}
	if srv.hits.Load() != before {
		t.Error("cancelled fetch reached the server")
	}
}

func TestFetchBodyCaps(t *testing.T) {
	big := "<p>HEAD</p><!--" + strings.Repeat("x", maxBodyBytes) + "--><p>TAIL</p>"
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write([]byte(big))
	zw.Close()
	var small bytes.Buffer
	zw = gzip.NewWriter(&small)
	zw.Write([]byte("<p>small gzip</p>"))
	zw.Close()
	var acceptEncoding atomic.Value
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		acceptEncoding.Store(r.Header.Get("Accept-Encoding"))
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/plain":
			fmt.Fprint(w, big)
		case "/gzip":
			w.Header().Set("Content-Encoding", "gzip")
			w.Write(gz.Bytes())
		case "/small":
			w.Header().Set("Content-Encoding", "gzip")
			w.Write(small.Bytes())
		}
	})
	f := testFetcher(srv)
	for _, path := range []string{"/plain", "/gzip"} {
		page, err := f.Fetch(context.Background(), mustParse(t, srv.at("big.test", path)))
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if !page.Truncated || page.Text != "HEAD" {
			t.Errorf("%s: truncated %v text %q", path, page.Truncated, page.Text)
		}
	}
	if gz.Len() > 1<<20 {
		t.Fatalf("gzip fixture is %d bytes, not a compression bomb", gz.Len())
	}
	page, err := f.Fetch(context.Background(), mustParse(t, srv.at("big.test", "/small")))
	if err != nil || page.Truncated || page.Text != "small gzip" {
		t.Errorf("small gzip = %+v, %v", page, err)
	}
	if ae, _ := acceptEncoding.Load().(string); ae != "gzip" {
		t.Errorf("Accept-Encoding = %q, want the transport's gzip", ae)
	}
}

func TestFetchRefusesBeforeReadingBody(t *testing.T) {
	wait := blockUntilDone(t)
	cases := []struct {
		name, contentType, encoding string
		sentinel                    error
		reason                      string
	}{
		{"brotli", "text/html", "br", ErrUnsupportedContent, "unsupported content encoding br"},
		{"gzip then br", "text/html", "gzip, br", ErrUnsupportedContent, "unsupported content encoding gzip"},
		{"unsafe encoding", "text/html", "x b\"z", ErrUnsupportedContent, "unsupported content encoding"},
		{"pdf", "application/pdf", "", ErrUnsupportedContent, "unsupported content type application/pdf"},
		{"json", "application/json; charset=utf-8", "", ErrUnsupportedContent, "unsupported content type application/json"},
		{"image", "image/png", "", ErrUnsupportedContent, "unsupported content type image/png"},
		{"long type", "application/x-" + strings.Repeat("a", 70), "", ErrUnsupportedContent, "unsupported content type"},
	}
	for _, c := range cases {
		c := c
		srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", c.contentType)
			if c.encoding != "" {
				w.Header().Set("Content-Encoding", c.encoding)
			}
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			wait(r)
		})
		f := testFetcher(srv)
		f.timeout = 3 * time.Second
		start := time.Now()
		_, err := f.Fetch(context.Background(), mustParse(t, srv.at("type.test", secretRest)))
		wantFetchError(t, err, c.sentinel, c.reason)
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("%s: refusal waited for the body (%v)", c.name, d)
		}
	}
}

func TestFetchContentTypes(t *testing.T) {
	wordKOI := "\xF0\xD2\xC9\xD7\xC5\xD4"
	word1251 := "\xCF\xF0\xE8\xE2\xE5\xF2"
	want := string([]rune{0x041F, 0x0440, 0x0438, 0x0432, 0x0435, 0x0442})
	cases := []struct {
		name, contentType, body string
		kind, text              string
	}{
		{"quoted charset", `text/html; charset="koi8-r"`, "<p>" + wordKOI, "html", want},
		{"invalid parameter ignored", "text/html; charset=utf-8; broken", `<meta charset="windows-1251"><p>` + word1251, "html", want},
		{"uppercase type", "TEXT/HTML", "<p>up</p>", "html", "up"},
		{"xhtml", "application/xhtml+xml", "<p>x</p>", "html", "x"},
		{"markdown", "text/markdown; charset=utf-8", "*md*", "text", "*md*"},
		{"plain koi8", "text/plain; charset=koi8-r", wordKOI, "text", want},
		{"missing sniffed html", "", `<!DOCTYPE html><meta charset="koi8-r"><p>` + wordKOI, "html", want},
		{"unparsable sniffed text", ";;;", "hello", "text", "hello"},
		{"identity encoding", "text/plain", "same", "text", "same"},
	}
	for _, c := range cases {
		c := c
		srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
			setContentType(w, c.contentType)
			if c.name == "identity encoding" {
				w.Header().Set("Content-Encoding", "identity")
			}
			fmt.Fprint(w, c.body)
		})
		page, err := testFetcher(srv).Fetch(context.Background(), mustParse(t, srv.at("type.test", "/")))
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if page.Kind != c.kind || page.Text != c.text {
			t.Errorf("%s: kind %q text %q, want %q %q", c.name, page.Kind, page.Text, c.kind, c.text)
		}
	}

	refused := []struct {
		name, contentType, body string
		sentinel                error
		reason                  string
	}{
		{"sniffed pdf", "", "%PDF-1.7\n%\xE2\xE3\xCF\xD3\n", ErrUnsupportedContent, "unsupported content type application/pdf"},
		{"sniffed binary", "", "\x00\x01\x02\x03", ErrUnsupportedContent, "unsupported content type application/octet-stream"},
		{"unknown charset", "text/html; charset=shift_jis", "<p>\x82\xA0", ErrUnsupportedEncoding, "unsupported encoding shift_jis"},
		{"unsafe charset", `text/html; charset="bad label!"`, "<p>\x82\xA0", ErrUnsupportedEncoding, "unsupported encoding"},
		{"undeclared legacy bytes", "text/html", "<p>" + word1251, ErrUnsupportedEncoding, "unsupported encoding"},
		{"utf-16 bom", "text/plain", "\xFF\xFEh\x00i\x00", ErrUnsupportedEncoding, "unsupported encoding utf-16le"},
	}
	for _, c := range refused {
		c := c
		srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
			setContentType(w, c.contentType)
			fmt.Fprint(w, c.body)
		})
		_, err := testFetcher(srv).Fetch(context.Background(), mustParse(t, srv.at("type.test", secretRest)))
		wantFetchError(t, err, c.sentinel, c.reason)
	}
}

func TestFetchStatus(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		code, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/"))
		w.WriteHeader(code)
		fmt.Fprint(w, "<p>error page</p>")
	})
	for _, code := range []int{302, 403, 404, 410, 500, 503} {
		_, err := testFetcher(srv).Fetch(context.Background(), mustParse(t, srv.at("status.test", "/"+strconv.Itoa(code)+"?q=QTOKEN")))
		wantFetchError(t, err, ErrStatus, "HTTP "+strconv.Itoa(code))
	}
	page, err := testFetcher(srv).Fetch(context.Background(), mustParse(t, srv.at("status.test", "/204")))
	if err != nil || page.Text != "" {
		t.Errorf("204 = %+v, %v", page, err)
	}
}

func TestFetchNetworkErrors(t *testing.T) {
	_, err := testFetcher().Fetch(context.Background(), mustParse(t, "http://nowhere.invalid"+secretRest))
	fe := wantFetchError(t, err, ErrLookup, "DNS lookup failed")
	if fe.Host != "nowhere.invalid" {
		t.Errorf("host %q", fe.Host)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := &testServer{port: netip.MustParseAddrPort(ln.Addr().String()).Port()}
	ln.Close()
	_, err = testFetcher(closed).Fetch(context.Background(), mustParse(t, closed.at("closed.test", secretRest)))
	wantFetchError(t, err, ErrConnect, "connection refused")

	secure := newTLSServer(t, htmlPage("<p>x</p>"))
	link := "https://127.0.0.1:" + strconv.Itoa(int(secure.port)) + secretRest
	_, err = testFetcher(secure).Fetch(context.Background(), mustParse(t, link))
	wantFetchError(t, err, ErrTLS, "TLS verification failed")

	// net/http reports an HTTP answer to a TLS hello as a plain error.
	plain := newServer(t, htmlPage("<p>x</p>"))
	_, err = testFetcher(plain).Fetch(context.Background(), mustParse(t, "https://127.0.0.1:"+strconv.Itoa(int(plain.port))+secretRest))
	wantFetchError(t, err, ErrConnect, "connection failed")

	// A certificate for 127.0.0.1 is not valid for a .test name.
	named := testFetcher(secure)
	named.roots = secure.roots()
	_, err = named.Fetch(context.Background(), mustParse(t, "https://named.test:"+strconv.Itoa(int(secure.port))+secretRest))
	wantFetchError(t, err, ErrTLS, "TLS verification failed")
	named.roots = secure.roots()
	if page, err := named.Fetch(context.Background(), mustParse(t, link)); err != nil || page.Text != "x" {
		t.Errorf("trusted TLS = %+v, %v", page, err)
	}
}

func TestFetchIgnoresProxyEnvironment(t *testing.T) {
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy", "ALL_PROXY", "all_proxy"} {
		t.Setenv(name, "http://127.0.0.1:1")
	}
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")
	srv := newServer(t, htmlPage("<p>direct</p>"))
	page, err := testFetcher(srv).Fetch(context.Background(), mustParse(t, srv.at("proxy.test", "/")))
	if err != nil || page.Text != "direct" {
		t.Errorf("fetch with proxy environment = %+v, %v", page, err)
	}
	if tr := (&Fetcher{}).transport(); tr.Proxy != nil || !tr.DisableKeepAlives || tr.MaxResponseHeaderBytes != maxHeaderBytes {
		t.Errorf("transport proxy %v keepalives off %v header cap %d", tr.Proxy != nil, tr.DisableKeepAlives, tr.MaxResponseHeaderBytes)
	}
}

func TestFetchRejectsUnparsedLink(t *testing.T) {
	var fe *FetchError
	_, err := (&Fetcher{}).Fetch(context.Background(), Link{Raw: "http://x.test/"})
	if err == nil || errors.As(err, &fe) {
		t.Errorf("zero link: %v", err)
	}
}
