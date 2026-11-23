package youtube

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

const answer = `{"contents":{"twoColumnSearchResultsRenderer":{"primaryContents":{"sectionListRenderer":{"contents":[
{"itemSectionRenderer":{"contents":[
 {"adSlotRenderer":{}},
 {"videoRenderer":{"videoId":"aaaaaaaaaa1","title":{"runs":[{"text":"First "},{"text":"video"}]},"ownerText":{"runs":[{"text":"Chan A"}]},"lengthText":{"simpleText":"12:34"}}},
 {"videoRenderer":{"videoId":"bbbbbbbbbb2","title":{"runs":[{"text":"A live one"}]},"ownerText":{"runs":[{"text":"Chan B"}]}}},
 {"videoRenderer":{"videoId":"aaaaaaaaaa1","title":{"runs":[{"text":"Duplicate"}]}}},
 {"videoRenderer":{"videoId":"bad id","title":{"runs":[{"text":"Bad"}]}}},
 {"videoRenderer":{"videoId":"cccccccccc3","title":{"simpleText":"Third"},"lengthText":{"simpleText":"1:00:00"}}}
]}}]}}}}}`

func TestSearch(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 1024)
		n, _ := r.Body.Read(b)
		got = string(b[:n])
		_, _ = w.Write([]byte(answer))
	}))
	defer srv.Close()
	old := Endpoint
	Endpoint = srv.URL
	defer func() { Endpoint = old }()

	vs, err := Search(context.Background(), `lofi "music"`, 5)
	if err != nil {
		t.Fatal(err)
	}
	want := []Video{
		{"aaaaaaaaaa1", "First video", "Chan A", "12:34"},
		{"bbbbbbbbbb2", "A live one", "Chan B", "LIVE"},
		{"cccccccccc3", "Third", "", "1:00:00"},
	}
	if len(vs) != len(want) {
		t.Fatalf("got %d videos: %+v", len(vs), vs)
	}
	for i := range want {
		if vs[i] != want[i] {
			t.Errorf("video %d = %+v, want %+v", i, vs[i], want[i])
		}
	}
	if want := `"query":"lofi \"music\""`; !contains(got, want) {
		t.Errorf("request %s lacks %s", got, want)
	}
	if vs, _ := Search(context.Background(), "x", 2); len(vs) != 2 {
		t.Errorf("limit 2 gave %d", len(vs))
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
