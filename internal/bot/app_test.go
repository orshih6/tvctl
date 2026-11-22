package bot

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/orshih6/tvctl/internal/telegram"
	"github.com/orshih6/tvctl/internal/tv"
)

const testToken = "123:secret"

// initData signs launch data for user id the way Telegram does.
func initData(token string, id int64) string {
	user := `{"id":` + strconv.FormatInt(id, 10) + `}`
	at := strconv.FormatInt(time.Now().Unix(), 10)
	key := hmac.New(sha256.New, []byte("WebAppData"))
	key.Write([]byte(token))
	mac := hmac.New(sha256.New, key.Sum(nil))
	mac.Write([]byte("auth_date=" + at + "\nuser=" + user))
	return url.Values{"auth_date": {at}, "user": {user}, "hash": {hex.EncodeToString(mac.Sum(nil))}}.Encode()
}

func testApp(t *testing.T) *httptest.Server {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	b := &Bot{
		// Nothing listens here: a request that got past the login fails at the TV.
		TV:  tv.New("127.0.0.1", "", "tvctl", t.TempDir()+"/token"),
		TG:  telegram.New(testToken, 42, log),
		Log: log,
	}
	srv := httptest.NewServer(b.appHandler())
	t.Cleanup(srv.Close)
	return srv
}

func call(t *testing.T, srv *httptest.Server, method, path, auth, body string) (int, string) {
	req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}

// The page is public; nothing under /api/ answers without Telegram's
// signature for the one chat.
func TestAppNeedsSignature(t *testing.T) {
	srv := testApp(t)
	if code, page := call(t, srv, "GET", "/", "", ""); code != 200 || !strings.Contains(page, "telegram-web-app.js") {
		t.Fatalf("GET / = %d", code)
	}
	logins := map[string]string{
		"nothing":      "",
		"not tma":      "Bearer " + initData(testToken, 42),
		"another chat": "tma " + initData(testToken, 43),
		"another bot":  "tma " + initData("999:other", 42),
		"garbage":      "tma hello",
	}
	calls := []struct{ method, path, body string }{
		{"GET", "/api/state", ""},
		{"POST", "/api/volume", `{"level":100}`},
		{"POST", "/api/do", `{"data":"p:off"}`},
	}
	for name, auth := range logins {
		for _, c := range calls {
			if code, body := call(t, srv, c.method, c.path, auth, c.body); code != http.StatusUnauthorized || body != "" {
				t.Errorf("%s: %s %s = %d %q, want an empty 401", name, c.method, c.path, code, body)
			}
		}
	}
}

func TestAppSigned(t *testing.T) {
	srv := testApp(t)
	me := "tma " + initData(testToken, 42)
	if code, body := call(t, srv, "GET", "/api/state", me, ""); code != 200 || !strings.Contains(body, `"on":false`) {
		t.Errorf("state = %d %s", code, body)
	}
	for _, body := range []string{``, `{}`, `{"level":"loud"}`, strings.Repeat(" ", 2000) + `{"level":1}`} {
		if code, _ := call(t, srv, "POST", "/api/volume", me, body); code != http.StatusBadRequest {
			t.Errorf("volume %q = %d, want 400", body, code)
		}
	}
	for _, data := range []string{"", "k:HOME", "k:KEY_HOME; rm", "o:browser", "x:1", "p:reboot"} {
		if code, _ := call(t, srv, "POST", "/api/do", me, `{"data":"`+data+`"}`); code != http.StatusBadRequest {
			t.Errorf("do %q = %d, want 400", data, code)
		}
	}
	// Only the page and the three calls exist; media is never served here.
	for _, path := range []string{"/m/x", "/api/", "/app.html", "/api/state/"} {
		if code, _ := call(t, srv, "GET", path, me, ""); code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, code)
		}
	}
}

func TestCapVolume(t *testing.T) {
	for _, c := range []struct{ base, want, got int }{
		{10, 25, 25},   // quiet range: free
		{10, 40, 40},   // up to 40 in one go
		{10, 100, 20},  // a slip to the top: +10
		{35, 60, 45},   // crossing 40: +10
		{50, 55, 55},   // small steps above 40 are fine
		{50, 61, 60},   // at most +10
		{80, 20, 20},   // down is never capped
		{5, -3, 0},     // floor
		{95, 250, 100}, // ceiling
	} {
		if got := capVolume(c.base, c.want); got != c.got {
			t.Errorf("capVolume(%d, %d) = %d, want %d", c.base, c.want, got, c.got)
		}
	}
}
