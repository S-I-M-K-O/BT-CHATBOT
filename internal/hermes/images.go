package hermes

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// This file holds picture search (Openverse, no key needed), the cached
// Mistral image agent and the size budget for the generated-images folder.

var openverseSearchURL = "https://api.openverse.org/v1/images/"

type openverseResult struct {
	Results []struct {
		URL       string `json:"url"`
		Thumbnail string `json:"thumbnail"`
		Title     string `json:"title"`
	} `json:"results"`
}

func handleImageSearch(ctx context.Context, w http.ResponseWriter, s *Settings, q string) {
	if !acquireImageJob(ctx) {
		return
	}
	defer releaseImageJob()
	handleImageSearchWith(w, s, q,
		func(u string, out any) error { return httpGetJSON(ctx, u, out) },
		func(u string, d time.Duration) ([]byte, error) { return httpGetImage(ctx, imageHTTP, u, d) })
}

// handleImageSearchWith is handleImageSearch with the network access injected
// so tests can run the full SSE picture flow offline.
func handleImageSearchWith(w http.ResponseWriter, s *Settings, q string, getJSON func(string, any) error, getBytes func(string, time.Duration) ([]byte, error)) {
	sse := newSSE(w)
	sse.event(map[string]any{"intent": "image"})

	p := phrasesFor(s.PiperVoice)

	subject := extractImageSubject(q)
	if subject == "" {
		sse.reply(p.searchNoSubject)
		return
	}
	u := openverseSearchURL + "?page_size=8&q=" + url.QueryEscape(subject)
	var res openverseResult
	if err := getJSON(u, &res); err != nil || len(res.Results) == 0 {
		if err != nil {
			log.Printf("image search error: %v", err)
		}
		sse.reply(p.searchEmpty)
		return
	}
	dir := genDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		sse.fail(p.searchStore)
		return
	}
	var urls []string
	for i, r := range res.Results {
		if len(urls) >= 3 {
			break
		}
		// Openverse's thumbnail is a small proxied copy; the source "url" is
		// often a 5-25 MB original on a flaky third-party host, so it is
		// only the fallback.
		data, ext, err := fetchFirst(getBytes, r.Thumbnail, r.URL)
		if err != nil {
			continue
		}
		name := fmt.Sprintf("search_%d_%d%s", time.Now().UnixNano(), i, ext)
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			continue
		}
		urls = append(urls, "/gen/"+name)
	}
	if len(urls) == 0 {
		sse.fail(p.searchDownload)
		return
	}
	enforceGenBudget()
	sse.text(fmt.Sprintf(p.searchFound, len(urls), subject))
	for _, u := range urls {
		sse.event(map[string]any{"image": u})
	}
	sse.done()
}

// imageAgent caches the Mistral agent used for image generation.
var imageAgent struct {
	sync.Mutex
	id string
}

func ensureImageAgent(ctx context.Context) (string, error) {
	imageAgent.Lock()
	defer imageAgent.Unlock()
	if imageAgent.id != "" {
		return imageAgent.id, nil
	}
	var agent struct {
		ID string `json:"id"`
	}
	err := mistralPost(ctx, "/v1/agents", map[string]any{
		"model":        mistralModel(),
		"name":         "HERMES Image Generator",
		"description":  "Generates images for the HERMES voice assistant.",
		"instructions": "You generate images on demand. Use the image_generation tool whenever the user asks to see an image or picture. Keep any spoken reply to one short sentence.",
		"tools":        []any{map[string]any{"type": "image_generation"}},
	}, &agent)
	if err != nil {
		return "", err
	}
	if agent.ID == "" {
		return "", fmt.Errorf("empty agent id from Mistral")
	}
	imageAgent.id = agent.ID
	log.Printf("created Mistral image agent %s", agent.ID)
	return agent.ID, nil
}

// forgetImageAgent drops the cached agent if it is still id, so the next
// request creates a fresh one.
func forgetImageAgent(id string) {
	imageAgent.Lock()
	defer imageAgent.Unlock()
	if imageAgent.id == id {
		imageAgent.id = ""
	}
}

// genBudgetFiles and genBudgetBytes cap what stays in GEN_DIR; the oldest
// pictures are deleted so the folder cannot grow without bound. Both are
// needed: 200 full-size originals can still add up to gigabytes.
const (
	genBudgetFiles = 200
	genBudgetBytes = 500 << 20 // 500 MB
)

var genBudgetMu sync.Mutex

// enforceGenBudget removes the oldest generated/searched images until at most
// genBudgetFiles remain and they total at most genBudgetBytes.
func enforceGenBudget() {
	enforceGenBudgetIn(genDir(), genBudgetFiles, genBudgetBytes)
}

func enforceGenBudgetIn(dir string, maxFiles int, maxBytes int64) {
	genBudgetMu.Lock()
	defer genBudgetMu.Unlock()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type file struct {
		name string
		mod  time.Time
		size int64
	}
	var files []file
	var total int64
	for _, e := range entries {
		if e.IsDir() || imageExtFromName(e.Name()) == "" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, file{e.Name(), info.ModTime(), info.Size()})
		total += info.Size()
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	for n := len(files); len(files) > 0 && (n > maxFiles || total > maxBytes); n-- {
		f := files[0]
		files = files[1:]
		if err := os.Remove(filepath.Join(dir, f.name)); err != nil {
			log.Printf("gen budget: %v", err)
		}
		// Count it as gone even on error so one stuck file cannot make
		// the loop delete everything newer.
		total -= f.size
	}
}

func imageExtFromName(name string) string {
	switch ext := strings.ToLower(filepath.Ext(name)); ext {
	case ".jpg", ".png", ".gif", ".webp":
		return ext
	}
	return ""
}

// fetchFirst downloads the first of candidates that is non-empty, loads and
// is a recognised image, returning it with its extension. A thumbnail that
// answers with an HTML error page still falls back to the original.
func fetchFirst(getBytes func(string, time.Duration) ([]byte, error), candidates ...string) ([]byte, string, error) {
	err := errors.New("no image url")
	for _, u := range candidates {
		if u == "" {
			continue
		}
		data, gerr := getBytes(u, 15*time.Second)
		if gerr != nil {
			err = gerr
			log.Printf("image search download failed for %s: %v", u, err)
			continue
		}
		if ext := imageExt(data); ext != "" {
			return data, ext, nil
		}
		err = errors.New("not an image")
		log.Printf("image search: skipping non-image result from %s", u)
	}
	return nil, "", err
}

// imageSearchMaxBytes caps a single search download. It is well below
// maxDownloadBytes because search results are only shown in the chat.
const imageSearchMaxBytes = 8 << 20 // 8 MB

// imageMaxRedirects caps redirect hops for search downloads.
const imageMaxRedirects = 3

// Search result URLs come from Openverse's index of third-party sites, so
// they (or a redirect they answer with) may point at the LAN, the Docker host
// or a cloud metadata endpoint. imageHTTP only connects to public addresses.
var imageHTTP = newImageClient(checkPublicDial)

// newImageClient builds a client whose every connection, including those for
// redirects, passes check on the resolved ip:port before connecting, so
// hostnames that resolve (or re-resolve) to internal addresses are refused.
func newImageClient(check func(address string) error) *http.Client {
	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error { return check(address) },
	}
	return &http.Client{
		Transport: &http.Transport{
			// No proxy: through one the dial check would only see the proxy,
			// and the proxy would reach whatever host the URL names.
			Proxy:                 nil,
			DialContext:           dialer.DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          10,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 15 * time.Second,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= imageMaxRedirects {
				return errors.New("too many redirects")
			}
			return checkImageScheme(req.URL)
		},
	}
}

func checkImageScheme(u *url.URL) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("refusing %q url", u.Scheme)
	}
	return nil
}

// httpGetImage fetches a search result with client, capped at
// imageSearchMaxBytes and timeout.
func httpGetImage(ctx context.Context, client *http.Client, u string, timeout time.Duration) ([]byte, error) {
	parsed, err := url.Parse(u)
	if err != nil {
		return nil, err
	}
	if err := checkImageScheme(parsed); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, imageSearchMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > imageSearchMaxBytes {
		return nil, fmt.Errorf("response exceeds %d bytes", imageSearchMaxBytes)
	}
	return data, nil
}

// checkPublicDial is the dial-time guard for imageHTTP: address is the
// already-resolved ip:port about to be connected to.
func checkPublicDial(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return err
	}
	if !isPublicAddr(ip) {
		return fmt.Errorf("refusing to connect to non-public address %s", ip)
	}
	return nil
}

// nonPublicPrefixes are ranges netip's Is* helpers do not cover.
var nonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),       // "this network"
	netip.MustParsePrefix("100.64.0.0/10"),   // carrier-grade NAT
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments
	netip.MustParsePrefix("198.18.0.0/15"),   // benchmarking
	netip.MustParsePrefix("240.0.0.0/4"),     // reserved, incl. broadcast
	netip.MustParsePrefix("64:ff9b:1::/48"),  // local-use NAT64 (RFC 8215)
	netip.MustParsePrefix("::/96"),           // deprecated IPv4-compatible (::10.0.0.1)
	netip.MustParsePrefix("2001::/32"),       // Teredo: the IPv4 inside is obfuscated
	netip.MustParsePrefix("2001:db8::/32"),   // documentation
	netip.MustParsePrefix("fec0::/10"),       // deprecated site-local (RFC 3879), still routed on some LANs
	netip.MustParsePrefix("::ffff:0:0:0/96"), // SIIT IPv4-translated (RFC 2765): carries an IPv4 address
}

// sixToFourPrefix is 6to4 (RFC 3056): bytes 2-5 are an IPv4 address that a
// 6to4 relay on the host's network would tunnel to, so it is checked too.
var sixToFourPrefix = netip.MustParsePrefix("2002::/16")

// nat64Prefix is the well-known NAT64 prefix; its last 4 bytes are an IPv4
// address that must be checked like any other.
var nat64Prefix = netip.MustParsePrefix("64:ff9b::/96")

// isPublicAddr reports whether ip is a globally routable unicast address, i.e.
// not loopback, private (RFC 1918, fc00::/7), link-local, CGNAT, unspecified,
// multicast or otherwise reserved. IPv4 addresses embedded in NAT64 and 6to4
// addresses are checked as IPv4.
func isPublicAddr(ip netip.Addr) bool {
	ip = ip.WithZone("").Unmap()
	if ip.Is6() && nat64Prefix.Contains(ip) {
		b := ip.As16()
		ip = netip.AddrFrom4([4]byte(b[12:]))
	} else if ip.Is6() && sixToFourPrefix.Contains(ip) {
		b := ip.As16()
		ip = netip.AddrFrom4([4]byte(b[2:6]))
	}
	if !ip.IsValid() || ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return false
	}
	for _, p := range nonPublicPrefixes {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}
