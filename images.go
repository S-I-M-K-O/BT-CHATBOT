package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
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
		func(u string, d time.Duration) ([]byte, error) { return httpGetWithTimeout(ctx, u, d) })
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
	if err := os.MkdirAll(dir, 0o755); err != nil {
		sse.fail(p.searchStore)
		return
	}
	var urls []string
	for i, r := range res.Results {
		if len(urls) >= 3 {
			break
		}
		data, err := getBytes(r.URL, 15*time.Second)
		if err != nil {
			// Source hosts (Flickr etc.) are flaky; fall back to Openverse's
			// own thumbnail CDN.
			log.Printf("image search download failed for %s: %v", r.URL, err)
			if r.Thumbnail == "" {
				continue
			}
			data, err = getBytes(r.Thumbnail, 15*time.Second)
			if err != nil {
				log.Printf("image search thumbnail fallback failed: %v", err)
				continue
			}
		}
		ext := imageExt(data)
		if ext == "" {
			log.Printf("image search: skipping non-image result from %s", r.URL)
			continue
		}
		name := fmt.Sprintf("search_%d_%d%s", time.Now().UnixNano(), i, ext)
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
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

// genBudgetFiles caps how many pictures stay in GEN_DIR; older ones are
// deleted so the folder cannot grow without bound.
const genBudgetFiles = 200

var genBudgetMu sync.Mutex

// enforceGenBudget removes the oldest generated/searched images beyond
// genBudgetFiles.
func enforceGenBudget() {
	genBudgetMu.Lock()
	defer genBudgetMu.Unlock()
	dir := genDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type file struct {
		name string
		mod  time.Time
	}
	var files []file
	for _, e := range entries {
		if e.IsDir() || imageExtFromName(e.Name()) == "" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, file{e.Name(), info.ModTime()})
	}
	if len(files) <= genBudgetFiles {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	for _, f := range files[:len(files)-genBudgetFiles] {
		if err := os.Remove(filepath.Join(dir, f.name)); err != nil {
			log.Printf("gen budget: %v", err)
		}
	}
}

func imageExtFromName(name string) string {
	switch ext := strings.ToLower(filepath.Ext(name)); ext {
	case ".jpg", ".png", ".gif", ".webp":
		return ext
	}
	return ""
}
