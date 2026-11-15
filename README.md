# tvctl

A Telegram remote for the Samsung TV at home (UE55DU8500, Tizen), as **@orshih_tvctl_bot**:

- `/remote`: buttons for arrows, OK, Back, Home, volume, play/pause, apps
- **send a photo, video or song**: it shows or plays on the TV
- **send a YouTube link**: it plays on the TV
- `/status` · `/vol 10` · `/mute` · `/open netflix` · `/close` (background apps) · `/on` · `/off` · `/stop`
- **plain words**: "turn it down a bit", "put on lofi music", "close everything and open YouTube".
  Claude (`claude -p`, WebSearch only, on the subscription) turns it into actions.

The same commands work from a terminal on the home network: `tvctl status`, `tvctl key KEY_HOME`,
`tvctl yt <link>`, … (`tvctl help`).

How it reaches the TV: REST on :8001 (apps), the paired remote websocket on :8002 (keys),
UPnP/DLNA on :9197 (volume, showing media by URL) and DIAL on :8080 (YouTube). It runs
on `ori-minipc` at home; see `k8s/README.md`.

```bash
make check     # gofmt, vet, tests
make build     # bin/tvctl
```
