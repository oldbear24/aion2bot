package bot

import (
	"encoding/json"
	"errors"
	"log"

	"github.com/bwmarrin/discordgo"
)

// discordgo's response structs always serialize "content" and "embeds",
// which Components V2 messages must not carry, so V2 payloads are sent
// through these small structs instead. Every V2 message swaps its emojis
// for the bot's icons on the way out (see icons.go).

type v2Message struct {
	Components      []discordgo.MessageComponent      `json:"components"`
	Flags           discordgo.MessageFlags            `json:"flags,omitempty"`
	AllowedMentions *discordgo.MessageAllowedMentions `json:"allowed_mentions,omitempty"`
}

type modal struct {
	CustomID   string                       `json:"custom_id"`
	Title      string                       `json:"title"`
	Components []discordgo.MessageComponent `json:"components"`
}

// modalSelect is a string select for modals. discordgo's SelectMenu always
// sends "disabled", a field Discord only accepts on message components.
type modalSelect struct {
	CustomID    string                       `json:"custom_id"`
	Placeholder string                       `json:"placeholder,omitempty"`
	MinValues   int                          `json:"min_values"`
	MaxValues   int                          `json:"max_values"`
	Options     []discordgo.SelectMenuOption `json:"options"`
}

func (modalSelect) Type() discordgo.ComponentType { return discordgo.SelectMenuComponent }

func (s modalSelect) MarshalJSON() ([]byte, error) {
	type plain modalSelect
	return json.Marshal(struct {
		plain
		Type discordgo.ComponentType `json:"type"`
	}{plain(s), s.Type()})
}

type interactionResponse struct {
	Type discordgo.InteractionResponseType `json:"type"`
	Data any                               `json:"data,omitempty"`
}

// noMentions renders mentions without notifying anyone.
var noMentions = &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{}}

func (b *Bot) respond(i *discordgo.Interaction, typ discordgo.InteractionResponseType, data any) error {
	endpoint := discordgo.EndpointInteractionResponse(i.ID, i.Token)
	_, err := b.s.RequestWithBucketID("POST", endpoint, interactionResponse{Type: typ, Data: data}, endpoint)
	return err
}

// reply answers with a new message only the clicking user can see.
func (b *Bot) reply(i *discordgo.Interaction, comps ...discordgo.MessageComponent) error {
	return b.respond(i, discordgo.InteractionResponseChannelMessageWithSource, v2Message{
		Components:      b.icons.apply(comps),
		Flags:           discordgo.MessageFlagsEphemeral | discordgo.MessageFlagsIsComponentsV2,
		AllowedMentions: noMentions,
	})
}

// update replaces the message whose component was used.
func (b *Bot) update(i *discordgo.Interaction, comps ...discordgo.MessageComponent) error {
	return b.respond(i, discordgo.InteractionResponseUpdateMessage, v2Message{
		Components:      b.icons.apply(comps),
		Flags:           discordgo.MessageFlagsIsComponentsV2,
		AllowedMentions: noMentions,
	})
}

func (b *Bot) showModal(i *discordgo.Interaction, m modal) error {
	return b.respond(i, discordgo.InteractionResponseModal, m)
}

// editReply changes the bot's earlier reply to this interaction.
func (b *Bot) editReply(i *discordgo.Interaction, comps ...discordgo.MessageComponent) error {
	endpoint := discordgo.EndpointWebhookMessage(i.AppID, i.Token, "@original")
	_, err := b.s.RequestWithBucketID("PATCH", endpoint, v2Message{Components: b.icons.apply(comps), AllowedMentions: noMentions}, endpoint)
	return err
}

// send posts a message to a channel or thread and returns its ID.
func (b *Bot) send(channelID string, comps []discordgo.MessageComponent, mentions *discordgo.MessageAllowedMentions) (string, error) {
	endpoint := discordgo.EndpointChannelMessages(channelID)
	body, err := b.s.RequestWithBucketID("POST", endpoint, v2Message{
		Components:      b.icons.apply(comps),
		Flags:           discordgo.MessageFlagsIsComponentsV2,
		AllowedMentions: mentions,
	}, endpoint)
	if err != nil {
		return "", err
	}
	var msg struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &msg); err != nil {
		return "", err
	}
	return msg.ID, nil
}

// threadStart is sent instead of discordgo's ThreadStart, which always
// carries "invitable", a field for private threads only.
type threadStart struct {
	Name                string `json:"name"`
	AutoArchiveDuration int    `json:"auto_archive_duration"`
}

// startThread starts a public thread under a message. The thread gets the
// message's ID; a thread that already exists counts as started.
func (b *Bot) startThread(channelID, messageID, name string) (string, error) {
	endpoint := discordgo.EndpointChannelMessageThread(channelID, messageID)
	_, err := b.s.RequestWithBucketID("POST", endpoint, threadStart{Name: name, AutoArchiveDuration: threadArchiveMinutes}, endpoint)
	if err != nil && restCode(err) != discordgo.ErrCodeThreadAlreadyCreatedForThisMessage {
		return "", err
	}
	return messageID, nil
}

func (b *Bot) edit(channelID, messageID string, comps []discordgo.MessageComponent) error {
	endpoint := discordgo.EndpointChannelMessage(channelID, messageID)
	_, err := b.s.RequestWithBucketID("PATCH", endpoint, v2Message{
		Components:      b.icons.apply(comps),
		AllowedMentions: noMentions,
	}, discordgo.EndpointChannelMessage(channelID, ""))
	return err
}

// dm is best effort: many users only accept DMs from friends.
func (b *Bot) dm(userID string, comps ...discordgo.MessageComponent) bool {
	ch, err := b.s.UserChannelCreate(userID)
	if err == nil {
		_, err = b.send(ch.ID, comps, noMentions)
	}
	if err != nil && restCode(err) != discordgo.ErrCodeCannotSendMessagesToThisUser {
		log.Printf("dm %s: %v", userID, err)
	}
	return err == nil
}

func restCode(err error) int {
	var re *discordgo.RESTError
	if errors.As(err, &re) && re.Message != nil {
		return re.Message.Code
	}
	return 0
}

// isGone reports whether a message or channel can no longer be reached,
// so the copy of the post should be forgotten.
func isGone(err error) bool {
	switch restCode(err) {
	case discordgo.ErrCodeUnknownMessage, discordgo.ErrCodeUnknownChannel, discordgo.ErrCodeMissingAccess:
		return true
	}
	return false
}

// interactionUser works for both server and DM interactions.
func interactionUser(i *discordgo.Interaction) (id, name string) {
	if i.Member != nil && i.Member.User != nil {
		return i.Member.User.ID, i.Member.DisplayName()
	}
	if i.User != nil {
		return i.User.ID, i.User.DisplayName()
	}
	return "", ""
}
