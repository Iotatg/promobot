package media

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Iotatg/gotdbot"

	"promobot/internal/store"
)

// Kind constants.
const (
	KindPhoto     = "photo"
	KindVideo     = "video"
	KindAudio     = "audio"
	KindDocument  = "document"
	KindAnimation = "animation"
	KindSticker   = "sticker"
)

// Send delivers a media attachment to a chat through the given client.
func Send(c *gotdbot.Client, chatID int64, m store.Media) error {
	file := inputFile(m)
	caption := m.Caption
	switch m.Kind {
	case KindPhoto:
		_, err := c.SendPhoto(chatID, file, &gotdbot.SendPhotoOpts{Caption: caption})
		return err
	case KindVideo:
		_, err := c.SendVideo(chatID, file, &gotdbot.SendVideoOpts{Caption: caption, SupportsStreaming: true})
		return err
	case KindAudio:
		_, err := c.SendAudio(chatID, file, &gotdbot.SendAudioOpts{Caption: caption})
		return err
	case KindDocument:
		_, err := c.SendDocument(chatID, file, &gotdbot.SendDocumentOpts{Caption: caption})
		return err
	case KindAnimation:
		_, err := c.SendAnimation(chatID, file, &gotdbot.SendAnimationOpts{Caption: caption})
		return err
	case KindSticker:
		_ = c.SendChooseSticker(chatID)
		_, err := c.SendSticker(chatID, file, nil)
		return err
	default:
		return fmt.Errorf("unsupported media kind %q", m.Kind)
	}
}

func inputFile(m store.Media) gotdbot.InputFile {
	if m.IsRemote {
		return gotdbot.FileFromRemote(m.Source)
	}
	return gotdbot.FileFromPath(m.Source)
}

// FromPath builds a Media by guessing the kind from the file extension. The
// kind can be overridden with an explicit argument.
func FromPath(path, caption string) store.Media {
	return store.Media{
		Kind:    KindFromPath(path),
		Source:  path,
		Caption: caption,
	}
}

// KindFromPath guesses a media kind from a file extension.
func KindFromPath(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".jpg", ".jpeg", ".png", ".webp", ".bmp":
		return KindPhoto
	case ".mp4", ".mov", ".mkv", ".webm":
		return KindVideo
	case ".mp3", ".m4a", ".ogg", ".oga", ".flac", ".wav":
		return KindAudio
	case ".gif":
		return KindAnimation
	default:
		return KindDocument
	}
}

// FromMessage builds a remote-backed Media from a photo/video/audio/document/
// animation message. It returns nil when the message has no supported media.
func FromMessage(msg *gotdbot.Message) *store.Media {
	caption := strings.TrimSpace(msg.GetText())
	if p := msg.Photo(); p != nil && len(p.Sizes) > 0 {
		if f := p.Sizes[len(p.Sizes)-1].Photo; f != nil {
			if id := remoteID(f); id != "" {
				return &store.Media{Kind: KindPhoto, Source: id, IsRemote: true, Caption: caption}
			}
		}
	}
	if v := msg.Video(); v != nil && v.Video != nil {
		if id := remoteID(v.Video); id != "" {
			return &store.Media{Kind: KindVideo, Source: id, IsRemote: true, Caption: caption}
		}
	}
	if a := msg.Audio(); a != nil && a.Audio != nil {
		if id := remoteID(a.Audio); id != "" {
			return &store.Media{Kind: KindAudio, Source: id, IsRemote: true, Caption: caption}
		}
	}
	if id := StickerRemoteID(msg); id != "" {
		return &store.Media{Kind: KindSticker, Source: id, IsRemote: true}
	}
	if d := msg.Document(); d != nil && d.Document != nil {
		if id := remoteID(d.Document); id != "" {
			return &store.Media{Kind: KindDocument, Source: id, IsRemote: true, Caption: caption}
		}
	}
	if an := msg.Animation(); an != nil && an.Animation != nil {
		if id := remoteID(an.Animation); id != "" {
			return &store.Media{Kind: KindAnimation, Source: id, IsRemote: true, Caption: caption}
		}
	}
	return nil
}

// Capture builds a Media from a photo/video/audio/document/animation/sticker
// message and downloads it to a local file under dir, so the attachment can be
// re-sent by either the bot or the assistant. It returns (nil, nil) when the
// message has no supported media.
func Capture(c *gotdbot.Client, msg *gotdbot.Message, dir string) (*store.Media, error) {
	m := FromMessage(msg)
	if m == nil {
		return nil, nil
	}
	if !m.IsRemote {
		return m, nil
	}

	file, err := msg.DownloadSync(c)
	if err != nil {
		return nil, err
	}
	if file == nil || file.Local == nil || file.Local.Path == "" {
		return nil, fmt.Errorf("media download produced no local file")
	}

	if dir == "" {
		dir = "."
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	dest := filepath.Join(dir, store.NewID()+extFor(file.Local.Path, m.Kind))
	if err := copyFile(file.Local.Path, dest); err != nil {
		return nil, err
	}

	m.Source = dest
	m.IsRemote = false
	return m, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// extFor returns the file extension to use for a downloaded file, falling back
// to a per-kind default when the source path has none.
func extFor(srcPath, kind string) string {
	if ext := filepath.Ext(srcPath); ext != "" {
		return ext
	}
	switch kind {
	case KindPhoto:
		return ".jpg"
	case KindVideo:
		return ".mp4"
	case KindAudio:
		return ".mp3"
	case KindAnimation:
		return ".gif"
	case KindSticker:
		return ".webp"
	default:
		return ".bin"
	}
}

func remoteID(f *gotdbot.File) string {
	if f == nil || f.Remote == nil {
		return ""
	}
	if f.Remote.Id != "" {
		return f.Remote.Id
	}
	return f.Remote.UniqueId
}

// StickerRemoteID extracts the remote file id from a sticker message so it can
// be stored and re-sent later.
func StickerRemoteID(msg *gotdbot.Message) string {
	st := msg.Sticker()
	if st == nil || st.Sticker == nil {
		return ""
	}
	if st.Sticker.Remote != nil && st.Sticker.Remote.Id != "" {
		return st.Sticker.Remote.Id
	}
	if st.Sticker.Remote != nil && st.Sticker.Remote.UniqueId != "" {
		return st.Sticker.Remote.UniqueId
	}
	return ""
}
