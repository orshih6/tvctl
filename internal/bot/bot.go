// Package bot turns Telegram messages into TV actions: commands, a button
// remote, photos/videos sent to the bot (shown on the TV), YouTube links
// (played on the TV) and plain-language requests (Claude picks the actions).
package bot

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/orshih6/tvctl/internal/claude"
	"github.com/orshih6/tvctl/internal/telegram"
	"github.com/orshih6/tvctl/internal/tv"
	"github.com/orshih6/tvctl/internal/youtube"
)

type Bot struct {
	TV        *tv.TV
	TG        *telegram.Bot
	LLM       claude.LLM // nil: plain-language requests are off
	MediaDir  string     // photos/videos waiting for the TV to fetch them
	MediaPort int        // served on this port, on every interface
	AppURL    string     // public HTTPS address of the Mini App; empty: no Mini App
	// Search finds YouTube videos; nil: youtube.Search. Tests replace it.
	Search func(ctx context.Context, query string, n int) ([]youtube.Video, error)
	Log    *slog.Logger

	volMu   sync.Mutex // one volume change at a time
	volBase int        // the volume when the current burst of changes began
	volAt   time.Time  // the last change
}

// ServeMedia serves MediaDir to the TV and deletes files older than a day.
func (b *Bot) ServeMedia(ctx context.Context) error {
	if err := os.MkdirAll(b.MediaDir, 0o700); err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle("GET /m/", http.StripPrefix("/m/", http.FileServer(noList{http.Dir(b.MediaDir)})))
	srv := &http.Server{Addr: ":" + strconv.Itoa(b.MediaPort), Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	go func() {
		for ctx.Err() == nil {
			entries, _ := os.ReadDir(b.MediaDir)
			for _, e := range entries {
				if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > 24*time.Hour {
					_ = os.Remove(filepath.Join(b.MediaDir, e.Name()))
				}
			}
			select {
			case <-ctx.Done():
			case <-time.After(time.Hour):
			}
		}
	}()
	err := srv.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// noList refuses directory listings: only a file's exact random name works.
type noList struct{ fs http.FileSystem }

func (n noList) Open(name string) (http.File, error) {
	f, err := n.fs.Open(name)
	if err != nil {
		return nil, err
	}
	if st, err := f.Stat(); err == nil && st.IsDir() {
		f.Close()
		return nil, os.ErrNotExist
	}
	return f, nil
}

// ---- the remote ---------------------------------------------------------------

func (b *Bot) remote() [][]telegram.Button {
	k := func(label, key string) telegram.Button { return telegram.Button{Text: label, Data: "k:" + key} }
	rows := [][]telegram.Button{
		{k("🔇", "KEY_MUTE"), k("🔉 Vol −", "KEY_VOLDOWN"), k("🔊 Vol +", "KEY_VOLUP")},
		{k("·", "KEY_MENU"), k("⬆️", "KEY_UP"), k("·", "KEY_INFO")},
		{k("⬅️", "KEY_LEFT"), k("OK", "KEY_ENTER"), k("➡️", "KEY_RIGHT")},
		{k("↩️ Back", "KEY_RETURN"), k("⬇️", "KEY_DOWN"), k("🏠 Home", "KEY_HOME")},
		{k("⏪", "KEY_REWIND"), k("⏯", "KEY_PLAY_BACK"), k("⏩", "KEY_FF")},
		{{Text: "▶️ YouTube", Data: "o:youtube"}, {Text: "🎬 Netflix", Data: "o:netflix"}, {Text: "🧹 Close apps", Data: "c:bg"}},
		{{Text: "📊 Status", Data: "s:"}, {Text: "⏻ Off", Data: "p:off"}, {Text: "⏻ On", Data: "p:on"}},
	}
	if b.AppURL != "" {
		rows = append([][]telegram.Button{{{Text: "🎚 Open the remote (volume slider)", App: b.AppURL}}}, rows...)
	}
	return rows
}

var Commands = []telegram.Command{
	{Name: "remote", Help: "the button remote"},
	{Name: "status", Help: "is it on, volume, what's open"},
	{Name: "on", Help: "wake the TV"},
	{Name: "off", Help: "TV to standby"},
	{Name: "vol", Help: "volume: /vol 10, /vol up, /vol down"},
	{Name: "mute", Help: "mute or unmute"},
	{Name: "open", Help: "open an app: /open youtube"},
	{Name: "close", Help: "close background apps (/close all: every app)"},
	{Name: "yt", Help: "search YouTube: /yt lofi music (or a link to play it)"},
	{Name: "stop", Help: "stop a photo or video sent from here"},
	{Name: "help", Help: "what this bot can do"},
}

const help = `<b>TV remote</b> for the Samsung TV.

• /remote — buttons: arrows, OK, Back, Home, volume
• Send a <b>photo or video</b> — it appears on the TV
• Send a <b>YouTube link</b> — it plays on the TV
• /yt lofi music — <b>search YouTube</b>, tap a result to play it
• /vol 10 · /mute · /open netflix · /close · /on · /off · /status
• Or just say it: <i>"turn it down a bit"</i>, <i>"put on lofi music"</i>, <i>"close everything and open YouTube"</i>

Only you can use this bot.`

func (b *Bot) Button(ctx context.Context, data string) string {
	kind, arg, _ := strings.Cut(data, ":")
	switch kind {
	case "k":
		if !keyName.MatchString(arg) {
			return ""
		}
		if err := b.TV.Keys(ctx, arg); err != nil {
			return b.problem(ctx, err)
		}
		return ""
	case "o":
		return plain(b.open(ctx, arg))
	case "c":
		return plain(b.closeApps(ctx, false))
	case "s":
		return plain(b.status(ctx))
	case "p":
		return plain(b.power(ctx, arg == "on"))
	case "y": // a search result
		return plain(b.youtube(ctx, arg))
	}
	return ""
}

func (b *Bot) Command(ctx context.Context, name, args string) (string, [][]telegram.Button) {
	switch name {
	case "start", "help":
		return help, b.remote()
	case "remote":
		return "📺 Remote", b.remote()
	}
	if isSearch(name, args) {
		return b.search(ctx, args)
	}
	if reply, ok := b.quick(ctx, name, args); ok {
		return reply, nil
	}
	return "I don't know /" + telegram.Esc(name) + ". Try /help.", nil
}

// quick handles a command with or without its slash ("vol 10", "netflix").
func (b *Bot) quick(ctx context.Context, name, args string) (string, bool) {
	switch name {
	case "status":
		return b.status(ctx), true
	case "on":
		return b.power(ctx, true), true
	case "off":
		return b.power(ctx, false), true
	case "vol", "volume":
		return b.volume(ctx, args), true
	case "mute", "unmute":
		return b.mute(ctx, name), true
	case "open":
		return b.open(ctx, args), true
	case "close":
		return b.closeApps(ctx, strings.EqualFold(args, "all")), true
	case "yt", "youtube":
		if args == "" {
			return b.open(ctx, "youtube"), true
		}
		return b.youtube(ctx, args), true
	case "stop":
		if err := b.TV.StopCast(ctx); err != nil {
			return b.problem(ctx, err), true
		}
		return "⏹ Stopped.", true
	}
	if _, ok := tv.LookupApp(name); ok && args == "" {
		return b.open(ctx, name), true
	}
	return "", false
}

func (b *Bot) Text(ctx context.Context, text string) (string, [][]telegram.Button) {
	if id, ok := tv.YouTubeID(text); ok && strings.Contains(strings.ToLower(text), "you") {
		return b.youtube(ctx, id), nil
	}
	name, args, _ := strings.Cut(strings.TrimSpace(text), " ")
	if isSearch(strings.ToLower(name), strings.TrimSpace(args)) {
		return b.search(ctx, strings.TrimSpace(args))
	}
	if reply, ok := b.quick(ctx, strings.ToLower(name), strings.TrimSpace(args)); ok {
		return reply, nil
	}
	if strings.EqualFold(text, "remote") {
		return "📺 Remote", b.remote()
	}
	if b.LLM == nil {
		return "I only understand commands for now. Try /help.", nil
	}
	return b.ask(ctx, text), nil
}

func (b *Bot) Media(ctx context.Context, m telegram.Media) string {
	kind := strings.SplitN(m.Mime, "/", 2)[0]
	if kind != "image" && kind != "video" && kind != "audio" {
		return "I can show photos, videos and music on the TV, not " + telegram.Esc(or(m.Mime, "this file")) + "."
	}
	if m.Size > telegram.MaxDownload {
		return "That file is over 20 MB, the most a Telegram bot can download. Send a smaller one (or send it as a photo, which Telegram compresses)."
	}
	ip, err := b.TV.LocalIP()
	if err != nil {
		return b.problem(ctx, err)
	}
	name := randomName() + ext(m)
	path := filepath.Join(b.MediaDir, name)
	if err := b.TG.Download(ctx, m.FileID, path); err != nil {
		return "Couldn't download it from Telegram: " + telegram.Esc(err.Error())
	}
	u := "http://" + net.JoinHostPort(ip, strconv.Itoa(b.MediaPort)) + "/m/" + name
	if err := b.TV.Cast(ctx, u, m.Mime, "From Telegram"); err != nil {
		return b.problem(ctx, err)
	}
	what := map[string]string{"image": "🖼 On the TV now.", "video": "🎞 Playing on the TV.", "audio": "🎵 Playing on the TV."}[kind]
	return what + " Press Back on the remote (or /stop) to leave it."
}

// ---- actions --------------------------------------------------------------------

func (b *Bot) status(ctx context.Context) string {
	info, err := b.TV.Info(ctx)
	if err != nil {
		return offMsg
	}
	var s strings.Builder
	fmt.Fprintf(&s, "📺 <b>%s</b> — %s\n", telegram.Esc(info.Name), map[bool]string{true: "on", false: "standby"}[info.PowerState == "on"])
	if vol, muted, err := b.TV.Volume(ctx); err == nil {
		fmt.Fprintf(&s, "🔊 Volume %d%s\n", vol, map[bool]string{true: " (muted)", false: ""}[muted])
	}
	if apps, err := b.TV.Running(ctx); err == nil {
		if len(apps) == 0 {
			s.WriteString("No apps open.")
		}
		for _, a := range apps {
			fmt.Fprintf(&s, "• %s%s\n", telegram.Esc(a.Name), map[bool]string{true: " — on screen", false: " — in the background"}[a.Visible])
		}
	}
	return strings.TrimSpace(s.String())
}

const offMsg = "📴 The TV isn't answering — it's off or off Wi-Fi. Send /on to try waking it."

func (b *Bot) power(ctx context.Context, on bool) string {
	if on {
		if err := b.TV.On(ctx); err != nil {
			return "⚠️ " + telegram.Esc(err.Error())
		}
		return "⏻ The TV is on."
	}
	if err := b.TV.Off(ctx); err != nil {
		return b.problem(ctx, err)
	}
	return "⏻ Off (standby). /on wakes it."
}

func (b *Bot) volume(ctx context.Context, arg string) string {
	arg = strings.ToLower(strings.TrimSpace(arg))
	cur, _, err := b.TV.Volume(ctx)
	if err != nil {
		return b.problem(ctx, err)
	}
	n := cur
	switch {
	case arg == "":
		return fmt.Sprintf("🔊 Volume is %d.", cur)
	case arg == "up" || arg == "+":
		n = cur + 3
	case arg == "down" || arg == "-":
		n = cur - 3
	default:
		v, err := strconv.Atoi(strings.TrimPrefix(arg, "+"))
		if err != nil {
			return "Volume is a number from 0 to 100, or up / down."
		}
		if strings.HasPrefix(arg, "+") || strings.HasPrefix(arg, "-") {
			n = cur + v
		} else {
			n = v
		}
	}
	cur, n, err = b.setVolume(ctx, n)
	if err != nil {
		return b.problem(ctx, err)
	}
	return fmt.Sprintf("🔊 Volume %d → %d.", cur, n)
}

// volBurst: volume changes closer together than this count as one jump, so
// a slider dragged up (many small steps) is capped like one big step.
const volBurst = 2 * time.Second

// capVolume is the volume to set when want is asked for and base is where
// the jump started: a typo, a model mistake or a slipped thumb shouldn't
// blast the room, so above 40 it rises at most 10 at once. Ask again to go
// higher.
func capVolume(base, want int) int {
	n := max(0, min(want, 100))
	if n > 40 && n > base+10 {
		n = base + 10
	}
	return n
}

// setVolume is the one way the volume is set: every caller gets the cap.
// It returns the volume before and after.
func (b *Bot) setVolume(ctx context.Context, want int) (cur, n int, err error) {
	b.volMu.Lock()
	defer b.volMu.Unlock()
	cur, _, err = b.TV.Volume(ctx)
	if err != nil {
		return 0, 0, err
	}
	base := cur
	if time.Since(b.volAt) < volBurst {
		base = min(base, b.volBase)
	}
	n = capVolume(base, want)
	if n != cur {
		if err := b.TV.SetVolume(ctx, n); err != nil {
			return cur, cur, err
		}
	}
	b.volBase, b.volAt = base, time.Now()
	return cur, n, nil
}

func (b *Bot) mute(ctx context.Context, which string) string {
	_, muted, err := b.TV.Volume(ctx)
	if err != nil {
		return b.problem(ctx, err)
	}
	want := !muted
	if which == "unmute" {
		want = false
	}
	if err := b.TV.SetMute(ctx, want); err != nil {
		return b.problem(ctx, err)
	}
	return map[bool]string{true: "🔇 Muted.", false: "🔊 Sound on."}[want]
}

func (b *Bot) open(ctx context.Context, name string) string {
	a, ok := tv.LookupApp(name)
	if !ok {
		var names []string
		for k := range tv.Apps {
			names = append(names, k)
		}
		return "I know these apps: " + strings.Join(names, ", ") + "."
	}
	if err := b.TV.Launch(ctx, a); err != nil {
		return b.problem(ctx, err)
	}
	return "▶️ Opening " + telegram.Esc(a.Name) + "."
}

func (b *Bot) closeApps(ctx context.Context, all bool) string {
	closed, err := b.TV.CloseBackground(ctx, all)
	if err != nil {
		return b.problem(ctx, err)
	}
	if len(closed) == 0 {
		return "🧹 Nothing was open in the background."
	}
	return "🧹 Closed " + telegram.Esc(strings.Join(closed, ", ")) + "."
}

func (b *Bot) youtube(ctx context.Context, link string) string {
	id, ok := tv.YouTubeID(link)
	if !ok {
		return "That doesn't look like a YouTube link."
	}
	if err := b.TV.PlayYouTube(ctx, id); err != nil {
		return b.problem(ctx, err)
	}
	return "▶️ Playing on the TV."
}

// isSearch: "yt lofi music" is a search; "yt <link or id>" plays, and "yt"
// alone opens the app (both left to quick).
func isSearch(name, args string) bool {
	switch name {
	case "search":
		return true
	case "yt", "youtube":
		return args != "" && !isVideo(args)
	}
	return false
}

var lowerWord = regexp.MustCompile(`^[a-z]+$`)

// isVideo reports whether s is a YouTube link or a bare video id rather than
// search words. An 11-letter lowercase word ("interstella") is taken as a
// word: real ids almost never look like that.
func isVideo(s string) bool {
	_, ok := tv.YouTubeID(s)
	return ok && !lowerWord.MatchString(strings.TrimSpace(s))
}

const searchResults = 6

func (b *Bot) find(ctx context.Context, q string, n int) ([]youtube.Video, error) {
	if b.Search != nil {
		return b.Search(ctx, q, n)
	}
	return youtube.Search(ctx, q, n)
}

// search lists YouTube videos for q, one button each; a tap plays it.
func (b *Bot) search(ctx context.Context, q string) (string, [][]telegram.Button) {
	if q == "" {
		return "Search for what? e.g. /yt lofi music", nil
	}
	vs, err := b.find(ctx, q, searchResults)
	if err != nil {
		b.Log.Warn("youtube search failed", "err", err)
		return "⚠️ YouTube search didn't answer. Try again, or send a link.", nil
	}
	if len(vs) == 0 {
		return "Nothing on YouTube for “" + telegram.Esc(q) + "”.", nil
	}
	var s strings.Builder
	var rows [][]telegram.Button
	fmt.Fprintf(&s, "🔎 <b>%s</b> — tap one to play it on the TV\n", telegram.Esc(q))
	for i, v := range vs {
		fmt.Fprintf(&s, "\n%d. %s\n    <i>%s</i>", i+1, telegram.Esc(v.Title), telegram.Esc(strings.Trim(v.Channel+" · "+v.Length, " ·")))
		rows = append(rows, []telegram.Button{{Text: fmt.Sprintf("%d. %s", i+1, telegram.Clip(v.Title, 44)), Data: "y:" + v.ID}})
	}
	return s.String(), rows
}

// problem turns an error into a reply, checking first whether the TV is
// simply off.
func (b *Bot) problem(ctx context.Context, err error) string {
	b.Log.Warn("tv action failed", "err", err)
	if errors.Is(err, tv.ErrNotAllowed) {
		return "🔐 The TV is asking whether to allow \"" + telegram.Esc(b.TV.Name) + "\" — press <b>Allow</b> on the TV with the real remote, then try again."
	}
	if _, ierr := b.TV.Info(ctx); ierr != nil {
		return offMsg
	}
	return "⚠️ " + telegram.Esc(err.Error())
}

// ---- plain language -----------------------------------------------------------

type plan struct {
	Reply   string `json:"reply"`
	Actions []struct {
		Do  string `json:"do"`
		Arg string `json:"arg"`
	} `json:"actions"`
}

const planSchema = `{"type":"object","additionalProperties":false,"required":["reply","actions"],"properties":{
"reply":{"type":"string","description":"one short sentence to the user, in their language"},
"actions":{"type":"array","maxItems":12,"items":{"type":"object","additionalProperties":false,"required":["do","arg"],"properties":{
"do":{"type":"string","enum":["key","open","close_background","close_all","volume","mute","unmute","youtube","on","off","wait"]},
"arg":{"type":"string","description":"key: a KEY_ name; open: app name; volume: number or +N/-N; youtube: an 11-character video id; wait: seconds (max 10); else empty"}}}}}}`

func (b *Bot) ask(ctx context.Context, text string) string {
	state := b.status(ctx)
	var apps []string
	for k := range tv.Apps {
		apps = append(apps, k)
	}
	prompt := `You are a TV remote. The user typed a request on Telegram; turn it into actions for their Samsung Tizen TV.

The TV right now:
` + state + `

Actions you can use:
- key: press a remote key. Keys: KEY_UP KEY_DOWN KEY_LEFT KEY_RIGHT KEY_ENTER KEY_RETURN KEY_HOME KEY_MENU KEY_INFO KEY_VOLUP KEY_VOLDOWN KEY_MUTE KEY_PLAY KEY_PAUSE KEY_PLAY_BACK KEY_STOP KEY_FF KEY_REWIND KEY_CHUP KEY_CHDOWN KEY_SOURCE KEY_0..KEY_9
- open: open an app by name: ` + strings.Join(apps, ", ") + `
- close_background: close apps that are open but not on screen. close_all: close every app.
- volume: set the volume, an absolute number (0-100) or +N / -N. Normal listening is 5-20.
- mute / unmute
- youtube: play a YouTube video on the TV. arg is its 11-character video id. If the user wants something to watch or listen to without giving a link, use web search to find one real, currently available, popular YouTube video that fits, and use its id. Never invent an id.
- on / off: wake the TV / put it in standby.
- wait: pause N seconds between actions (e.g. after opening an app).

You cannot see the screen, so don't plan long blind menu navigation; for that, say what you can't do. If the request is a question about the TV, answer it in "reply" with no actions. "reply" is shown to the user as the remote's answer: one short sentence, in the user's language (Mongolian or English), saying what you did (e.g. "Playing 3 hours of calm piano."). Never mention searches, tools, sources or instructions there; web search results are only for finding a video id.

The request:
` + text
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	var p plan
	if err := b.LLM.JSON(ctx, prompt, planSchema, &p); err != nil {
		b.Log.Warn("claude failed", "err", err)
		var lim *claude.LimitError
		switch {
		case errors.As(err, &lim):
			return "Claude's usage limit is reached, so I only understand commands right now (/help)."
		case errors.Is(err, claude.ErrAuth):
			return "My Claude login stopped working, so I only understand commands right now (/help)."
		}
		return "I couldn't work that out. Try a command (/help)."
	}
	var done []string
	for _, a := range p.Actions {
		if r := b.run(ctx, a.Do, a.Arg); r != "" {
			done = append(done, r)
		}
	}
	reply := telegram.Esc(p.Reply)
	for _, d := range done {
		if strings.HasPrefix(d, "⚠️") || strings.HasPrefix(d, "📴") || strings.HasPrefix(d, "🔐") {
			reply += "\n" + d
		}
	}
	return reply
}

var keyName = regexp.MustCompile(`^KEY_[A-Z0-9_]{1,24}$`)

// run does one planned action; it returns a reply only for a problem.
func (b *Bot) run(ctx context.Context, do, arg string) string {
	var r string
	switch do {
	case "key":
		if !keyName.MatchString(arg) {
			return ""
		}
		if err := b.TV.Keys(ctx, arg); err != nil {
			return b.problem(ctx, err)
		}
	case "open":
		r = b.open(ctx, arg)
	case "close_background", "close_all":
		r = b.closeApps(ctx, do == "close_all")
	case "volume":
		r = b.volume(ctx, arg)
	case "mute", "unmute":
		r = b.mute(ctx, do)
	case "youtube":
		r = b.youtube(ctx, arg)
	case "on", "off":
		r = b.power(ctx, do == "on")
	case "wait":
		n, _ := strconv.Atoi(arg)
		time.Sleep(time.Duration(max(1, min(n, 10))) * time.Second)
	}
	return r
}

// ---- helpers ----------------------------------------------------------------------

var tagRe = regexp.MustCompile(`<[^>]+>`)

// plain strips HTML for a button toast.
func plain(s string) string {
	return strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&#34;", `"`, "&#39;", "'").Replace(tagRe.ReplaceAllString(s, ""))
}

func randomName() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func ext(m telegram.Media) string {
	if e := filepath.Ext(m.Name); e != "" && len(e) <= 5 {
		return strings.ToLower(e)
	}
	return map[string]string{"image": ".jpg", "video": ".mp4", "audio": ".mp3"}[strings.SplitN(m.Mime, "/", 2)[0]]
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
