package bot

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"

	"aion2bot/internal/lfg"
)

// A reply relayed to the other servers starts with a small line like the
// one Discord shows over replies: who wrote the message it answers and how
// that starts, linked to the message's copy in the thread there. Webhooks
// can't reply, so the line stands in for it; where the bot relays a
// message itself, it replies to the copy instead.

const (
	// maxQuote bounds the line a relayed reply starts with; quoteLine stays
	// well within it.
	maxQuote     = 300
	maxQuoteName = 40
	maxExcerpt   = 100
	quotePrefix  = "-# ↪ "
)

// replyTo is the player's message a reply answers, with its copies.
type replyTo struct {
	source  lfg.Relayed
	copies  []lfg.Relay
	excerpt string
}

// repliedTo finds the message a player's reply answers, when it's a
// player's message: one written in the thread, or a copy of one from
// another server. The bot's notes show on each server apart, so a reply to
// one gets no line.
func (b *Bot) repliedTo(m *discordgo.Message) *replyTo {
	if m.Type != discordgo.MessageTypeReply || m.MessageReference == nil || m.MessageReference.MessageID == "" {
		return nil
	}
	r := m.ReferencedMessage
	if r == nil {
		// Discord didn't send it along, or it's gone.
		var err error
		if r, err = b.s.ChannelMessage(m.ChannelID, m.MessageReference.MessageID); err != nil {
			return nil
		}
	}
	src, copies, relayed := b.store.RelayGroup(r.ID)
	to := &replyTo{source: src, copies: copies}
	switch {
	case relayed && src.MessageID != r.ID:
		// Messages relayed by earlier versions have no author recorded;
		// their webhook copies show it.
		if to.source.Author == "" && r.WebhookID != "" && r.Author != nil {
			to.source.Author = r.Author.Username
		}
		to.excerpt = excerptOf(relayedText(r))
	case r.Author != nil && !r.Author.Bot && r.WebhookID == "":
		to.source.MessageID, to.source.GuildID, to.source.ChannelID, to.source.AuthorID = r.ID, m.GuildID, m.ChannelID, r.Author.ID
		if to.source.Author == "" {
			to.source.Author = authorName(r)
		}
		to.excerpt = excerptOf(b.relayContent(r))
	default:
		return nil
	}
	if to.source.Author == "" {
		return nil
	}
	return to
}

// quoteIn is how a reply's copy in a thread on a server shows what it
// answers: the line it starts with, and the copy of that message in the
// thread, if there is one, for the bot to reply to. A message that wasn't
// relayed there because its author is blocked there isn't quoted either.
func (b *Bot) quoteIn(to *replyTo, guildID, threadID string) (quote, replyID string) {
	if to == nil {
		return "", ""
	}
	if to.source.ChannelID == threadID {
		replyID = to.source.MessageID
	}
	for _, c := range to.copies {
		if c.ChannelID == threadID {
			replyID = c.MessageID
		}
	}
	if replyID == "" && b.store.Blocked(to.source.AuthorID, guildID) {
		return "", ""
	}
	link := ""
	if replyID != "" {
		link = "https://discord.com/channels/" + guildID + "/" + threadID + "/" + replyID
	}
	return quoteLine(to.source.Author, to.excerpt, link), replyID
}

// quoteLine is "-# ↪ Name: how the message starts", the name linked to
// the message when there's a link.
func quoteLine(name, excerpt, link string) string {
	name = escape(truncate(name, maxQuoteName))
	who := "**" + name + "**"
	if link != "" {
		who = "[" + strings.NewReplacer("[", "", "]", "").Replace(name) + "](" + link + ")"
	}
	line := quotePrefix + who
	if excerpt != "" {
		line += ": " + excerpt
	}
	return line
}

// relayHead is the name the bot leads the messages it relays itself with:
// "**Name** · *Server*: ", escaped.
var relayHead = regexp.MustCompile(`^\*\*(?:\\.|[^*\\])*\*\*(?: · \*(?:\\.|[^*\\])*\*)?: `)

// relayedText is the text of a relayed copy, without the line a relayed
// reply starts with, or the name the bot leads its own copies with.
func relayedText(r *discordgo.Message) string {
	text := r.Content
	if strings.HasPrefix(text, quotePrefix) {
		_, text, _ = strings.Cut(text, "\n")
	}
	if r.WebhookID == "" {
		text = relayHead.ReplaceAllString(text, "")
	}
	return text
}

var (
	maskedLink = regexp.MustCompile(`\[([^\]]*)\]\(<?https?://[^)\s]*>?\)`)
	// blockMarks start quotes, headings, subtext and lists, which would
	// break the line in the middle of one.
	blockMarks = regexp.MustCompile(`^(?:>>> |> |-# |#{1,3} |[-*] )+`)
)

// excerptOf is how a message starts, on one line of at most maxExcerpt
// characters: links don't unfold into previews there, attachments show as
// 📎, and a spoiler is never cut open.
func excerptOf(text string) string {
	text = strings.ReplaceAll(maskedLink.ReplaceAllString(text, "$1"), "```", "`")
	var words []string
	length, cut := 0, false
	for line := range strings.Lines(text) {
		for _, w := range strings.Fields(blockMarks.ReplaceAllString(strings.TrimSpace(line), "")) {
			switch {
			case strings.HasPrefix(w, "https://cdn.discordapp.com/") || strings.HasPrefix(w, "https://media.discordapp.net/"):
				w = "📎"
			case strings.HasPrefix(w, "https://") || strings.HasPrefix(w, "http://"):
				w = "<" + w + ">"
			}
			n := utf8.RuneCountInString(w)
			if length+len(words)+n > maxExcerpt {
				if len(words) == 0 {
					if strings.HasPrefix(w, "<") {
						w = "🔗"
					}
					words = append(words, truncate(w, maxExcerpt))
				}
				cut = true
				break
			}
			words = append(words, w)
			length += n
		}
		if cut {
			break
		}
	}
	ex := strings.Join(words, " ")
	if strings.Count(ex, "||")%2 == 1 {
		ex, cut = strings.TrimSpace(ex[:strings.LastIndex(ex, "||")]), true
	}
	if cut && !strings.HasSuffix(ex, "…") {
		ex += "…"
	}
	return ex
}
