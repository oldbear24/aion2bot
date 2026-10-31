package bot

import (
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"

	"aion2bot/internal/i18n"
	"aion2bot/internal/lfg"
)

// Drafts are dropped after this long without a click.
const draftTTL = time.Hour

// draft is a post being filled in on the /lfg panel. Drafts live in memory
// only; after a restart the panel asks the user to run /lfg again.
type draft struct {
	DungeonID  string
	ModeID     string
	LookingFor []string
	Missing    int
	MinGS      int
	Note       string
	// StartAt is when the run starts, zero for "as soon as the party is
	// full". StartInput is what the leader typed, to show it again.
	StartAt    time.Time
	StartInput string
	// Zone is the leader's time zone, which typed start times are read in.
	Zone *time.Location
	// Problem is a catalog key shown on the panel after invalid input.
	Problem string
	touched time.Time
}

func (d draft) zone() *time.Location {
	if d.Zone != nil {
		return d.Zone
	}
	return time.UTC
}

func (b *Bot) getDraft(userID string) (draft, bool) {
	b.draftMu.Lock()
	defer b.draftMu.Unlock()
	d, ok := b.drafts[userID]
	if !ok {
		return draft{}, false
	}
	return *d, true
}

func (b *Bot) putDraft(userID string, d draft) {
	b.draftMu.Lock()
	defer b.draftMu.Unlock()
	d.touched = time.Now()
	b.drafts[userID] = &d
}

func (b *Bot) dropDraft(userID string) {
	b.draftMu.Lock()
	defer b.draftMu.Unlock()
	delete(b.drafts, userID)
}

func (b *Bot) dropStaleDrafts(now time.Time) {
	b.draftMu.Lock()
	defer b.draftMu.Unlock()
	for id, d := range b.drafts {
		if now.Sub(d.touched) > draftTTL {
			delete(b.drafts, id)
		}
	}
}

// selectDungeon switches the draft to a dungeon, starting at its first mode.
func (b *Bot) selectDungeon(d *draft, dungeonID string) {
	dungeon, ok := b.game.Dungeon(dungeonID)
	if !ok {
		return
	}
	d.DungeonID, d.ModeID = dungeon.ID, ""
	if len(dungeon.Modes) > 0 {
		d.ModeID = dungeon.Modes[0].ID
	}
	b.applyMode(d)
}

func (b *Bot) selectMode(d *draft, modeID string) {
	dungeon, _ := b.game.Dungeon(d.DungeonID)
	if _, ok := dungeon.Mode(modeID); ok {
		d.ModeID = modeID
		b.applyMode(d)
	}
}

// applyMode keeps the missing count within the mode's party size. The
// minimum gear score is left alone: it is optional and up to the leader.
func (b *Bot) applyMode(d *draft) {
	d.Missing = min(d.Missing, b.draftMaxMissing(*d))
}

func (b *Bot) draftMaxMissing(d draft) int {
	if dungeon, ok := b.game.Dungeon(d.DungeonID); ok {
		return dungeon.MaxMissing(d.ModeID, b.game.MaxMissing)
	}
	return b.game.MaxMissing
}

// gameMinGS is the gear score the game itself requires for the draft's mode.
func (b *Bot) gameMinGS(d draft) int {
	dungeon, _ := b.game.Dungeon(d.DungeonID)
	m, _ := dungeon.Mode(d.ModeID)
	return m.GS
}

// /lfg opens the create panel, in the leader's language.
func (b *Bot) cmdLFG(i *discordgo.Interaction) error {
	userID, _ := interactionUser(i)
	l := lang(i)
	if open := b.store.ActivePostsByLeader(userID); len(open) >= b.game.MaxRunsPerLeader {
		return b.reply(i, b.tooManyRuns(open, l))
	}
	if len(b.store.Targets(i.GuildID)) == 0 {
		return b.reply(i, notice(colorClosed, l.T("draft.nowhere")))
	}
	d := draft{Missing: 1, Zone: b.timeZone(userID, i.Locale)}
	b.putDraft(userID, d)
	return b.reply(i, b.draftPanel(d, l))
}

func (b *Bot) onDraft(i *discordgo.Interaction, field string, values []string) error {
	userID, name := interactionUser(i)
	l := lang(i)
	d, ok := b.getDraft(userID)
	if !ok {
		return b.update(i, notice(colorClosed, l.T("draft.expired")))
	}
	d.Problem = ""

	switch field {
	case fieldDungeon:
		b.selectDungeon(&d, first(values))
	case fieldMode:
		b.selectMode(&d, first(values))
	case fieldLookingFor:
		d.LookingFor = b.normalizeLookingFor(values, d.LookingFor)
	case fieldMissing:
		if n, err := strconv.Atoi(first(values)); err == nil && n >= 1 {
			d.Missing = min(n, b.draftMaxMissing(d))
		}
	case fieldDetails:
		return b.showModal(i, b.detailsModal(d, l))
	case fieldPost:
		return b.postDraft(i, userID, name, d)
	case fieldCancel:
		b.dropDraft(userID)
		return b.update(i, notice(colorClosed, l.T("draft.cancelled")))
	}
	b.putDraft(userID, d)
	return b.update(i, b.draftPanel(d, l))
}

// onDraftDetails handles the gear score, time & note form. It was opened
// from the panel, so the answer updates the panel in place.
func (b *Bot) onDraftDetails(i *discordgo.Interaction) error {
	userID, _ := interactionUser(i)
	l := lang(i)
	d, ok := b.getDraft(userID)
	if !ok {
		return b.update(i, notice(colorClosed, l.T("draft.expired")))
	}
	values := modalValues(i.ModalSubmitData().Components)
	d.Problem = ""
	d.Note = strings.TrimSpace(first(values["note"]))

	if gs, err := parseGS(first(values["gs"])); err != nil {
		d.Problem = "draft.bad_gs"
	} else {
		d.MinGS = gs
	}

	now := time.Now()
	d.StartInput = strings.TrimSpace(first(values["start"]))
	at, err := parseStart(d.StartInput, d.zone(), now)
	switch {
	case err != nil:
		d.Problem = "draft.bad_start"
	case !at.IsZero() && !at.After(now):
		d.Problem = "draft.start_past"
	default:
		d.StartAt = at
	}
	b.putDraft(userID, d)
	return b.update(i, b.draftPanel(d, l))
}

func (b *Bot) postDraft(i *discordgo.Interaction, userID, name string, d draft) error {
	l := lang(i)
	now := time.Now()
	switch _, ok := b.game.Dungeon(d.DungeonID); {
	case !ok:
		d.Problem = "draft.need_dungeon"
	case len(d.LookingFor) == 0:
		d.Problem = "draft.need_looking"
	case !d.StartAt.IsZero() && !d.StartAt.After(now):
		// The panel may have stayed open past the start time.
		d.Problem = "draft.start_past"
	}
	if d.Problem != "" {
		b.putDraft(userID, d)
		return b.update(i, b.draftPanel(d, l))
	}

	// A run planned for later stays up until well after it starts.
	expires := now.Add(b.game.PostTTL())
	if d.StartAt.After(now) {
		expires = d.StartAt.Add(b.game.PostTTL())
	}
	prof, _ := b.store.Profile(userID)
	p, err := b.store.CreatePost(lfg.Post{
		LeaderID:   userID,
		LeaderName: name,
		LeaderChar: prof.Char,
		GuildID:    i.GuildID,
		GuildName:  b.guildName(i.GuildID),
		DungeonID:  d.DungeonID,
		ModeID:     d.ModeID,
		MinGS:      d.MinGS,
		LookingFor: d.LookingFor,
		Missing:    d.Missing,
		StartAt:    d.StartAt,
		Note:       d.Note,
		Status:     lfg.StatusOpen,
		CreatedAt:  now,
		ExpiresAt:  expires,
	}, b.game.MaxRunsPerLeader)
	if errors.Is(err, lfg.ErrTooManyPosts) {
		b.dropDraft(userID)
		return b.update(i, b.tooManyRuns(b.store.ActivePostsByLeader(userID), l))
	}
	if err != nil {
		return b.update(i, notice(colorClosed, errorText(err, l)))
	}
	b.dropDraft(userID)

	// Answer within Discord's 3 second limit, then fill in the result.
	guilds := b.store.Targets(i.GuildID)
	if err := b.update(i, notice(colorOpen, l.T("draft.sending", l.N(len(guilds), "n.server")))); err != nil {
		log.Printf("acknowledge post %s: %v", p.ID, err)
	}
	sent, failed := b.broadcast(p, guilds, true)

	if sent == 0 {
		b.store.UpdatePost(p.ID, func(p *lfg.Post) error { return p.Close(lfg.StatusClosed, time.Now()) })
		return b.editReply(i, notice(colorClosed, l.T("draft.not_delivered")))
	}
	msg := l.T("draft.live", l.N(sent, "n.server"))
	if failed > 0 {
		msg += l.T("draft.unreachable", l.N(failed, "n.server"))
	}
	err = b.editReply(i, container(colorOpen, text(msg), manageRow(p.ID, l)))
	b.sendAlerts(p.ID)
	return err
}

// parseGS accepts "3000", "3,000", "3 000", "3.2k" and the Czech "3,2k".
// Empty means no requirement.
func parseGS(s string) (int, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.NewReplacer(" ", "", nbsp, "", "+", "", "gs", "").Replace(s)
	if s == "" {
		return 0, nil
	}
	if k, ok := strings.CutSuffix(s, "k"); ok {
		f, err := strconv.ParseFloat(strings.ReplaceAll(k, ",", "."), 64)
		if err != nil || f < 0 || f > 100 {
			return 0, errors.New("invalid gear score")
		}
		return int(f*1000 + 0.5), nil
	}
	n, err := strconv.Atoi(strings.NewReplacer(",", "", ".", "").Replace(s))
	if err != nil || n < 0 || n > 99999 {
		return 0, errors.New("invalid gear score")
	}
	return n, nil
}

// nbsp is the thousands separator Czech gear scores are written with.
var nbsp = string(rune(0xA0))

// tooManyRuns lists the leader's open posts with a Manage button for each,
// so they can close one before starting another.
func (b *Bot) tooManyRuns(open []lfg.Post, l i18n.Lang) discordgo.MessageComponent {
	var lines strings.Builder
	var buttons []discordgo.MessageComponent
	for n, p := range open {
		dungeon := b.game.DungeonOrUnknown(p.DungeonID)
		fmt.Fprintf(&lines, "\n%d. **%s** %s", n+1, dungeon.Name, difficultyLine(dungeon, p.ModeID, 0, l))
		if len(buttons) < 5 {
			buttons = append(buttons, discordgo.Button{CustomID: customID(actManage, p.ID),
				Label: truncate(fmt.Sprintf("%d. %s", n+1, dungeon.Name), 80), Style: discordgo.SecondaryButton})
		}
	}
	comps := []discordgo.MessageComponent{text(l.T("draft.too_many", l.N(len(open), "n.post"), lines.String()))}
	if len(buttons) > 0 {
		comps = append(comps, discordgo.ActionsRow{Components: buttons})
	}
	return container(colorFull, comps...)
}
