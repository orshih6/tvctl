// Package youtube searches YouTube the way its own website does (the
// "InnerTube" search call): no API key, no account. It only finds videos;
// playing one is the TV's job.
package youtube

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"
)

type Video struct {
	ID      string
	Title   string
	Channel string
	Length  string // "12:34"; "LIVE" for a live stream
}

// Endpoint is the search call; tests point it at a fake.
var Endpoint = "https://www.youtube.com/youtubei/v1/search?prettyPrint=false"

var client = &http.Client{Timeout: 20 * time.Second}

// Search returns up to n videos for query, in YouTube's own order.
func Search(ctx context.Context, query string, n int) ([]Video, error) {
	body, _ := json.Marshal(map[string]any{
		"context": map[string]any{"client": map[string]string{"clientName": "WEB", "clientVersion": "2.20240101.00.00", "hl": "en"}},
		"query":   query,
		"params":  "EgIQAQ==", // videos only: no channels, playlists or Shorts shelves
	})
	req, err := http.NewRequestWithContext(ctx, "POST", Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("youtube search: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("youtube search: HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("youtube search: %w", err)
	}
	return parse(raw, n)
}

var idRe = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

// parse finds every "videoRenderer" wherever it sits: YouTube moves the
// surrounding layout around, the video entry itself has been stable.
func parse(raw []byte, n int) ([]Video, error) {
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("youtube search: unreadable answer: %w", err)
	}
	var out []Video
	seen := map[string]bool{}
	var walk func(v any)
	walk = func(v any) {
		if len(out) >= n {
			return
		}
		switch x := v.(type) {
		case []any:
			for _, e := range x {
				walk(e)
			}
		case map[string]any:
			if r, ok := x["videoRenderer"].(map[string]any); ok {
				id, _ := r["videoId"].(string)
				title := text(r["title"])
				if idRe.MatchString(id) && title != "" && !seen[id] {
					seen[id] = true
					length := text(r["lengthText"])
					if length == "" {
						length = "LIVE"
					}
					out = append(out, Video{ID: id, Title: title, Channel: text(r["ownerText"]), Length: length})
				}
				return
			}
			for _, e := range x {
				walk(e)
			}
		}
	}
	// Results are in order inside arrays, but Go maps are not ordered, so walk
	// the known path first and fall back to the whole document.
	walk(dig(doc, "contents", "twoColumnSearchResultsRenderer", "primaryContents", "sectionListRenderer", "contents"))
	if len(out) == 0 {
		walk(doc)
	}
	return out, nil
}

func dig(v any, keys ...string) any {
	for _, k := range keys {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[k]
	}
	return v
}

// text reads YouTube's two text shapes: {"simpleText": …} and {"runs": [{"text": …}]}.
func text(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	if s, ok := m["simpleText"].(string); ok {
		return s
	}
	var s string
	runs, _ := m["runs"].([]any)
	for _, r := range runs {
		if rm, ok := r.(map[string]any); ok {
			t, _ := rm["text"].(string)
			s += t
		}
	}
	return s
}
