package bot

import (
	"context"
	"strings"
	"testing"

	"github.com/orshih6/tvctl/internal/youtube"
)

func TestPlain(t *testing.T) {
	if got := plain("🧹 Closed <b>Netflix</b> &amp; YouTube."); got != "🧹 Closed Netflix & YouTube." {
		t.Errorf("plain = %q", got)
	}
}

func TestKeyName(t *testing.T) {
	for _, k := range []string{"KEY_HOME", "KEY_VOLUP", "KEY_0"} {
		if !keyName.MatchString(k) {
			t.Errorf("%s should be a key", k)
		}
	}
	for _, k := range []string{"HOME", "KEY_home", "KEY_HOME; rm"} {
		if keyName.MatchString(k) {
			t.Errorf("%q should not be a key", k)
		}
	}
}

func TestIsSearch(t *testing.T) {
	for _, c := range []struct {
		name, args string
		want       bool
	}{
		{"yt", "lofi music", true},
		{"youtube", "монгол дуу", true},
		{"yt", "interstella", true}, // 11 lowercase letters: a word, not an id
		{"search", "dQw4w9WgXcQ", true},
		{"yt", "", false}, // opens the app
		{"yt", "dQw4w9WgXcQ", false},
		{"yt", "https://youtu.be/dQw4w9WgXcQ", false},
		{"yt", "https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=3", false},
		{"vol", "10", false},
	} {
		if got := isSearch(c.name, c.args); got != c.want {
			t.Errorf("isSearch(%q, %q) = %v", c.name, c.args, got)
		}
	}
}

func TestSearchReply(t *testing.T) {
	b := &Bot{Search: func(_ context.Context, q string, n int) ([]youtube.Video, error) {
		return []youtube.Video{{ID: "aaaaaaaaaa1", Title: "Tom & <Jerry>", Channel: "WB", Length: "3:00"}, {ID: "bbbbbbbbbb2", Title: "Live", Length: "LIVE"}}, nil
	}}
	text, rows := b.Text(context.Background(), "yt tom and jerry")
	if !strings.Contains(text, "Tom &amp; &lt;Jerry&gt;") || !strings.Contains(text, "WB · 3:00") {
		t.Errorf("reply = %q", text)
	}
	if len(rows) != 2 || rows[0][0].Data != "y:aaaaaaaaaa1" || rows[1][0].Data != "y:bbbbbbbbbb2" {
		t.Errorf("buttons = %+v", rows)
	}
	if !appAction("y:aaaaaaaaaa1") || appAction("y:https://youtu.be/aaaaaaaaaa1") || appAction("y:short") {
		t.Error("the Mini App plays a search result by id, nothing else")
	}
}
