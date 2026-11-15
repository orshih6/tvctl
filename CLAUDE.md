# tvctl

Telegram remote for the Samsung TV at home. Go, no database. Deploy: `k8s/README.md`.

Invariants:
- **Only `TELEGRAM_CHAT_ID` is heard.** Everyone else is ignored without a reply. Never
  add a way for another chat to control the TV.
- **It must run on the home network.** The TV only listens on the LAN, so the Deployment
  is pinned to `ori-minipc` with `hostNetwork`. Never move it to `vmi2981248`.
- **Media files have random 128-bit names, no directory listing, and are deleted after a
  day.** The media port is reachable by anything on the LAN/tailnet; the names are the
  only guard.
- **Volume jumps are capped** (no more than +10 above 40 at once) so a typo or a model
  mistake can't blast the room.
- Claude gets **WebSearch only** (to find a YouTube video id) and never invents an id;
  everything it does goes through the same small action set as the buttons.
- The TV quirks are load-bearing, each with a comment in `internal/tv`: DIAL needs the
  YouTube app's `Origin`, DLNA wants named entities (`&quot;`, not `&#34;`), and `Play`
  answers "Transition not available" when the TV already started the new URI.
- Use a bot dedicated to tvctl (@orshih_tvctl_bot), never the notification bot.

`make check` before finishing work.
