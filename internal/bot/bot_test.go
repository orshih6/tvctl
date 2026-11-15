package bot

import "testing"

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
