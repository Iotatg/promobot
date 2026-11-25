package bot

import (
	"context"
	"fmt"
	"strings"

	"github.com/Iotatg/gotdbot"

	"promobot/internal/persona"
)

// Settings keys for the owner-defined custom persona.
const (
	settingPersonaName    = "persona_name"
	settingPersonaDetails = "persona_details"
)

// cmdPersona lets the owner view or set a custom persona (name + free-form
// style/details) layered over the base identity, applied to the bot's own AI
// and every connected assistant account without a restart.
func (b *Bot) cmdPersona(c *gotdbot.Client, msg *gotdbot.Message) error {
	if !b.isOwner(msg) {
		return nil
	}
	args := strings.TrimSpace(msg.Args())
	if args == "" {
		return b.showPersona(c, msg)
	}

	sub, rest := splitFirst(args)
	ctx := context.Background()
	switch strings.ToLower(sub) {
	case "name":
		if rest == "" {
			_, err := msg.ReplyText(c, "Usage: /persona name <name>", nil)
			return err
		}
		if err := b.store.SetSetting(ctx, settingPersonaName, rest); err != nil {
			_, rerr := msg.ReplyText(c, "Save failed: "+err.Error(), nil)
			return rerr
		}
		b.applyPersona(ctx)
		_, err := msg.ReplyText(c, fmt.Sprintf("Persona name set to '%s'.", rest), nil)
		return err
	case "set", "details":
		if rest == "" {
			_, err := msg.ReplyText(c, "Usage: /persona set <details...>", nil)
			return err
		}
		if err := b.store.SetSetting(ctx, settingPersonaDetails, rest); err != nil {
			_, rerr := msg.ReplyText(c, "Save failed: "+err.Error(), nil)
			return rerr
		}
		b.applyPersona(ctx)
		_, err := msg.ReplyText(c, "Custom persona details saved and applied.", nil)
		return err
	case "reset":
		_ = b.store.DelSetting(ctx, settingPersonaName)
		_ = b.store.DelSetting(ctx, settingPersonaDetails)
		b.applyPersona(ctx)
		_, err := msg.ReplyText(c, "Custom persona cleared; back to the default.", nil)
		return err
	default:
		_, err := msg.ReplyText(c, personaUsage, nil)
		return err
	}
}

func (b *Bot) showPersona(c *gotdbot.Client, msg *gotdbot.Message) error {
	ctx := context.Background()
	name, _ := b.store.GetSetting(ctx, settingPersonaName)
	details, _ := b.store.GetSetting(ctx, settingPersonaDetails)

	effName := strings.TrimSpace(name)
	if effName == "" {
		effName = b.cfg.PersonaName
	}
	var sb strings.Builder
	sb.WriteString("Assistant persona\n")
	sb.WriteString("Name: " + effName)
	if strings.TrimSpace(name) != "" {
		sb.WriteString(" (custom)")
	}
	sb.WriteString("\n")
	if strings.TrimSpace(details) == "" {
		sb.WriteString("Custom details: none (using default personality)\n")
	} else {
		sb.WriteString("Custom details:\n" + clip(details, 600) + "\n")
	}
	sb.WriteString("\nUsage:\n")
	sb.WriteString(personaUsage)
	_, err := msg.ReplyText(c, sb.String(), nil)
	return err
}

const personaUsage = "/persona - show current persona\n" +
	"/persona name <name> - set the assistant's name\n" +
	"/persona set <details...> - add custom personality/tone/details\n" +
	"/persona reset - clear the custom persona"

// ApplyStoredPersona loads a persisted custom persona (if any) and applies it.
// Called once at startup.
func (b *Bot) ApplyStoredPersona(ctx context.Context) {
	name, _ := b.store.GetSetting(ctx, settingPersonaName)
	details, _ := b.store.GetSetting(ctx, settingPersonaDetails)
	if strings.TrimSpace(name) == "" && strings.TrimSpace(details) == "" {
		return
	}
	b.applyPersona(ctx)
}

// applyPersona rebuilds the effective system prompt and pushes it to the bot's
// AI client and every connected assistant account.
func (b *Bot) applyPersona(ctx context.Context) {
	name, _ := b.store.GetSetting(ctx, settingPersonaName)
	details, _ := b.store.GetSetting(ctx, settingPersonaDetails)

	base := persona.Config{
		Name:  b.cfg.PersonaName,
		Age:   b.cfg.PersonaAge,
		DOB:   b.cfg.PersonaDOB,
		From:  b.cfg.PersonaFrom,
		Owner: "meri creator",
	}.WithName(name)
	system := persona.AppendDetails(base.System(), details)

	if b.ai != nil {
		b.ai.SetSystem(system)
	}
	if b.sessions != nil {
		b.sessions.SetSystem(system)
		b.sessions.SetPersonaName(name)
	}
	b.log.Info("persona applied", "name", strings.TrimSpace(name), "custom_details", strings.TrimSpace(details) != "")
}

func splitFirst(s string) (string, string) {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, " \t\n"); i >= 0 {
		return s[:i], strings.TrimSpace(s[i+1:])
	}
	return s, ""
}

func clip(s string, max int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= max {
		return string(r)
	}
	return string(r[:max]) + "..."
}
