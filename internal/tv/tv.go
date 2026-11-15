// Package tv talks to a Samsung Tizen TV on the local network, over the four
// interfaces such a TV exposes:
//
//   - REST on :8001 (device info, app status, launch, close). No pairing.
//   - The remote-control websocket on :8002 (key presses). Paired once: the
//     first connection makes the TV ask "Allow?", and the token it then hands
//     out is reused so it never asks again.
//   - UPnP/DLNA on :9197 (volume, mute, showing a photo or video by URL).
//   - DIAL on :8080 (starting YouTube on a given video).
//
// None of these can read the screen; Samsung offers no screenshot.
package tv

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

type TV struct {
	Host string // e.g. 192.168.1.157
	MAC  string // Wi-Fi or Ethernet MAC, for Wake-on-LAN
	// Name is how this client appears in the TV's device list.
	Name string
	// TokenFile keeps the pairing token between runs.
	TokenFile string

	http *http.Client
	mu   sync.Mutex // one websocket session at a time
}

func New(host, mac, name, tokenFile string) *TV {
	return &TV{Host: host, MAC: mac, Name: name, TokenFile: tokenFile, http: &http.Client{Timeout: 8 * time.Second}}
}

// Apps the bot knows by name. IDs are the TV's own (see `tvctl apps`).
var Apps = map[string]App{
	"youtube": {"111299001912", "YouTube"},
	"netflix": {"3201907018807", "Netflix"},
	"prime":   {"3201910019365", "Prime Video"},
	"spotify": {"3201606009684", "Spotify"},
	"appletv": {"3201807016597", "Apple TV"},
	"twitch":  {"3202203026841", "Twitch"},
	"browser": {"3202010022079", "Browser"},
}

type App struct{ ID, Name string }

// LookupApp finds an app by a loose name ("YouTube", "prime video", "you tube").
func LookupApp(name string) (App, bool) {
	k := strings.NewReplacer(" ", "", "-", "", "_", "", "video", "").Replace(strings.ToLower(name))
	if a, ok := Apps[k]; ok {
		return a, true
	}
	for key, a := range Apps {
		if strings.HasPrefix(key, k) && len(k) >= 3 {
			return a, true
		}
	}
	return App{}, false
}

// ---- REST ------------------------------------------------------------------

type Info struct {
	Name       string
	Model      string
	PowerState string // "on" or "standby"
	Network    string
}

// Info reads the device description. An error means the TV is off (or
// unplugged) or not on this network.
func (t *TV) Info(ctx context.Context) (Info, error) {
	var r struct {
		Device struct {
			Name        string `json:"name"`
			ModelName   string `json:"modelName"`
			PowerState  string `json:"PowerState"`
			NetworkType string `json:"networkType"`
		} `json:"device"`
	}
	if err := t.getJSON(ctx, "http://"+t.Host+":8001/api/v2/", &r); err != nil {
		return Info{}, err
	}
	return Info{html.UnescapeString(r.Device.Name), r.Device.ModelName, r.Device.PowerState, r.Device.NetworkType}, nil
}

type AppStatus struct {
	App
	Installed, Running, Visible bool
}

func (t *TV) AppStatus(ctx context.Context, a App) (AppStatus, error) {
	var r struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Running bool   `json:"running"`
		Visible bool   `json:"visible"`
		Code    int    `json:"code"`
	}
	if err := t.getJSON(ctx, "http://"+t.Host+":8001/api/v2/applications/"+a.ID, &r); err != nil {
		return AppStatus{App: a}, err
	}
	if r.Code == 404 || r.ID == "" {
		return AppStatus{App: a}, nil
	}
	return AppStatus{App: App{r.ID, r.Name}, Installed: true, Running: r.Running, Visible: r.Visible}, nil
}

// Running lists the known apps that are open, the one on screen first.
func (t *TV) Running(ctx context.Context) ([]AppStatus, error) {
	var out []AppStatus
	for _, a := range Apps {
		s, err := t.AppStatus(ctx, a)
		if err != nil {
			return nil, err
		}
		if s.Running {
			if s.Visible {
				out = append([]AppStatus{s}, out...)
			} else {
				out = append(out, s)
			}
		}
	}
	return out, nil
}

func (t *TV) Launch(ctx context.Context, a App) error {
	return t.do(ctx, "POST", "http://"+t.Host+":8001/api/v2/applications/"+a.ID, "", nil)
}

func (t *TV) Close(ctx context.Context, a App) error {
	return t.do(ctx, "DELETE", "http://"+t.Host+":8001/api/v2/applications/"+a.ID, "", nil)
}

// CloseBackground closes every known app that is running but not on screen,
// and returns their names. This is what frees memory for the browser.
func (t *TV) CloseBackground(ctx context.Context, includeVisible bool) ([]string, error) {
	running, err := t.Running(ctx)
	if err != nil {
		return nil, err
	}
	var closed []string
	for _, s := range running {
		if s.Visible && !includeVisible {
			continue
		}
		if err := t.Close(ctx, s.App); err != nil {
			return closed, fmt.Errorf("close %s: %w", s.Name, err)
		}
		closed = append(closed, s.Name)
	}
	return closed, nil
}

// ---- DIAL ------------------------------------------------------------------

var ytID = regexp.MustCompile(`(?:v=|youtu\.be/|/shorts/|/live/|/embed/)([A-Za-z0-9_-]{11})`)

// YouTubeID pulls the video id out of a link, or accepts a bare id.
func YouTubeID(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if m := ytID.FindStringSubmatch(s); m != nil {
		return m[1], true
	}
	if regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`).MatchString(s) {
		return s, true
	}
	return "", false
}

// PlayYouTube opens YouTube on the TV straight into a video. The TV answers
// 403 to a DIAL launch without an Origin; it accepts the one the YouTube
// phone app sends.
func (t *TV) PlayYouTube(ctx context.Context, videoID string) error {
	req, err := http.NewRequestWithContext(ctx, "POST", "http://"+t.Host+":8080/ws/apps/YouTube", strings.NewReader("v="+url.QueryEscape(videoID)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", `text/plain; charset="utf-8"`)
	req.Header.Set("Origin", "package:com.google.android.youtube")
	resp, err := t.http.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("YouTube on the TV refused the video (HTTP %d)", resp.StatusCode)
	}
	return nil
}

// ---- DLNA ------------------------------------------------------------------

func (t *TV) soap(ctx context.Context, service, action, args string) (string, error) {
	ctl := map[string]string{"AVTransport": "/upnp/control/AVTransport1", "RenderingControl": "/upnp/control/RenderingControl1"}[service]
	body := `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body>` +
		`<u:` + action + ` xmlns:u="urn:schemas-upnp-org:service:` + service + `:1"><InstanceID>0</InstanceID>` + args + `</u:` + action + `></s:Body></s:Envelope>`
	req, err := http.NewRequestWithContext(ctx, "POST", "http://"+t.Host+":9197"+ctl, strings.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	req.Header.Set("SOAPACTION", `"urn:schemas-upnp-org:service:`+service+`:1#`+action+`"`)
	resp, err := t.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("%s: HTTP %d: %s", action, resp.StatusCode, tag(string(b), "errorDescription"))
	}
	return string(b), nil
}

func tag(s, name string) string {
	_, rest, ok := strings.Cut(s, "<"+name+">")
	if !ok {
		return ""
	}
	v, _, _ := strings.Cut(rest, "</"+name+">")
	return v
}

func (t *TV) Volume(ctx context.Context) (int, bool, error) {
	v, err := t.soap(ctx, "RenderingControl", "GetVolume", "<Channel>Master</Channel>")
	if err != nil {
		return 0, false, err
	}
	m, err := t.soap(ctx, "RenderingControl", "GetMute", "<Channel>Master</Channel>")
	if err != nil {
		return 0, false, err
	}
	n, _ := strconv.Atoi(tag(v, "CurrentVolume"))
	return n, tag(m, "CurrentMute") == "1", nil
}

func (t *TV) SetVolume(ctx context.Context, n int) error {
	n = max(0, min(n, 100))
	_, err := t.soap(ctx, "RenderingControl", "SetVolume", "<Channel>Master</Channel><DesiredVolume>"+strconv.Itoa(n)+"</DesiredVolume>")
	return err
}

func (t *TV) SetMute(ctx context.Context, on bool) error {
	v := "0"
	if on {
		v = "1"
	}
	_, err := t.soap(ctx, "RenderingControl", "SetMute", "<Channel>Master</Channel><DesiredMute>"+v+"</DesiredMute>")
	return err
}

// Cast makes the TV fetch and show a media URL (photo, video or audio). The
// URL must be reachable from the TV, i.e. on the home network.
func (t *TV) Cast(ctx context.Context, mediaURL, mime, title string) error {
	class := map[string]string{"image": "object.item.imageItem.photo", "video": "object.item.videoItem", "audio": "object.item.audioItem.musicTrack"}[strings.SplitN(mime, "/", 2)[0]]
	if class == "" {
		return fmt.Errorf("the TV can't show %s", mime)
	}
	var esc bytes.Buffer
	_ = xml.EscapeText(&esc, []byte(title))
	didl := `<DIDL-Lite xmlns="urn:schemas-upnp-org:metadata-1-0/DIDL-Lite/" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:upnp="urn:schemas-upnp-org:metadata-1-0/upnp/">` +
		`<item id="1" parentID="0" restricted="1"><dc:title>` + esc.String() + `</dc:title><upnp:class>` + class + `</upnp:class>` +
		`<res protocolInfo="http-get:*:` + mime + `:*">` + xmlEsc(mediaURL) + `</res></item></DIDL-Lite>`
	if _, err := t.soap(ctx, "AVTransport", "SetAVTransportURI",
		"<CurrentURI>"+xmlEsc(mediaURL)+"</CurrentURI><CurrentURIMetaData>"+xmlEsc(didl)+"</CurrentURIMetaData>"); err != nil {
		return err
	}
	_, err := t.soap(ctx, "AVTransport", "Play", "<Speed>1</Speed>")
	if err != nil && strings.Contains(err.Error(), "Transition not available") {
		// When something was already showing, the TV starts the new URI by
		// itself and refuses Play; that's success if it is playing now.
		if st, serr := t.soap(ctx, "AVTransport", "GetTransportInfo", ""); serr == nil {
			if s := tag(st, "CurrentTransportState"); s == "PLAYING" || s == "TRANSITIONING" {
				return nil
			}
		}
	}
	return err
}

// xmlEsc escapes with named entities only: the TV answers "Invalid Args" to
// the &#34; that html.EscapeString writes for a quote.
var xmlEsc = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;").Replace

func (t *TV) StopCast(ctx context.Context) error {
	_, err := t.soap(ctx, "AVTransport", "Stop", "")
	return err
}

// ---- websocket remote --------------------------------------------------------

// ErrNotAllowed means the TV refused the remote: nobody pressed Allow, or the
// device was removed from the TV's list.
var ErrNotAllowed = errors.New("the TV did not allow the remote (press Allow on the TV, then try again)")

// Keys presses remote buttons in order (KEY_VOLUP, KEY_HOME, …), with a short
// gap so the TV registers each one.
func (t *TV) Keys(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second) // a first pairing waits for Allow
	defer cancel()
	conn, err := t.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	for i, k := range keys {
		if i > 0 {
			time.Sleep(350 * time.Millisecond)
		}
		msg, _ := json.Marshal(map[string]any{"method": "ms.remote.control", "params": map[string]string{
			"Cmd": "Click", "DataOfCmd": k, "Option": "false", "TypeOfRemote": "SendRemoteKey"}})
		if err := conn.Write(ctx, websocket.MessageText, msg); err != nil {
			return fmt.Errorf("send %s: %w", k, err)
		}
	}
	time.Sleep(300 * time.Millisecond) // let the last press land before closing
	return nil
}

// Pair connects once so the TV asks Allow, and stores the token.
func (t *TV) Pair(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	conn, err := t.dial(ctx)
	if err != nil {
		return err
	}
	return conn.Close(websocket.StatusNormalClosure, "")
}

func (t *TV) dial(ctx context.Context) (*websocket.Conn, error) {
	q := url.Values{"name": {base64.StdEncoding.EncodeToString([]byte(t.Name))}}
	if tok := t.token(); tok != "" {
		q.Set("token", tok)
	}
	u := "wss://" + t.Host + ":8002/api/v2/channels/samsung.remote.control?" + q.Encode()
	// The TV's certificate is self-signed by Samsung; there is no CA to check it against.
	hc := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	conn, _, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPClient: hc})
	if err != nil {
		return nil, fmt.Errorf("connect to the TV remote: %w", err)
	}
	conn.SetReadLimit(1 << 20)
	// The first message says whether we're in: ms.channel.connect (with a
	// token on first pairing) or ms.channel.unauthorized.
	for {
		_, b, err := conn.Read(ctx)
		if err != nil {
			conn.CloseNow()
			if ctx.Err() != nil {
				return nil, ErrNotAllowed
			}
			return nil, fmt.Errorf("TV remote: %w", err)
		}
		var m struct {
			Event string `json:"event"`
			Data  struct {
				Token string `json:"token"`
			} `json:"data"`
		}
		_ = json.Unmarshal(b, &m)
		switch m.Event {
		case "ms.channel.connect":
			if m.Data.Token != "" && m.Data.Token != t.token() {
				t.saveToken(m.Data.Token)
			}
			go drain(conn) // keep reading so the TV's pings are answered
			return conn, nil
		case "ms.channel.unauthorized", "ms.channel.timeOut":
			conn.CloseNow()
			return nil, ErrNotAllowed
		}
	}
}

func drain(c *websocket.Conn) {
	for {
		if _, _, err := c.Read(context.Background()); err != nil {
			return
		}
	}
}

func (t *TV) token() string {
	b, _ := os.ReadFile(t.TokenFile)
	return strings.TrimSpace(string(b))
}

func (t *TV) saveToken(tok string) { _ = os.WriteFile(t.TokenFile, []byte(tok+"\n"), 0o600) }

// Paired reports whether a token is stored.
func (t *TV) Paired() bool { return t.token() != "" }

// ---- power -------------------------------------------------------------------

// On wakes the TV: a Wake-on-LAN packet (works when "Power On with Mobile" is
// enabled), then, if it answers in standby, the power key.
func (t *TV) On(ctx context.Context) error {
	if info, err := t.Info(ctx); err == nil && info.PowerState == "on" {
		return nil
	}
	wolErr := t.wake()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if info, err := t.Info(ctx); err == nil {
			if info.PowerState == "on" {
				return nil
			}
			_ = t.Keys(ctx, "KEY_POWER")
			time.Sleep(4 * time.Second)
			if info, err := t.Info(ctx); err == nil && info.PowerState == "on" {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	if wolErr != nil {
		return fmt.Errorf("the TV didn't wake (wake-on-LAN: %v)", wolErr)
	}
	return errors.New("the TV didn't wake. On the TV, turn on Settings → General → External Device Manager → Device Connection Manager → Power On with Mobile")
}

// Off puts the TV in standby.
func (t *TV) Off(ctx context.Context) error {
	if info, err := t.Info(ctx); err != nil || info.PowerState != "on" {
		return nil
	}
	return t.Keys(ctx, "KEY_POWER")
}

func (t *TV) wake() error {
	hw, err := net.ParseMAC(t.MAC)
	if err != nil {
		return fmt.Errorf("TV_MAC %q: %w", t.MAC, err)
	}
	pkt := bytes.Repeat([]byte{0xff}, 6)
	for range 16 {
		pkt = append(pkt, hw...)
	}
	var last error
	for _, addr := range []string{"255.255.255.255:9", t.Host + ":9", broadcastOf(t.Host) + ":9"} {
		c, err := net.Dial("udp", addr)
		if err != nil {
			last = err
			continue
		}
		_, err = c.Write(pkt)
		c.Close()
		if err != nil {
			last = err
		}
	}
	return last
}

func broadcastOf(host string) string {
	ip := net.ParseIP(host).To4()
	if ip == nil {
		return host
	}
	return fmt.Sprintf("%d.%d.%d.255", ip[0], ip[1], ip[2])
}

// LocalIP is this machine's address on the TV's network: what the TV must use
// to fetch something we serve.
func (t *TV) LocalIP() (string, error) {
	c, err := net.Dial("udp", t.Host+":9")
	if err != nil {
		return "", err
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).IP.String(), nil
}

// ---- http helpers --------------------------------------------------------------

func (t *TV) getJSON(ctx context.Context, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return err
	}
	resp, err := t.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

func (t *TV) do(ctx context.Context, method, u, ctype string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	resp, err := t.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: HTTP %d %s", method, u, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}
