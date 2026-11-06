package bot

import (
	"errors"
	"log"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"

	"aion2bot/internal/i18n"
	"aion2bot/internal/lfg"
)

// The leader copies a post from Manage to another start, typically the
// same run next week: open or ended, it becomes a new post on every linked
// server, with the players who were on it in the places they had, only the
// party, or nobody. Everyone who comes along is told, in the thread under
// the new post on their own server or else by DM, with a Leave button.
// The old post stays as it is.

// The choices of the copy form's menu.
const (
	copyAll   = "all"
	copyParty = "party"
	copyNone  = "none"
)

// nextStart is the start a copy is offered: the post's a week later, at
// the same time of day in the leader's time zone, and in the future. A
// post without a start offers none.
func nextStart(at time.Time, zone *time.Location, now time.Time) time.Time {
	if at.IsZero() {
		return time.Time{}
	}
	t := at.In(zone)
	for {
		if t = t.AddDate(0, 0, 7); t.After(now) {
			return t
		}
	}
}

// copyModal asks for the copy's start, filled in with the same time next
// week, whom it takes along, and its note.
func (b *Bot) copyModal(p lfg.Post, zone *time.Location, l i18n.Lang) modal {
	required, optional := true, false
	now := time.Now()
	comps := []discordgo.MessageComponent{
		discordgo.Label{
			Label:       l.T("copy.start"),
			Description: truncate(l.T("details.start_help", zoneLabel(zone, now)), 100),
			Component: discordgo.TextInput{CustomID: "start", Style: discordgo.TextInputShort,
				Value: startInput(nextStart(p.StartAt, zone, now), zone), Placeholder: l.T("details.start_ph"),
				Required: &required, MaxLength: 30},
		},
	}
	if people := len(p.People()) - 1; people > 0 {
		opts := []discordgo.SelectMenuOption{{Label: l.T("copy.who_all"), Value: copyAll, Description: l.N(people, "n.player"),
			Emoji: emoji("👥"), Default: true}}
		if party := len(p.Accepted); party > 0 && party < people {
			opts = append(opts, discordgo.SelectMenuOption{Label: l.T(textFor("copy.who_party", p.Rally)), Value: copyParty,
				Description: l.N(party, "n.player"), Emoji: emoji("✅")})
		}
		opts = append(opts, discordgo.SelectMenuOption{Label: l.T("copy.who_none"), Value: copyNone,
			Description: l.T("copy.who_none_desc"), Emoji: emoji("📣")})
		comps = append(comps, discordgo.Label{
			Label:       l.T("copy.who"),
			Description: l.T("copy.who_help"),
			Component:   modalSelect{CustomID: "who", MinValues: 1, MaxValues: 1, Options: opts},
		})
	}
	comps = append(comps, discordgo.Label{
		Label: l.T("details.note"),
		Component: discordgo.TextInput{CustomID: "note", Style: discordgo.TextInputParagraph, Value: p.Note,
			Placeholder: l.T("details.note_ph"), Required: &optional, MaxLength: 300},
	})
	return modal{
		CustomID:   customID(actCopyForm, p.ID),
		Title:      truncate(l.T("copy.title", b.game.DungeonOrUnknown(p.DungeonID).Name), 45),
		Components: comps,
	}
}

// onCopySubmit posts the copy the leader asked for on every server linked
// with the post's, and tells everyone who comes along. Players blocked on
// the server they joined from, or on the post's, stay behind.
func (b *Bot) onCopySubmit(i *discordgo.Interaction, postID string) error {
	userID, _ := interactionUser(i)
	l := lang(i)
	src, ok := b.store.Post(postID)
	switch {
	case !ok:
		return b.reply(i, notice(colorClosed, errorText(lfg.ErrNotFound, l)))
	case userID != src.LeaderID:
		return b.reply(i, notice(colorClosed, l.T("manage.only_leader")))
	case b.store.Blocked(userID, src.GuildID):
		return b.reply(i, notice(colorClosed, l.T("blocked.post")))
	}
	values := modalValues(i.ModalSubmitData().Components)
	now := time.Now()
	at, err := parseStart(first(values["start"]), b.timeZone(userID, i.Locale), now)
	switch {
	case err != nil || at.IsZero():
		return b.reply(i, notice(colorClosed, l.T("copy.bad_start")))
	case !at.After(now):
		return b.reply(i, notice(colorClosed, l.T("draft.start_past")))
	}
	who := lfg.CopyEveryone
	switch first(values["who"]) {
	case copyParty:
		who = lfg.CopyParty
	case copyNone:
		who = lfg.CopyNobody
	}
	guilds := b.store.Targets(src.GuildID)
	if len(guilds) == 0 {
		return b.reply(i, notice(colorClosed, l.T("draft.nowhere")))
	}

	c := src.Copy(who, at, strings.TrimSpace(first(values["note"])), b.game.PostTTL(), now, func(pl lfg.Player) bool {
		return b.blockedFrom(src, pl.UserID, pl.GuildID)
	})
	// The party hears about the copy now. The players waiting and the
	// reserves are pinged again once they get into the party.
	for _, pl := range c.Accepted {
		c.MarkPinged(pl.UserID)
	}
	p, err := b.store.CreatePost(c, b.game.MaxRunsPerLeader)
	if errors.Is(err, lfg.ErrTooManyPosts) {
		return b.reply(i, b.tooManyRuns(b.store.ActivePostsByLeader(userID), l))
	}
	if err != nil {
		return b.reply(i, notice(colorClosed, errorText(err, l)))
	}

	// Answer within Discord's 3 second limit, then fill in the result. A
	// copy that is full already doesn't ping the servers' roles.
	if err := b.reply(i, notice(colorOpen, l.T("copy.sending", l.N(len(guilds), "n.server")))); err != nil {
		log.Printf("acknowledge copy %s of post %s: %v", p.ID, src.ID, err)
	}
	open := p.Status == lfg.StatusOpen
	sent, failed := b.broadcast(p, guilds, open)
	if sent == 0 {
		b.store.UpdatePost(p.ID, func(p *lfg.Post) error { return p.Close(lfg.StatusClosed, time.Now()) })
		return b.editReply(i, notice(colorClosed, l.T("draft.not_delivered")))
	}
	// The threads to tell everyone in came with the cards.
	if stored, ok := b.store.Post(p.ID); ok {
		p = stored
	}
	msg := l.T("copy.done", b.runName(p), startText(p.StartAt, "", l), l.N(sent, "n.server"))
	if told := b.tellCopied(p); told > 0 {
		msg += l.T("copy.told", l.N(told, "n.player"))
	}
	if failed > 0 {
		msg += l.T("draft.unreachable", l.N(failed, "n.server"))
	}
	err = b.editReply(i, container(colorOpen, text(msg), manageRow(p.ID, l)))
	if open {
		b.sendAlerts(p.ID)
	}
	return err
}

// tellCopied pings everyone who came along to a copy in the thread under it,
// on the server they joined from, mentionBatch per note, or by DM. Every
// thread gets the note. It returns how many were told.
func (b *Bot) tellCopied(p lfg.Post) int {
	people := p.People()[1:]
	if len(people) == 0 {
		return 0
	}
	pings := map[string][]string{}
	for _, pl := range people {
		pings[pl.GuildID] = append(pings[pl.GuildID], pl.UserID)
	}
	b.learnMembers(p, p.GuildIDs())
	b.pingInBatches(p, pings, func(guildID string, users []string, first bool, l i18n.Lang) discordgo.MessageComponent {
		if len(users) == 0 && !first {
			return nil
		}
		return b.copiedNote(p, users, guildID, l)
	}, func(guildID, userID string) bool {
		pl, ok := personOn(p, userID)
		return ok && b.dm(userID, b.copiedDM(p, pl, b.guildLang(guildID)))
	})
	return len(people)
}

// copiedNote tells a thread the leader copied a run to a new start, and
// the players pinged with it that they're on it again.
func (b *Bot) copiedNote(p lfg.Post, users []string, guildID string, l i18n.Lang) discordgo.MessageComponent {
	leader := who(p.LeaderID, p.LeaderName, b.mentionable(p.LeaderID, p.GuildID, guildID))
	head := l.T("thread.copied_title") + "\n" + l.T("thread.copied", leader, b.runName(p))
	if len(users) > 0 {
		mentions := make([]string, 0, len(users))
		for _, id := range users {
			mentions = append(mentions, "<@"+id+">")
		}
		head += "\n" + l.T("thread.copied_you", strings.Join(mentions, " "))
	}
	return container(statusColor(p.Status),
		text(head),
		divider(),
		discordgo.Section{
			Components: []discordgo.MessageComponent{text(l.T("card.start") + postStart(p, l) + "\n" + b.statusLine(p, l) + "\n" +
				l.T("thread.copied_hint"))},
			Accessory: leaveButton(p.ID, l),
		},
	)
}

// copiedDM tells a player by DM that they're on the copy, where, and how to
// leave it.
func (b *Bot) copiedDM(p lfg.Post, pl lfg.Player, l i18n.Lang) discordgo.MessageComponent {
	return container(statusColor(p.Status),
		text(l.T("dm.copied", b.runName(p), escape(p.LeaderName), p.LeaderID, b.bringing(p, pl), b.mineStanding(p, pl.UserID, l),
			postStart(p, l))+leaderCharLine(p, l)+l.T("dm.copied_hint")),
		discordgo.ActionsRow{Components: []discordgo.MessageComponent{leaveButton(p.ID, l)}},
	)
}

// leaveButton takes the player who clicks it off a post, like the card's.
func leaveButton(postID string, l i18n.Lang) discordgo.Button {
	return discordgo.Button{CustomID: customID(actLeave, postID), Label: l.T("card.leave"), Style: discordgo.SecondaryButton}
}
