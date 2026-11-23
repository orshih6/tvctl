# tvctl

Telegram remote for a Samsung TV (the author's is at home). Go, no database. **Public repo (MIT)**: never commit an address, MAC, chat id or token. Deploy: `k8s/README.md`.

Invariants:
- **Only `TELEGRAM_CHAT_ID` is heard.** Everyone else is ignored without a reply. Never
  add a way for another chat to control the TV.
- **It must run on the home network.** The TV only listens on the LAN, so the Deployment
  is pinned to `ori-minipc` with `hostNetwork`. Never move it to `vmi2981248`.
- **Media files have random 128-bit names, no directory listing, and are deleted after a
  day.** The media port is reachable by anything on the LAN/tailnet; the names are the
  only guard.
- **Volume jumps are capped** (no more than +10 above 40 at once) so a typo, a model
  mistake or a slipped thumb can't blast the room. Every volume change goes through
  `setVolume`; changes under two seconds apart count as one jump (the slider sends many).
- **The Mini App (`internal/bot/app.go`) is the only thing reachable from the internet.**
  The page is a public empty shell; every `/api/` call must pass `VerifyInitData`
  (Telegram's signature, fresh, the one chat) or gets an empty 401. It serves the page and
  four calls (state, volume, a button action, YouTube search), on its own port: never add
  media, files or an unsigned call to it, and it can do only what the bot's buttons do. It
  plays a search result by its 11-character id only, and the page's one outside image
  source is `i.ytimg.com` (thumbnails).
- **YouTube search (`internal/youtube`) uses the website's own search call**: no API key
  and no account. It only returns ids and titles; never add downloading or a login to it.
- Claude gets **WebSearch only** (to find a YouTube video id) and never invents an id;
  everything it does goes through the same small action set as the buttons.
- The TV quirks are load-bearing, each with a comment in `internal/tv`: DIAL needs the
  YouTube app's `Origin`, DLNA wants named entities (`&quot;`, not `&#34;`), and `Play`
  answers "Transition not available" when the TV already started the new URI. A YouTube app that
  is already open ignores a DIAL launch (it answers 200 and does nothing), so
  `PlayYouTube` sends it the video over YouTube's cast channel (`internal/tv/lounge.go`).
- Use a bot dedicated to tvctl (@orshih_tvctl_bot), never the notification bot.

`make check` before finishing work.
