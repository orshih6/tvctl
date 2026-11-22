package bot

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// The Mini App: a remote with a real volume slider, opened inside Telegram.
// Its address is public (Telegram only opens HTTPS pages), so the page is an
// empty shell and every /api/ call must carry Telegram's signed launch data
// for the one chat this bot obeys. Without it nothing is read from the TV and
// nothing is done, the same rule as the bot's messages.

//go:embed app.html
var appHTML []byte

// ServeApp serves the Mini App on port until ctx ends. Media is not served
// here: this port is the one that may be routed from the internet.
func (b *Bot) ServeApp(ctx context.Context, port int) error {
	srv := &http.Server{
		Addr:              ":" + strconv.Itoa(port),
		Handler:           b.appHandler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (b *Bot) appHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Cache-Control", "no-store")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline' https://telegram.org; style-src 'unsafe-inline'; connect-src 'self'; img-src data:")
		_, _ = w.Write(appHTML)
	})
	mux.HandleFunc("GET /api/state", b.signed(func(w http.ResponseWriter, r *http.Request) {
		reply(w, b.appState(r.Context(), ""))
	}))
	mux.HandleFunc("POST /api/volume", b.signed(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Level *int `json:"level"`
		}
		if !body(w, r, &in) || in.Level == nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, n, err := b.setVolume(r.Context(), *in.Level)
		if err != nil {
			reply(w, b.appState(r.Context(), plain(b.problem(r.Context(), err))))
			return
		}
		// Only what changed: the page asks for this many times a second.
		out := struct {
			Volume int    `json:"volume"`
			Msg    string `json:"msg,omitempty"`
		}{Volume: n}
		if n < *in.Level && n < 100 {
			out.Msg = "Capped at " + strconv.Itoa(n) + ". Slide again to go higher."
		}
		reply(w, out)
	}))
	mux.HandleFunc("POST /api/do", b.signed(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Data string `json:"data"`
		}
		if !body(w, r, &in) || !appAction(in.Data) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		// The same code path as a tap on the bot's buttons.
		msg := b.Button(r.Context(), in.Data)
		if strings.HasPrefix(in.Data, "k:") {
			reply(w, map[string]string{"msg": msg}) // a key press: no need to ask the TV how it is
			return
		}
		reply(w, b.appState(r.Context(), msg))
	}))
	return mux
}

// appAction reports whether data is one of the bot's own button actions.
func appAction(data string) bool {
	kind, arg, ok := strings.Cut(data, ":")
	if !ok {
		return false
	}
	switch kind {
	case "k":
		return keyName.MatchString(arg)
	case "o":
		return arg == "youtube" || arg == "netflix"
	case "c":
		return arg == "bg"
	case "p":
		return arg == "on" || arg == "off"
	}
	return false
}

// signed lets a request through only with Telegram's signed launch data for
// this bot's chat ("Authorization: tma <initData>"). Anyone else gets an
// empty 401, whatever they asked for.
func (b *Bot) signed(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data, ok := strings.CutPrefix(r.Header.Get("Authorization"), "tma ")
		if !ok || b.TG.VerifyInitData(data, time.Now()) != nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		defer cancel()
		next(w, r.WithContext(ctx))
	}
}

type appState struct {
	On     bool   `json:"on"`
	Name   string `json:"name,omitempty"`
	Volume int    `json:"volume"`
	Muted  bool   `json:"muted"`
	Msg    string `json:"msg,omitempty"` // something to tell the user
}

func (b *Bot) appState(ctx context.Context, msg string) appState {
	st := appState{Msg: msg}
	info, err := b.TV.Info(ctx)
	if err != nil {
		return st
	}
	st.Name, st.On = info.Name, info.PowerState == "on"
	st.Volume, st.Muted, _ = b.TV.Volume(ctx)
	return st
}

func body(w http.ResponseWriter, r *http.Request, into any) bool {
	return json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(into) == nil
}

func reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}
