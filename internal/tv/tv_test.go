package tv

import "testing"

func TestYouTubeID(t *testing.T) {
	for in, want := range map[string]string{
		"https://www.youtube.com/watch?v=jfKfPfyJRdk":             "jfKfPfyJRdk",
		"https://youtu.be/jfKfPfyJRdk?si=abc":                     "jfKfPfyJRdk",
		"https://youtube.com/shorts/abcdefghijk":                  "abcdefghijk",
		"https://m.youtube.com/watch?feature=share&v=A1b2C3d4E5_": "A1b2C3d4E5_",
		"jfKfPfyJRdk": "jfKfPfyJRdk",
	} {
		if got, ok := YouTubeID(in); !ok || got != want {
			t.Errorf("YouTubeID(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"hello there", "https://example.com/watch?x=1", "short"} {
		if got, ok := YouTubeID(in); ok {
			t.Errorf("YouTubeID(%q) = %q; want no match", in, got)
		}
	}
}

func TestLookupApp(t *testing.T) {
	for in, want := range map[string]string{"YouTube": "YouTube", "prime video": "Prime Video", "Apple TV": "Apple TV", "net": "Netflix"} {
		if a, ok := LookupApp(in); !ok || a.Name != want {
			t.Errorf("LookupApp(%q) = %v, %v; want %s", in, a, ok, want)
		}
	}
	if _, ok := LookupApp("ab"); ok {
		t.Error("a two-letter name should not match")
	}
}
