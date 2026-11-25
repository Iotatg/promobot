package assistant

import (
	"testing"

	"github.com/gotd/td/tg"
)

func TestForwardedID(t *testing.T) {
	cases := []struct {
		name string
		up   tg.UpdatesClass
		want int
	}{
		{"nil", nil, 0},
		{"updates", &tg.Updates{Updates: []tg.UpdateClass{
			&tg.UpdateMessageID{ID: 42, RandomID: 7},
		}}, 42},
		{"updatesCombined", &tg.UpdatesCombined{Updates: []tg.UpdateClass{
			&tg.UpdateMessageID{ID: 99, RandomID: 7},
		}}, 99},
		{"noMessageID", &tg.Updates{Updates: []tg.UpdateClass{
			&tg.UpdateMessageID{ID: 0},
		}}, 0},
	}
	for _, tc := range cases {
		if got := forwardedID(tc.up); got != tc.want {
			t.Errorf("%s: forwardedID = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestRandID(t *testing.T) {
	a, b := randID(), randID()
	if a <= 0 {
		t.Errorf("randID must be positive, got %d", a)
	}
	if a == b {
		t.Errorf("randID returned duplicate values %d", a)
	}
}
