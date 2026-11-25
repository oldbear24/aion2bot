package bot

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gorilla/websocket"
)

func watchedBot(t *testing.T, token string) *Bot {
	t.Helper()
	s, err := discordgo.New("Bot " + token)
	if err != nil {
		t.Fatal(err)
	}
	return &Bot{s: s, stop: make(chan struct{})}
}

// lost runs the watchdog and reports whether it gave the gateway up within
// the time given; if not, the watchdog is stopped.
func lost(b *Bot, every time.Duration, limit int, within time.Duration) bool {
	gave := make(chan bool, 1)
	go func() { gave <- b.gatewayLost(every, limit) }()
	select {
	case up := <-gave:
		return up
	case <-time.After(within):
		close(b.stop)
		return <-gave
	}
}

func TestGatewayIsLostWhenItStaysDown(t *testing.T) {
	t.Run("never connected", func(t *testing.T) {
		if !lost(watchedBot(t, "test"), time.Millisecond, 5, 5*time.Second) {
			t.Error("a gateway that was never up isn't given up")
		}
	})
	t.Run("connected", func(t *testing.T) {
		b := watchedBot(t, "test")
		b.s.DataReady = true
		if lost(b, time.Millisecond, 5, 100*time.Millisecond) {
			t.Error("a gateway that is up was given up")
		}
	})
	t.Run("session lock stuck", func(t *testing.T) {
		b := watchedBot(t, "test")
		b.s.DataReady = true
		b.s.Lock()
		if !lost(b, time.Millisecond, 5, 5*time.Second) {
			t.Error("a session whose lock is stuck isn't given up")
		}
	})
	t.Run("down for a moment", func(t *testing.T) {
		b := watchedBot(t, "test")
		go func() {
			for i := 0; i < 20; i++ {
				time.Sleep(10 * time.Millisecond)
				b.s.Lock()
				b.s.DataReady = i%2 == 1
				b.s.Unlock()
			}
		}()
		if lost(b, 2*time.Millisecond, 20, 300*time.Millisecond) {
			t.Error("a gateway that came back was given up")
		}
	})
}

// fakeGateway is a Discord gateway that drops each bot's first connection.
// The bot then tries to resume its session: the bot with the token "resumes"
// may, and the bot with the token "invalid" is told Invalid Session, as
// Discord does once it has forgotten a session.
func fakeGateway(t *testing.T) {
	t.Helper()
	var wsURL string
	up := websocket.Upgrader{}
	mux := http.NewServeMux()
	mux.HandleFunc("/gateway", func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bot ")
		json.NewEncoder(w).Encode(map[string]string{"url": wsURL + "/" + token})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		c.WriteJSON(map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": 45000}})
		var first struct {
			Op int `json:"op"`
		}
		if c.ReadJSON(&first) != nil {
			return
		}
		// discordgo ends the gateway's address with a slash.
		token := strings.Trim(r.URL.Path, "/")
		switch {
		case first.Op == 2: // Identify
			c.WriteJSON(map[string]any{"op": 0, "s": 1, "t": "READY", "d": map[string]any{
				"v": 10, "session_id": "session", "resume_gateway_url": wsURL + "/" + token,
				"user": map[string]any{"id": "1", "username": "bot"}, "guilds": []any{},
			}})
			time.Sleep(200 * time.Millisecond)
			return
		case first.Op == 6 && token == "invalid": // Resume
			c.WriteJSON(map[string]any{"op": 9, "d": false})
		case first.Op == 6:
			c.WriteJSON(map[string]any{"op": 0, "s": 2, "t": "RESUMED", "d": map[string]any{}})
		default:
			t.Errorf("the bot %q started with op %d", token, first.Op)
		}
		// Stay connected until the bot hangs up.
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	})
	srv := httptest.NewServer(mux)
	wsURL = "ws" + strings.TrimPrefix(srv.URL, "http")
	old := discordgo.EndpointGateway
	discordgo.EndpointGateway = srv.URL + "/gateway"
	t.Cleanup(func() {
		discordgo.EndpointGateway = old
		srv.CloseClientConnections()
		srv.Close()
	})
}

// discordgo hangs for good when its resume is answered with Invalid Session
// (see watchdog.go); the watchdog is what notices. A resume that works takes
// about a second, and must not be taken for that.
func TestGatewayIsLostWhenResumingHangs(t *testing.T) {
	fakeGateway(t)
	const every, limit = 100 * time.Millisecond, 30
	for token, wantLost := range map[string]bool{"resumes": false, "invalid": true} {
		t.Run(token, func(t *testing.T) {
			t.Parallel()
			b := watchedBot(t, token)
			if err := b.s.Open(); err != nil {
				t.Fatal(err)
			}
			if got := lost(b, every, limit, 5*time.Second); got != wantLost {
				t.Errorf("gateway given up: %v, want %v", got, wantLost)
			}
			if !wantLost {
				if !b.gatewayUp() {
					t.Error("the gateway isn't up after resuming")
				}
				b.s.Close()
			}
		})
	}
}
