// Package telegram is tvctl's Bot API client: text, a button remote, and the
// photos and videos sent to the bot. Only the configured chat is heard;
// anyone else is ignored without a reply.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

type Bot struct {
	token  string
	chatID int64
	http   *http.Client
	log    *slog.Logger
	Base   string // Bot API root; tests point it at a fake
}

func New(token string, chatID int64, log *slog.Logger) *Bot {
	return &Bot{token: token, chatID: chatID, http: &http.Client{Timeout: 70 * time.Second}, log: log, Base: "https://api.telegram.org"}
}

func (b *Bot) redact(err error) error {
	return fmt.Errorf("telegram: %s", strings.ReplaceAll(err.Error(), b.token, "***"))
}

func (b *Bot) call(ctx context.Context, method string, params, out any) error {
	body, err := json.Marshal(params)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", b.Base+"/bot"+b.token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.http.Do(req)
	if err != nil {
		return b.redact(err) // the URL carries the token
	}
	defer resp.Body.Close()
	var r struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return err
	}
	if !r.OK {
		return fmt.Errorf("telegram %s: %s", method, r.Description)
	}
	if out != nil {
		return json.Unmarshal(r.Result, out)
	}
	return nil
}

// Button is an inline callback button, or, with App set, one that opens that
// HTTPS page as a Mini App.
type Button struct{ Text, Data, App string }

func keyboard(rows [][]Button) map[string]any {
	kb := [][]map[string]any{}
	for _, row := range rows {
		var r []map[string]any
		for _, bt := range row {
			if bt.App != "" {
				r = append(r, map[string]any{"text": bt.Text, "web_app": map[string]string{"url": bt.App}})
				continue
			}
			r = append(r, map[string]any{"text": bt.Text, "callback_data": bt.Data})
		}
		if len(r) > 0 {
			kb = append(kb, r)
		}
	}
	return map[string]any{"inline_keyboard": kb}
}

// Send posts HTML text, with buttons if given.
func (b *Bot) Send(ctx context.Context, text string, rows ...[]Button) error {
	p := map[string]any{"chat_id": b.chatID, "text": Clip(text, 4096), "parse_mode": "HTML", "disable_web_page_preview": true}
	if len(rows) > 0 {
		p["reply_markup"] = keyboard(rows)
	}
	return b.call(ctx, "sendMessage", p, nil)
}

func (b *Bot) typing(ctx context.Context) {
	_ = b.call(ctx, "sendChatAction", map[string]any{"chat_id": b.chatID, "action": "typing"}, nil)
}

func (b *Bot) answer(ctx context.Context, id, text string) {
	_ = b.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": id, "text": Clip(text, 190)}, nil)
}

type Command struct{ Name, Help string }

func (b *Bot) SetCommands(ctx context.Context, cs []Command) error {
	var list []map[string]string
	for _, c := range cs {
		list = append(list, map[string]string{"command": c.Name, "description": c.Help})
	}
	return b.call(ctx, "setMyCommands", map[string]any{"commands": list}, nil)
}

// SetMenuButton makes the button beside the message box open url as a Mini
// App, for the configured chat only.
func (b *Bot) SetMenuButton(ctx context.Context, text, url string) error {
	return b.call(ctx, "setChatMenuButton", map[string]any{"chat_id": b.chatID,
		"menu_button": map[string]any{"type": "web_app", "text": text, "web_app": map[string]string{"url": url}}}, nil)
}

// Media is a photo, video or audio file sent to the bot.
type Media struct {
	FileID string
	Mime   string // best guess when Telegram gives none
	Name   string
	Size   int64
}

// MaxDownload is the Bot API's limit for getFile.
const MaxDownload = 20 << 20

// Download saves a file sent to the bot into path.
func (b *Bot) Download(ctx context.Context, fileID, path string) error {
	var f struct {
		FilePath string `json:"file_path"`
	}
	if err := b.call(ctx, "getFile", map[string]any{"file_id": fileID}, &f); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", b.Base+"/file/bot"+b.token+"/"+f.FilePath, nil)
	if err != nil {
		return err
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return b.redact(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("telegram download: HTTP %d", resp.StatusCode)
	}
	out, err := os.Create(path)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, io.LimitReader(resp.Body, MaxDownload+1)); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// Handler receives what the user does. Each method returns the reply (HTML;
// empty for none) and, optionally, buttons to send with it.
type Handler interface {
	Button(ctx context.Context, data string) (toast string)
	Command(ctx context.Context, name, args string) (string, [][]Button)
	Text(ctx context.Context, text string) (string, [][]Button)
	Media(ctx context.Context, m Media) string
}

type file struct {
	FileID   string `json:"file_id"`
	FileSize int64  `json:"file_size"`
	MimeType string `json:"mime_type"`
	FileName string `json:"file_name"`
}

type update struct {
	UpdateID int64 `json:"update_id"`
	Message  *struct {
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
		Text     string `json:"text"`
		Caption  string `json:"caption"`
		Photo    []file `json:"photo"`
		Video    *file  `json:"video"`
		Audio    *file  `json:"audio"`
		Document *file  `json:"document"`
	} `json:"message"`
	CallbackQuery *struct {
		ID      string `json:"id"`
		Data    string `json:"data"`
		Message *struct {
			Chat struct {
				ID int64 `json:"id"`
			} `json:"chat"`
		} `json:"message"`
	} `json:"callback_query"`
}

// Poll long-polls until ctx ends. The offset is persisted so a restart
// neither replays nor drops a press.
func (b *Bot) Poll(ctx context.Context, h Handler, offset func() int64, save func(int64)) {
	next := offset()
	for ctx.Err() == nil {
		var ups []update
		err := b.call(ctx, "getUpdates", map[string]any{"offset": next, "timeout": 50, "allowed_updates": []string{"message", "callback_query"}}, &ups)
		if err != nil {
			if ctx.Err() == nil {
				b.log.Warn("telegram poll failed", "err", err)
				select {
				case <-ctx.Done():
				case <-time.After(10 * time.Second):
				}
			}
			continue
		}
		for _, u := range ups {
			next = u.UpdateID + 1
			save(next) // first: a press that crashes the handler must not replay forever
			b.handle(ctx, h, u)
		}
	}
}

func (b *Bot) handle(ctx context.Context, h Handler, u update) {
	if q := u.CallbackQuery; q != nil {
		if q.Message == nil || q.Message.Chat.ID != b.chatID {
			return
		}
		b.answer(ctx, q.ID, h.Button(ctx, q.Data))
		return
	}
	m := u.Message
	if m == nil || m.Chat.ID != b.chatID {
		return
	}
	var media *Media
	switch {
	case len(m.Photo) > 0:
		p := m.Photo[len(m.Photo)-1] // the largest size
		media = &Media{FileID: p.FileID, Mime: "image/jpeg", Name: "photo.jpg", Size: p.FileSize}
	case m.Video != nil:
		media = &Media{FileID: m.Video.FileID, Mime: or(m.Video.MimeType, "video/mp4"), Name: or(m.Video.FileName, "video.mp4"), Size: m.Video.FileSize}
	case m.Audio != nil:
		media = &Media{FileID: m.Audio.FileID, Mime: or(m.Audio.MimeType, "audio/mpeg"), Name: or(m.Audio.FileName, "audio.mp3"), Size: m.Audio.FileSize}
	case m.Document != nil:
		media = &Media{FileID: m.Document.FileID, Mime: m.Document.MimeType, Name: m.Document.FileName, Size: m.Document.FileSize}
	}
	if media != nil {
		b.typing(ctx)
		if reply := h.Media(ctx, *media); reply != "" {
			b.reply(ctx, reply, nil)
		}
		return
	}
	text := strings.TrimSpace(m.Text)
	if text == "" {
		return
	}
	var reply string
	var rows [][]Button
	if strings.HasPrefix(text, "/") {
		cmd, args, _ := strings.Cut(text, " ")
		cmd, _, _ = strings.Cut(strings.TrimPrefix(cmd, "/"), "@")
		reply, rows = h.Command(ctx, strings.ToLower(cmd), strings.TrimSpace(args))
	} else {
		b.typing(ctx)
		reply, rows = h.Text(ctx, text)
	}
	if reply != "" {
		b.reply(ctx, reply, rows)
	}
}

func (b *Bot) reply(ctx context.Context, text string, rows [][]Button) {
	if err := b.Send(ctx, text, rows...); err != nil {
		b.log.Warn("telegram reply failed", "err", err)
	}
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// Esc escapes text for Telegram HTML.
func Esc(s string) string { return html.EscapeString(s) }

func Clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func ParseChatID(s string) (int64, error) { return strconv.ParseInt(strings.TrimSpace(s), 10, 64) }
