package bot

import (
	"testing"

	"github.com/Iotatg/gotdbot"
)

func TestOriginChannel(t *testing.T) {
	cases := []struct {
		name     string
		msg      *gotdbot.Message
		wantChat int64
		wantMsg  int
		wantOK   bool
	}{
		{"nil", nil, 0, 0, false},
		{"noForward", &gotdbot.Message{}, 0, 0, false},
		{
			"channelOrigin",
			&gotdbot.Message{ForwardInfo: &gotdbot.MessageForwardInfo{
				Origin: &gotdbot.MessageOriginChannel{ChatId: -1001234567890, MessageId: 42},
			}},
			-1001234567890, 42, true,
		},
		{
			"userOrigin",
			&gotdbot.Message{ForwardInfo: &gotdbot.MessageForwardInfo{
				Origin: &gotdbot.MessageOriginUser{SenderUserId: 123},
			}},
			0, 0, false,
		},
		{
			"channelOriginMissingID",
			&gotdbot.Message{ForwardInfo: &gotdbot.MessageForwardInfo{
				Origin: &gotdbot.MessageOriginChannel{ChatId: -1001234567890},
			}},
			0, 0, false,
		},
	}
	for _, tc := range cases {
		chat, msg, ok := originChannel(tc.msg)
		if chat != tc.wantChat || msg != tc.wantMsg || ok != tc.wantOK {
			t.Errorf("%s: originChannel = (%d,%d,%v), want (%d,%d,%v)",
				tc.name, chat, msg, ok, tc.wantChat, tc.wantMsg, tc.wantOK)
		}
	}
}
