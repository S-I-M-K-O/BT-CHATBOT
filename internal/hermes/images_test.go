package hermes

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestIsPublicAddr(t *testing.T) {
	cases := map[string]bool{
		"8.8.8.8":              true,
		"151.101.1.69":         true,
		"2606:4700::1111":      true,
		"127.0.0.1":            false,
		"::1":                  false,
		"10.1.2.3":             false,
		"172.16.0.1":           false,
		"192.168.1.1":          false,
		"169.254.169.254":      false, // cloud metadata
		"100.64.0.1":           false, // CGNAT
		"0.0.0.0":              false,
		"0.1.2.3":              false,
		"::":                   false,
		"224.0.0.1":            false,
		"255.255.255.255":      false,
		"fc00::1":              false,
		"fd12:3456::1":         false,
		"fe80::1":              false,
		"fe80::1%en0":          false,
		"ff02::1":              false,
		"::ffff:127.0.0.1":     false, // v4-mapped loopback
		"::ffff:192.168.0.1":   false,
		"64:ff9b::a00:1":       false, // NAT64 of 10.0.0.1
		"64:ff9b::808:808":     true,  // NAT64 of 8.8.8.8
		"198.18.0.1":           false,
		"192.0.0.8":            false,
		"::ffff:93.184.216.34": true,
		"64:ff9b:1::a00:1":     false, // local-use NAT64
		"2002:c0a8:101::1":     false, // 6to4 of 192.168.1.1
		"2002:7f00:1::1":       false, // 6to4 of 127.0.0.1
		"2002:808:808::1":      true,  // 6to4 of 8.8.8.8
		"::a00:1":              false, // IPv4-compatible 10.0.0.1
		"2001:0:4136:e378::1":  false, // Teredo
		"2001:db8::1":          false, // documentation
		"fec0::1":              false, // deprecated site-local, still routed on some LANs
		"::ffff:0:a00:1":       false, // SIIT IPv4-translated 10.0.0.1
	}
	for s, want := range cases {
		if got := isPublicAddr(netip.MustParseAddr(s)); got != want {
			t.Errorf("isPublicAddr(%s) = %v, want %v", s, got, want)
		}
	}
}

// TestImageClientRefusesLoopback: the production client must not connect to
// a local service at all, so the handler is never reached.
func TestImageClientRefusesLoopback(t *testing.T) {
	var hit atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit.Store(true)
	}))
	defer srv.Close()

	if _, err := httpGetImage(context.Background(), imageHTTP, srv.URL+"/img.jpg", 5*time.Second); err == nil {
		t.Error("expected loopback download to be refused")
	}
	if hit.Load() {
		t.Error("loopback server was contacted")
	}
}

// allowOnly returns a dial check that lets through only srv (standing in for
// a public host) and applies the real policy to everything else.
func allowOnly(srv *httptest.Server) func(string) error {
	host := strings.TrimPrefix(srv.URL, "http://")
	return func(address string) error {
		if address == host {
			return nil
		}
		return checkPublicDial(address)
	}
}

// TestImageClientBlocksRedirectToInternal reproduces the audit's blind SSRF:
// a "public" image host answers 302 to an internal URL, which must not be
// requested.
func TestImageClientBlocksRedirectToInternal(t *testing.T) {
	var internalHit atomic.Bool
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		internalHit.Store(true)
	}))
	defer internal.Close()
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, internal.URL+"/admin/reboot", http.StatusFound)
	}))
	defer evil.Close()

	client := newImageClient(allowOnly(evil))
	if _, err := httpGetImage(context.Background(), client, evil.URL+"/img.jpg", 5*time.Second); err == nil {
		t.Error("expected redirect to internal host to fail")
	}
	if internalHit.Load() {
		t.Error("internal server was reached through a redirect")
	}
}

func TestImageClientRedirectLimits(t *testing.T) {
	var hops atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/loop":
			hops.Add(1)
			http.Redirect(w, r, srv.URL+"/loop", http.StatusFound)
		case "/file":
			http.Redirect(w, r, "file:///etc/passwd", http.StatusFound)
		case "/ok":
			http.Redirect(w, r, srv.URL+"/img", http.StatusFound)
		case "/img":
			w.Write([]byte{0xFF, 0xD8, 0xFF, 0xE0})
		}
	}))
	defer srv.Close()
	client := newImageClient(allowOnly(srv))
	ctx := context.Background()

	if _, err := httpGetImage(ctx, client, srv.URL+"/loop", 5*time.Second); err == nil {
		t.Error("expected endless redirects to fail")
	}
	if n := hops.Load(); n > imageMaxRedirects+1 {
		t.Errorf("followed %d hops, want at most %d", n, imageMaxRedirects+1)
	}
	if _, err := httpGetImage(ctx, client, srv.URL+"/file", 5*time.Second); err == nil {
		t.Error("expected redirect to file:// to fail")
	}
	if _, err := httpGetImage(ctx, client, srv.URL+"/ok", 5*time.Second); err != nil {
		t.Errorf("a short redirect to a public host should work: %v", err)
	}
}

func TestHTTPGetImageRejectsBadInput(t *testing.T) {
	big := bytes.Repeat([]byte("x"), imageSearchMaxBytes+1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(big)
	}))
	defer srv.Close()
	ctx := context.Background()

	if _, err := httpGetImage(ctx, newImageClient(allowOnly(srv)), srv.URL, 5*time.Second); err == nil {
		t.Error("expected oversized search download to fail")
	}
	for _, u := range []string{"file:///etc/passwd", "ftp://example.com/x.jpg", "gopher://example.com/"} {
		if _, err := httpGetImage(ctx, imageHTTP, u, time.Second); err == nil {
			t.Errorf("expected %s to be refused", u)
		}
	}
}

// TestImageSearchPrefersThumbnail: the full-size original is only fetched
// when Openverse's small thumbnail fails, keeping GEN_DIR small.
func TestImageSearchPrefersThumbnail(t *testing.T) {
	gen := t.TempDir()
	t.Setenv("GEN_DIR", gen)
	defer func() {
		files, _ := filepath.Glob(filepath.Join(gen, "search_*"))
		if len(files) == 0 {
			t.Error("no search image stored")
		}
		for _, f := range files {
			if fi, err := os.Stat(f); err != nil {
				t.Error(err)
			} else if fi.Mode().Perm() != 0o600 {
				t.Errorf("%s: want mode 0600, got %v", f, fi.Mode().Perm())
			}
		}
	}()
	var fetched []string
	getBytes := func(u string, d time.Duration) ([]byte, error) {
		fetched = append(fetched, u)
		return fakeImageBytes(u, d)
	}
	rec := httptest.NewRecorder()
	handleImageSearchWith(rec, defaultSettings(), "show me a picture of a lion",
		fakeOpenverseResults(t, 2), getBytes)

	if len(fetched) != 2 {
		t.Fatalf("expected one download per result, got %v", fetched)
	}
	for _, u := range fetched {
		if !strings.Contains(u, "thumb") {
			t.Errorf("fetched original %s although the thumbnail works", u)
		}
	}
}

// TestImageSearchFallsBackToOriginal: a failing thumbnail, or one that is
// not an image (e.g. an HTML error page), falls back to the source url.
func TestImageSearchFallsBackToOriginal(t *testing.T) {
	for name, thumb := range map[string]func() ([]byte, error){
		"error":    func() ([]byte, error) { return nil, os.ErrPermission },
		"nonimage": func() ([]byte, error) { return []byte("<html>oops</html>"), nil },
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("GEN_DIR", t.TempDir())
			getBytes := func(u string, d time.Duration) ([]byte, error) {
				if strings.Contains(u, "thumb") {
					return thumb()
				}
				return fakeImageBytes(u, d)
			}
			rec := httptest.NewRecorder()
			handleImageSearchWith(rec, defaultSettings(), "show me a picture of a lion",
				fakeOpenverseResults(t, 2), getBytes)
			if got := strings.Count(rec.Body.String(), `"image":"/gen/`); got != 2 {
				t.Errorf("expected 2 images via the original url, got %d", got)
			}
		})
	}
}

// TestEnforceGenBudgetBytes: a few huge pictures must be evicted even though
// the file count is well under the limit.
func TestEnforceGenBudgetBytes(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-time.Hour)
	write := func(name string, size int, age int) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
		mt := base.Add(time.Duration(age) * time.Minute)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	write("a.jpg", 400, 1) // oldest
	write("b.png", 400, 2)
	write("c.jpg", 400, 3)
	write("d.webp", 100, 4) // newest
	write("notes.txt", 5000, 0)

	enforceGenBudgetIn(dir, 10, 800)

	for name, want := range map[string]bool{
		"a.jpg": false, "b.png": false, "c.jpg": true, "d.webp": true, "notes.txt": true,
	} {
		_, err := os.Stat(filepath.Join(dir, name))
		if got := err == nil; got != want {
			t.Errorf("%s kept = %v, want %v", name, got, want)
		}
	}

	// The count limit still applies on its own.
	enforceGenBudgetIn(dir, 1, 1<<30)
	if _, err := os.Stat(filepath.Join(dir, "c.jpg")); err == nil {
		t.Error("c.jpg should be evicted by the count limit")
	}
	if _, err := os.Stat(filepath.Join(dir, "d.webp")); err != nil {
		t.Error("newest file should survive the count limit")
	}
}
