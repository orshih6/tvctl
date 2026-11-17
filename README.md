# tvctl

A Telegram remote for a Samsung TV (Tizen). You run it on a machine on the same network
as the TV, talk to your own Telegram bot, and the TV does it:

- `/remote`: buttons for arrows, OK, Back, Home, volume, play/pause, apps
- **send a photo, video or song**: it shows or plays on the TV
- **send a YouTube link**: it plays on the TV
- `/status` · `/vol 10` · `/mute` · `/open netflix` · `/close` (background apps) · `/on` · `/off` · `/stop`
- **plain words**: "turn it down a bit", "put on lofi music", "close everything and open YouTube".
  Claude (`claude -p`, WebSearch only) turns it into the same actions as the buttons.

The same commands work from a terminal: `tvctl status`, `tvctl key KEY_HOME`,
`tvctl yt <link>`, … (`tvctl help`).

Built and tested against a 2024 Samsung UE55DU8500 (Tizen 8). Other recent Samsung TVs
speak the same protocols but are untested.

## How it reaches the TV

No cloud and no Samsung account: everything is the TV's own LAN interfaces.

| What | How |
|---|---|
| Apps (open, close, status) | REST on `:8001` |
| Remote keys | the paired remote websocket on `:8002` |
| Volume, mute, showing media by URL | UPnP/DLNA on `:9197` |
| YouTube by video id | DIAL on `:8080` |
| Power on | Wake-on-LAN, then the power key |

Media you send is saved under a random 128-bit name, served to the TV from a small HTTP
server on the machine running tvctl, and deleted after a day.

## Setup

You need Go 1.27+, a Samsung TV on your network, and a Telegram bot of your own.

1. **Create a bot** with [@BotFather](https://t.me/BotFather) and note its token. Use a
   bot only for tvctl: two programs polling one bot steal each other's messages.
2. **Find your chat id**: message your bot, then open
   `https://api.telegram.org/bot<token>/getUpdates` and read `message.chat.id`.
3. **Find the TV's address and MAC** in the TV's Settings → General → Network → Network
   Status → IP Settings (give it a DHCP reservation on your router so it stays put).
4. **Run it:**

```bash
make build

export TV_HOST=192.168.1.50            # the TV
export TV_MAC=AA:BB:CC:DD:EE:FF        # for /on (wake-on-LAN)
export TELEGRAM_BOT_TOKEN=123456:ABC…
export TELEGRAM_CHAT_ID=123456789      # the only chat the bot listens to

bin/tvctl pair     # the TV asks "Allow tvctl?" — press Allow with the real remote
bin/tvctl serve
```

For `/on` to work, turn on Settings → General → External Device Manager → Device
Connection Manager → **Power On with Mobile** on the TV.

### Settings

| Variable | Meaning |
|---|---|
| `TV_HOST` | the TV's address (required) |
| `TV_MAC` | the TV's MAC address, for wake-on-LAN |
| `TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID` | the bot, and the one chat it obeys (`serve` only) |
| `TVCTL_DATA` | where the pairing token and media live (default `~/.config/tvctl`) |
| `TVCTL_NAME` | how the remote appears in the TV's device list (default `tvctl`) |
| `TVCTL_MEDIA_PORT` | port the TV fetches media from (default `8765`) |
| `TVCTL_MODEL` | Claude model for plain-language requests (default `sonnet`); `off` disables them |
| `CLAUDE_CODE_OAUTH_TOKEN` | credential for Claude Code, when it isn't already logged in |

Plain-language requests need [Claude Code](https://claude.com/claude-code) installed
(`claude` on the `PATH`). Without it, or with `TVCTL_MODEL=off`, commands, buttons, media
and YouTube links all still work.

### Docker

The `Dockerfile` builds an image with tvctl and Claude Code. Run it with host networking,
because the TV has to reach the media port and wake-on-LAN is a broadcast:

```bash
docker build -t tvctl .
docker run -d --network host -v tvctl-data:/data \
  -e TV_HOST -e TV_MAC -e TELEGRAM_BOT_TOKEN -e TELEGRAM_CHAT_ID tvctl
```

`k8s/README.md` describes how the author runs it on a home Kubernetes node; it is specific
to that cluster.

## Safety

- Only `TELEGRAM_CHAT_ID` is heard. Every other chat is ignored without a reply.
- Volume jumps are capped (no more than +10 above 40 at once).
- Claude can only search the web and choose from the same small action set as the
  buttons; it cannot run anything else.
- The media port is open to your LAN. The random file names are the only guard, so don't
  expose that port to the internet.

## Development

```bash
make check     # gofmt, vet, tests
make build     # bin/tvctl
```

## License

MIT. See [LICENSE](LICENSE).
