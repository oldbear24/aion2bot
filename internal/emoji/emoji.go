// Package emoji uploads the icons bundled with the bot as application
// emojis, which the bot can show on every server: the role, class and
// faction icons, the crown that marks a party's leader, and the interface
// icons that stand in for the emojis in its messages.
package emoji

import (
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"io/fs"
	"log"
	"path"
	"strings"

	"github.com/bwmarrin/discordgo"
)

// images holds one 128×128 PNG per role, class or faction, named after its
// id in game.json, e.g. tank.png, gladiator.png or elyos.png, and
// leader.png. images/ui holds the interface icons; tools/icons draws them
// and the faction icons. The interface icons' ids start with "ui_", e.g.
// ui_join.
//
//go:embed images/*.png images/ui/*.png
var images embed.FS

// uiDir holds the interface icons.
const uiDir = "images/ui"

// prefix marks the application emojis this package manages. Each name ends
// with a hash of the image, so a changed icon is uploaded as a new emoji
// and the old one is deleted.
const prefix = "lfg_"

type icon struct {
	id  string
	png []byte
}

// Sync makes sure every bundled icon is an application emoji and returns
// them by role or class id, or ui_ id, written the way messages show them,
// e.g. "<:lfg_tank_1a2b3c:123456789012345678>". Icons that can't be
// uploaded are left out.
func Sync(s *discordgo.Session, appID string) (map[string]string, error) {
	files, err := fs.Glob(images, "images/*.png")
	if err != nil {
		return nil, err
	}
	ui, err := fs.Glob(images, uiDir+"/*.png")
	if err != nil {
		return nil, err
	}
	files = append(files, ui...)
	want := map[string]icon{}
	for _, f := range files {
		png, err := images.ReadFile(f)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(png)
		id := strings.TrimSuffix(path.Base(f), ".png")
		if path.Dir(f) == uiDir {
			id = "ui_" + id
		}
		want[prefix+id+"_"+hex.EncodeToString(sum[:3])] = icon{id, png}
	}

	existing, err := s.ApplicationEmojis(appID)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, e := range existing {
		if !strings.HasPrefix(e.Name, prefix) {
			continue
		}
		if ic, ok := want[e.Name]; ok {
			out[ic.id] = e.MessageFormat()
			delete(want, e.Name)
			continue
		}
		// An older version of an icon, or one that is no longer bundled.
		if err := s.ApplicationEmojiDelete(appID, e.ID); err != nil {
			log.Printf("delete old emoji %s: %v", e.Name, err)
		}
	}
	for name, ic := range want {
		e, err := s.ApplicationEmojiCreate(appID, &discordgo.EmojiParams{
			Name:  name,
			Image: "data:image/png;base64," + base64.StdEncoding.EncodeToString(ic.png),
		})
		if err != nil {
			log.Printf("upload emoji %s: %v", name, err)
			continue
		}
		out[ic.id] = e.MessageFormat()
	}
	return out, nil
}
