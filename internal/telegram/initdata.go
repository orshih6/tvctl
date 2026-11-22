package telegram

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// InitDataMaxAge is how long a Mini App's signed launch data is accepted. The
// page gets it once, when it opens, so this is also how long an open remote
// keeps working before it has to be reopened.
const InitDataMaxAge = 12 * time.Hour

var errInitData = errors.New("telegram: init data not accepted")

// VerifyInitData reports whether initData (Telegram.WebApp.initData, as the
// Mini App received it) was signed by Telegram for this bot, is fresh, and
// belongs to the configured chat. That is the Mini App's whole login: the
// page is public, so nothing may act without it.
//
// https://core.telegram.org/bots/webapps#validating-data-received-via-the-mini-app
func (b *Bot) VerifyInitData(initData string, now time.Time) error {
	if initData == "" || len(initData) > 4096 {
		return errInitData
	}
	vals, err := url.ParseQuery(initData)
	if err != nil {
		return errInitData
	}
	var lines []string
	for k, v := range vals {
		if k == "hash" {
			continue
		}
		if len(v) != 1 {
			return errInitData
		}
		lines = append(lines, k+"="+v[0])
	}
	sort.Strings(lines)

	key := hmac.New(sha256.New, []byte("WebAppData"))
	key.Write([]byte(b.token))
	mac := hmac.New(sha256.New, key.Sum(nil))
	mac.Write([]byte(strings.Join(lines, "\n")))
	got, err := hex.DecodeString(vals.Get("hash"))
	if err != nil || !hmac.Equal(got, mac.Sum(nil)) {
		return errInitData
	}

	// Signed, so the fields can be believed.
	at, err := strconv.ParseInt(vals.Get("auth_date"), 10, 64)
	if err != nil {
		return errInitData
	}
	if age := now.Sub(time.Unix(at, 0)); age > InitDataMaxAge || age < -5*time.Minute {
		return errInitData
	}
	var user struct {
		ID int64 `json:"id"`
	}
	// In a private chat the chat id is the user's id.
	if json.Unmarshal([]byte(vals.Get("user")), &user) != nil || user.ID == 0 || user.ID != b.chatID {
		return errInitData
	}
	return nil
}
