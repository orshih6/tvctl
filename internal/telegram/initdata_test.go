package telegram

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// sign builds init data the way Telegram does.
func sign(token string, fields map[string]string) string {
	var lines []string
	q := url.Values{}
	for k, v := range fields {
		lines = append(lines, k+"="+v)
		q.Set(k, v)
	}
	sort.Strings(lines)
	key := hmac.New(sha256.New, []byte("WebAppData"))
	key.Write([]byte(token))
	mac := hmac.New(sha256.New, key.Sum(nil))
	mac.Write([]byte(strings.Join(lines, "\n")))
	q.Set("hash", hex.EncodeToString(mac.Sum(nil)))
	return q.Encode()
}

func TestVerifyInitData(t *testing.T) {
	const token = "123:secret"
	now := time.Unix(1_800_000_000, 0)
	b := New(token, 42, nil)
	fields := func(user string, at time.Time) map[string]string {
		return map[string]string{"auth_date": strconv.FormatInt(at.Unix(), 10), "query_id": "AAE", "user": user}
	}
	me := `{"id":42,"first_name":"A"}`

	good := sign(token, fields(me, now.Add(-time.Minute)))
	if err := b.VerifyInitData(good, now); err != nil {
		t.Fatalf("good init data refused: %v", err)
	}
	bad := map[string]string{
		"empty":             "",
		"no hash":           "auth_date=1&user=" + url.QueryEscape(me),
		"another bot":       sign("999:other", fields(me, now)),
		"another user":      sign(token, fields(`{"id":43}`, now)),
		"no user":           sign(token, map[string]string{"auth_date": strconv.FormatInt(now.Unix(), 10)}),
		"too old":           sign(token, fields(me, now.Add(-InitDataMaxAge-time.Minute))),
		"from the future":   sign(token, fields(me, now.Add(time.Hour))),
		"edited after":      strings.Replace(good, "AAE", "AAF", 1),
		"user swapped in":   strings.Replace(sign(token, fields(`{"id":43}`, now)), "43", "42", 1),
		"not a query":       "%zz",
		"field given twice": good + "&query_id=AAE",
	}
	for name, data := range bad {
		if err := b.VerifyInitData(data, now); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
