package bot

import (
	"log"
	"time"

	"github.com/bwmarrin/discordgo"
)

// discordgo reconnects to the gateway by itself, but not always: when
// Discord answers its attempt to resume the session with Invalid Session,
// it closes the connection while still holding the session's lock for
// opening it, and waits for that lock for good. Reading from a gateway
// that stopped answering halfway through connecting never times out
// either. The bot then stays up but offline: no click or command reaches
// it, and nothing in the log says why.
//
// Nothing gets a session out of that, so the bot exits once the gateway has
// been down for too long, and Docker's restart policy starts it again with
// a new session. Everything it needs is in the database, and it redraws
// the cards on start.

const (
	gatewayCheckEvery = 10 * time.Second
	// A reconnect takes a few seconds. An outage that lasts longer than
	// these checks ends the same way; the bot then comes back with the
	// restarts, which try again every minute at most.
	gatewayChecks = 12
)

// gatewayUp reports whether the gateway connection is open and sending
// heartbeats. A session whose lock is taken counts as down: opening and
// closing the connection hold it, and so does a reconnect that hangs.
func (b *Bot) gatewayUp() bool {
	if !b.s.TryRLock() {
		return false
	}
	defer b.s.RUnlock()
	return b.s.DataReady
}

// gatewayLost waits until the gateway was down at limit checks in a row
// and returns true, or false once the bot stops. Checks are counted, not
// time, so a computer waking from sleep gets its chance to reconnect.
func (b *Bot) gatewayLost(every time.Duration, limit int) bool {
	tick := time.NewTicker(every)
	defer tick.Stop()
	down := 0
	for {
		select {
		case <-b.stop:
			return false
		case <-tick.C:
		}
		if b.gatewayUp() {
			down = 0
		} else if down++; down >= limit {
			return true
		}
	}
}

func (b *Bot) watchGateway() {
	if b.gatewayLost(gatewayCheckEvery, gatewayChecks) {
		log.Fatalf("The connection to Discord has been down for %v and didn't come back. Exiting, so the bot is started again.",
			gatewayCheckEvery*gatewayChecks)
	}
}

// The log tells when the gateway connection went down and came back, which
// discordgo itself only logs as warnings.
func onConnect(*discordgo.Session, *discordgo.Connect) {
	log.Print("Connected to Discord.")
}

func onDisconnect(*discordgo.Session, *discordgo.Disconnect) {
	log.Print("Disconnected from Discord.")
}
