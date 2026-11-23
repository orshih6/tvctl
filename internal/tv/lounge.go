package tv

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Playing a video in a YouTube app that is already open.
//
// A DIAL launch ("v=<id>") is only read when the app starts: sent to a running
// YouTube the TV answers 200 and keeps showing what it was showing. A running
// app is reached the way the YouTube phone app casts to it instead: its DIAL
// description carries a screen id, YouTube trades that for a "lounge" token,
// and a setPlaylist command on that channel plays the video. No account.

// LoungeBase is YouTube's cast service; tests point it at a fake.
var LoungeBase = "https://www.youtube.com/api/lounge"

var loungeHTTP = &http.Client{Timeout: 15 * time.Second}

var screenIDRe = regexp.MustCompile(`<screenId>([A-Za-z0-9_-]+)</screenId>`)

// screenID reads the DIAL description of the YouTube app: its screen id when
// the app is running, "" when it is not (or says nothing useful).
func screenID(dial string) string {
	if !strings.Contains(dial, "<state>running</state>") {
		return ""
	}
	if m := screenIDRe.FindStringSubmatch(dial); m != nil {
		return m[1]
	}
	return ""
}

func (t *TV) youtubeScreen(ctx context.Context) string {
	req, err := http.NewRequestWithContext(ctx, "GET", "http://"+t.Host+":8080/ws/apps/YouTube", nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Origin", "package:com.google.android.youtube")
	resp, err := t.http.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return screenID(string(b))
}

var (
	loungeSID = regexp.MustCompile(`\["c","([^"]+)"`)
	loungeGS  = regexp.MustCompile(`\["S","([^"]+)"`)
)

// session pulls the two session ids out of the answer to a bind.
func session(bind string) (sid, gsession string, ok bool) {
	a, b := loungeSID.FindStringSubmatch(bind), loungeGS.FindStringSubmatch(bind)
	if a == nil || b == nil {
		return "", "", false
	}
	return a[1], b[1], true
}

// cast plays videoID in the running YouTube app with this screen id.
func (t *TV) cast(ctx context.Context, screen, videoID string) error {
	post := func(u string, token string, form url.Values) (string, error) {
		req, err := http.NewRequestWithContext(ctx, "POST", u, strings.NewReader(form.Encode()))
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "https://www.youtube.com/")
		if token != "" {
			req.Header.Set("X-YouTube-LoungeId-Token", token)
		}
		resp, err := loungeHTTP.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
		if resp.StatusCode != 200 {
			return "", fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		return string(b), nil
	}

	out, err := post(LoungeBase+"/pairing/get_lounge_token_batch", "", url.Values{"screen_ids": {screen}})
	if err != nil {
		return fmt.Errorf("youtube cast token: %w", err)
	}
	var tok struct {
		Screens []struct {
			LoungeToken string `json:"loungeToken"`
		} `json:"screens"`
	}
	if json.Unmarshal([]byte(out), &tok) != nil || len(tok.Screens) == 0 || tok.Screens[0].LoungeToken == "" {
		return errors.New("youtube cast token: none given")
	}
	token := tok.Screens[0].LoungeToken

	id := make([]byte, 13)
	_, _ = rand.Read(id)
	out, err = post(LoungeBase+"/bc/bind?RID=0&VER=8&CVER=1", token, url.Values{
		"device": {"REMOTE_CONTROL"}, "id": {hex.EncodeToString(id)}, "name": {t.Name},
		"mdx-version": {"3"}, "pairing_type": {"cast"}, "app": {"android-phone-13.14.55"},
	})
	if err != nil {
		return fmt.Errorf("youtube cast connect: %w", err)
	}
	sid, gs, ok := session(out)
	if !ok {
		return errors.New("youtube cast connect: no session")
	}
	q := url.Values{"SID": {sid}, "gsessionid": {gs}, "RID": {"1"}, "VER": {"8"}, "CVER": {"1"}}
	_, err = post(LoungeBase+"/bc/bind?"+q.Encode(), token, url.Values{
		"count": {"1"}, "req0__sc": {"setPlaylist"}, "req0_videoId": {videoID}, "req0_listId": {""},
		"req0_currentTime": {"0"}, "req0_currentIndex": {"-1"}, "req0_audioOnly": {"false"},
	})
	if err != nil {
		return fmt.Errorf("youtube cast play: %w", err)
	}
	return nil
}
