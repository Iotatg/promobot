package scheduler

import (
	"context"
	"math/rand"
	"sync"
	"time"

	"github.com/Iotatg/gotdbot"

	"promobot/internal/config"
	"promobot/internal/notify"
	"promobot/internal/store"
)

// SendFunc delivers a text message or a media attachment to a chat. It returns
// an error that may be a TDLib error (inspectable with gotdbot.IsFloodWait).
type SendFunc func(chatID int64, text string, m *store.Media) error

// Service runs one goroutine per enabled schedule, applying jitter, warmup and
// FloodWait handling.
type Service struct {
	cfg   *config.Config
	store store.Store
	log   *notify.Logger
	bot   SendFunc
	asst  SendFunc

	mu    sync.Mutex
	stops map[string]chan struct{}
	wg    sync.WaitGroup
}

// New creates a scheduler Service. bot and asst may be nil when unavailable.
func New(cfg *config.Config, st store.Store, log *notify.Logger, bot, asst SendFunc) *Service {
	return &Service{
		cfg:   cfg,
		store: st,
		log:   log,
		bot:   bot,
		asst:  asst,
		stops: make(map[string]chan struct{}),
	}
}

// Start loads all enabled schedules and launches them.
func (s *Service) Start(ctx context.Context) error {
	schedules, err := s.store.ListSchedules(ctx)
	if err != nil {
		return err
	}
	for _, sch := range schedules {
		if sch.Enabled {
			s.launch(ctx, sch)
		}
	}
	return nil
}

// Reload cancels all running schedules and restarts from the store.
func (s *Service) Reload(ctx context.Context) error {
	s.Stop()
	return s.Start(ctx)
}

// Stop signals all running schedule goroutines to exit and waits for them.
func (s *Service) Stop() {
	s.mu.Lock()
	for id, stop := range s.stops {
		close(stop)
		delete(s.stops, id)
	}
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *Service) launch(ctx context.Context, sch store.Schedule) {
	s.mu.Lock()
	if _, ok := s.stops[sch.ID]; ok {
		s.mu.Unlock()
		return
	}
	stop := make(chan struct{})
	s.stops[sch.ID] = stop
	s.mu.Unlock()

	s.wg.Add(1)
	go s.loop(ctx, stop, sch)
}

func (s *Service) loop(ctx context.Context, stop chan struct{}, sch store.Schedule) {
	defer s.wg.Done()

	ch, err := s.store.GetChannel(ctx, sch.ChatID)
	if err != nil {
		s.log.Error("schedule: channel missing", "id", sch.ID, "chat", sch.ChatID, "err", err.Error())
		return
	}
	send, ok := s.sendFuncFor(ch.Sender)
	if !ok {
		s.log.Error("schedule: sender unavailable", "id", sch.ID, "sender", ch.Sender)
		return
	}

	if d := s.warmupWait(ch); d > 0 {
		s.log.Info("warmup wait", "chat", sch.ChatID, "seconds", int64(d.Seconds()))
		if !s.sleep(ctx, stop, d) {
			return
		}
	}

	for {
		interval := time.Duration(sch.IntervalMinutes) * time.Minute
		if interval <= 0 {
			interval = time.Minute
		}
		wait := interval
		if s.cfg.JitterSeconds > 0 {
			wait += time.Duration(rand.Intn(s.cfg.JitterSeconds)) * time.Second
		}

		if !s.sleep(ctx, stop, wait) {
			return
		}

		err := send(sch.ChatID, sch.Text, sch.Media)
		now := time.Now()
		if err != nil {
			if gotdbot.IsFloodWait(err) {
				secs := gotdbot.FloodWaitSeconds(err)
				if secs <= 0 {
					secs = 60
				}
				if s.cfg.MaxFloodWaitSecs > 0 && secs > s.cfg.MaxFloodWaitSecs {
					secs = s.cfg.MaxFloodWaitSecs
				}
				s.log.Warn("FloodWait: pausing account", "chat", sch.ChatID, "seconds", secs)
				if !s.sleep(ctx, stop, time.Duration(secs)*time.Second) {
					return
				}
			} else {
				s.log.Error("send failed", "chat", sch.ChatID, "err", err.Error())
			}
			continue
		}

		s.log.Info("announcement sent", "chat", sch.ChatID, "schedule", sch.Name)
		sch.LastSentAt = now.Unix()
		sch.NextSentAt = now.Add(wait).Unix()
		_ = s.store.UpdateSchedule(ctx, sch)
	}
}

// sleep waits for d, returning false if the schedule was stopped or ctx done.
func (s *Service) sleep(ctx context.Context, stop chan struct{}, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-stop:
		return false
	case <-ctx.Done():
		return false
	}
}

func (s *Service) warmupWait(ch *store.Channel) time.Duration {
	if s.cfg.WarmupMinutes <= 0 {
		return 0
	}
	until := ch.AddedAt + int64(s.cfg.WarmupMinutes)*60
	now := time.Now().Unix()
	if now >= until {
		return 0
	}
	return time.Duration(until-now) * time.Second
}

func (s *Service) sendFuncFor(sender string) (SendFunc, bool) {
	switch sender {
	case store.SenderAssistant:
		if s.asst == nil {
			return nil, false
		}
		return s.asst, true
	default:
		if s.bot == nil {
			return nil, false
		}
		return s.bot, true
	}
}
