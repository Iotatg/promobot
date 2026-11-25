package assistant

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"

	"github.com/gotd/td/tg"
)

// ForwardMessage forwards a single message from fromChatID to chatID as the
// assistant, preserving the original "Forwarded from" header (unlike a plain
// re-upload/copy). It returns the id of the new message, or 0 when the server
// did not report it.
func (c *Client) ForwardMessage(chatID, fromChatID int64, msgID int, silent bool) (int, error) {
	if msgID <= 0 {
		return 0, fmt.Errorf("invalid message id")
	}
	to, err := c.inputPeer(chatID)
	if err != nil {
		return 0, fmt.Errorf("resolve target: %w", err)
	}
	from, err := c.inputPeer(fromChatID)
	if err != nil {
		return 0, fmt.Errorf("resolve source: %w", err)
	}
	up, err := c.api.MessagesForwardMessages(context.Background(), &tg.MessagesForwardMessagesRequest{
		Silent:   silent,
		FromPeer: from,
		ID:       []int{msgID},
		RandomID: []int64{randID()},
		ToPeer:   to,
	})
	if err != nil {
		return 0, err
	}
	return forwardedID(up), nil
}

// EditCaption replaces the text/caption of a message the assistant previously
// sent. An empty caption clears it.
func (c *Client) EditCaption(chatID int64, msgID int, caption string) error {
	if msgID <= 0 {
		return fmt.Errorf("invalid message id")
	}
	peer, err := c.inputPeer(chatID)
	if err != nil {
		return err
	}
	req := &tg.MessagesEditMessageRequest{Peer: peer, ID: msgID}
	req.SetMessage(caption)
	_, err = c.api.MessagesEditMessage(context.Background(), req)
	return err
}

// PinMessage pins a message in a chat as the assistant.
func (c *Client) PinMessage(chatID int64, msgID int, silent bool) error {
	if msgID <= 0 {
		return fmt.Errorf("invalid message id")
	}
	peer, err := c.inputPeer(chatID)
	if err != nil {
		return err
	}
	_, err = c.api.MessagesUpdatePinnedMessage(context.Background(), &tg.MessagesUpdatePinnedMessageRequest{
		Silent: silent,
		Peer:   peer,
		ID:     msgID,
	})
	return err
}

// forwardedID extracts the id of the newly created message from the update
// stream returned by messages.forwardMessages.
func forwardedID(up tg.UpdatesClass) int {
	switch u := up.(type) {
	case *tg.Updates:
		return findMessageID(u.Updates)
	case *tg.UpdatesCombined:
		return findMessageID(u.Updates)
	}
	return 0
}

func findMessageID(updates []tg.UpdateClass) int {
	for _, u := range updates {
		if id, ok := u.(*tg.UpdateMessageID); ok {
			return id.ID
		}
	}
	return 0
}

// randID returns a positive random id for message de-duplication.
func randID() int64 {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return int64(binary.BigEndian.Uint64(b[:]) & 0x7fffffffffffffff)
}
