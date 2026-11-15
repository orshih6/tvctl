// tvctl controls the Samsung TV at home: `tvctl serve` is the Telegram bot,
// the other commands do the same things from a terminal on the home network.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/orshih6/tvctl/internal/bot"
	"github.com/orshih6/tvctl/internal/claude"
	"github.com/orshih6/tvctl/internal/telegram"
	"github.com/orshih6/tvctl/internal/tv"
)

var version = "dev"

const usage = `tvctl — the Samsung TV at home, from Telegram or a terminal

  tvctl serve            the Telegram bot (TELEGRAM_BOT_TOKEN, TELEGRAM_CHAT_ID)
  tvctl pair             connect once so the TV asks Allow; stores the token
  tvctl status           power, volume, open apps
  tvctl key KEY_HOME …   press remote keys
  tvctl vol [N]          show or set the volume
  tvctl open <app>       youtube, netflix, prime, spotify, appletv, twitch, browser
  tvctl close [all]      close background apps (all: every app)
  tvctl yt <link|id>     play a YouTube video on the TV
  tvctl on | off         wake / standby

Environment: TV_HOST (default 192.168.1.157), TV_MAC (for wake-on-LAN),
TVCTL_DATA (state dir, default ~/.config/tvctl), TVCTL_NAME (default tvctl),
TVCTL_MEDIA_PORT (default 8765), TVCTL_MODEL (Claude model, default sonnet;
"off" disables plain-language requests), CLAUDE_CODE_OAUTH_TOKEN.
`

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if len(os.Args) < 2 || os.Args[1] == "-h" || os.Args[1] == "--help" || os.Args[1] == "help" {
		fmt.Print(usage)
		return
	}
	home, _ := os.UserHomeDir()
	data := env("TVCTL_DATA", filepath.Join(home, ".config", "tvctl"))
	if err := os.MkdirAll(data, 0o700); err != nil {
		fail(err)
	}
	t := tv.New(env("TV_HOST", "192.168.1.157"), env("TV_MAC", "F4:DD:06:1A:7D:FA"), env("TVCTL_NAME", "tvctl"), filepath.Join(data, "tv-token"))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cmd, args := os.Args[1], os.Args[2:]
	arg := strings.Join(args, " ")

	switch cmd {
	case "serve":
		serve(ctx, t, data, log)
	case "version":
		fmt.Println(version)
	case "pair":
		fmt.Println("Connecting… if the TV asks, press Allow with the real remote.")
		check(t.Pair(ctx))
		fmt.Println("Paired; the token is in", t.TokenFile)
	case "status":
		info, err := t.Info(ctx)
		check(err)
		vol, muted, _ := t.Volume(ctx)
		fmt.Printf("%s (%s) %s, volume %d, muted %v\n", info.Name, info.Model, info.PowerState, vol, muted)
		apps, err := t.Running(ctx)
		check(err)
		for _, a := range apps {
			fmt.Printf("  %s running, on screen %v\n", a.Name, a.Visible)
		}
	case "key":
		check(t.Keys(ctx, args...))
	case "vol":
		if arg == "" {
			v, m, err := t.Volume(ctx)
			check(err)
			fmt.Println(v, map[bool]string{true: "(muted)", false: ""}[m])
			return
		}
		n, err := strconv.Atoi(arg)
		check(err)
		check(t.SetVolume(ctx, n))
	case "open":
		a, ok := tv.LookupApp(arg)
		if !ok {
			fail(fmt.Errorf("unknown app %q", arg))
		}
		check(t.Launch(ctx, a))
	case "close":
		closed, err := t.CloseBackground(ctx, arg == "all")
		check(err)
		fmt.Println("closed:", strings.Join(closed, ", "))
	case "yt":
		id, ok := tv.YouTubeID(arg)
		if !ok {
			fail(fmt.Errorf("not a YouTube link: %q", arg))
		}
		check(t.PlayYouTube(ctx, id))
	case "on":
		check(t.On(ctx))
	case "off":
		check(t.Off(ctx))
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}

func serve(ctx context.Context, t *tv.TV, data string, log *slog.Logger) {
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	chat, err := telegram.ParseChatID(os.Getenv("TELEGRAM_CHAT_ID"))
	if token == "" || err != nil {
		fail(fmt.Errorf("TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID are required"))
	}
	port, _ := strconv.Atoi(env("TVCTL_MEDIA_PORT", "8765"))
	tg := telegram.New(token, chat, log)
	b := &bot.Bot{TV: t, TG: tg, MediaDir: filepath.Join(data, "media"), MediaPort: port, Log: log}
	if model := env("TVCTL_MODEL", "sonnet"); model != "off" {
		// WebSearch only: it finds a YouTube video for "put on something relaxing".
		b.LLM = &claude.Runner{Model: model, Tools: []string{"WebSearch"}, Timeout: 3 * time.Minute}
	}

	go func() {
		if err := b.ServeMedia(ctx); err != nil {
			log.Error("media server stopped", "err", err)
		}
	}()
	if err := tg.SetCommands(ctx, bot.Commands); err != nil {
		log.Warn("set commands", "err", err)
	}
	offsetFile := filepath.Join(data, "telegram-offset")
	offset := func() int64 {
		b, _ := os.ReadFile(offsetFile)
		n, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
		return n
	}
	save := func(n int64) { _ = os.WriteFile(offsetFile, []byte(strconv.FormatInt(n, 10)), 0o600) }

	ip, _ := t.LocalIP()
	log.Info("tvctl serving", "version", version, "tv", t.Host, "paired", t.Paired(), "media", fmt.Sprintf("http://%s:%d/m/", ip, port), "claude", b.LLM != nil)
	tg.Poll(ctx, b, offset, save)
}

func check(err error) {
	if err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "tvctl:", err)
	os.Exit(1)
}
