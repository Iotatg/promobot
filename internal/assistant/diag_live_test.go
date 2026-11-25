package assistant

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/gotd/td/tg"

	"promobot/internal/ai"
	"promobot/internal/config"
)

// TestDiagLive inspects the assistant's recent history with the owner and, if it
// finds a recent incoming media/sticker message, runs it through processMessage
// to exercise the incoming path end-to-end. Skipped unless RUN_LIVE_DIAG=1.
func TestDiagLive(t *testing.T) {
	if os.Getenv("RUN_LIVE_DIAG") != "1" {
		t.Skip("set RUN_LIVE_DIAG=1 to run the assistant diagnostics")
	}
	if root := findRoot(); root != "" {
		_ = os.Chdir(root)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.AssistantSession == "" {
		t.Skip("no assistant session configured")
	}
	var aiClient *ai.Client
	if cfg.ResolveChatURL() != "" && cfg.UserLLMAPIKey != "" {
		aiClient = ai.New(cfg.UserLLMAPIKey, cfg.ResolveChatURL(), cfg.UserLLMModel, "")
	}
	c, err := FromPyrogramSession(int(cfg.APIID), cfg.APIHash, cfg.AssistantSession, aiClient)
	if err != nil {
		t.Fatalf("connect assistant: %v", err)
	}
	defer c.Close()

	ctx := context.Background()
	owner, err := c.pm.ResolveUserID(ctx, cfg.OwnerID)
	if err != nil {
		t.Fatalf("resolve owner %d: %v", cfg.OwnerID, err)
	}
	uname, _ := owner.Username()
	t.Logf("owner resolved: id=%d username=%s", owner.ID(), uname)

	res, err := c.api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
		Peer:  owner.InputPeer(),
		Limit: 25,
	})
	if err != nil {
		t.Fatalf("get history: %v", err)
	}
	var msgs []*tg.Message
	switch h := res.(type) {
	case *tg.MessagesMessages:
		msgs = toMessages(h.Messages)
	case *tg.MessagesMessagesSlice:
		msgs = toMessages(h.Messages)
	case *tg.MessagesChannelMessages:
		msgs = toMessages(h.Messages)
	}
	t.Logf("history entries: %d", len(msgs))

	var lastMedia, lastSticker *tg.Message
	for _, m := range msgs {
		dir := "in "
		if m.Out {
			dir = "out"
		}
		t.Logf("%s id=%d media=%s text=%q", dir, m.ID, mediaKindName(m), truncate(m.Message))
		if m.Out {
			continue
		}
		if lastSticker == nil && isSticker(m) {
			lastSticker = m
		}
		if lastMedia == nil {
			if _, ok := messageMediaKind(m); ok {
				lastMedia = m
			}
		}
	}

	check := func(label string) {
		res2, err := c.api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: owner.InputPeer(), Limit: 5})
		if err != nil {
			t.Logf("%s: get history: %v", label, err)
			return
		}
		var after []*tg.Message
		switch h := res2.(type) {
		case *tg.MessagesMessages:
			after = toMessages(h.Messages)
		case *tg.MessagesMessagesSlice:
			after = toMessages(h.Messages)
		case *tg.MessagesChannelMessages:
			after = toMessages(h.Messages)
		}
		for _, m := range after {
			dir := "in "
			if m.Out {
				dir = "out"
			}
			t.Logf("%s %s id=%d media=%s text=%q", label, dir, m.ID, mediaKindName(m), truncate(m.Message))
		}
	}

	if lastMedia != nil {
		t.Logf("processing media id=%d %s", lastMedia.ID, mediaKindName(lastMedia))
		c.processMessage(ctx, lastMedia)
		time.Sleep(45 * time.Second)
		check("after-media")
	} else {
		t.Log("no incoming analysable media found")
	}

	if lastSticker != nil {
		time.Sleep(3 * time.Second) // clear per-user cooldown
		t.Logf("processing sticker id=%d", lastSticker.ID)
		c.processMessage(ctx, lastSticker)
		time.Sleep(10 * time.Second)
		check("after-sticker")
	} else {
		t.Log("no incoming sticker found")
	}

	if lastMedia != nil {
		time.Sleep(3 * time.Second)
		peer := &tg.PeerUser{UserID: owner.ID()}
		synth := &tg.Message{
			ID:      lastMedia.ID + 100000,
			PeerID:  peer,
			FromID:  peer,
			Message: "Kon baitha hua hai, kon hai image me",
			ReplyTo: &tg.MessageReplyHeader{ReplyToMsgID: lastMedia.ID},
		}
		t.Logf("reply-to-media: replying to id=%d", lastMedia.ID)
		answer, handled := c.tryReplyToMedia(ctx, synth, synth.Message)
		t.Logf("reply-to-media handled=%v answer=%q", handled, truncate(answer))
		if handled && answer != "" {
			c.sendToChat(ctx, synth, answer)
		}
		time.Sleep(5 * time.Second)
		check("after-reply-media")
	}
}

func toMessages(in []tg.MessageClass) []*tg.Message {
	var out []*tg.Message
	for _, m := range in {
		if msg, ok := m.(*tg.Message); ok {
			out = append(out, msg)
		}
	}
	return out
}
