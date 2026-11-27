package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Iotatg/gotdbot"

	"promobot/internal/ai"
	"promobot/internal/assistant"
	"promobot/internal/bot"
	"promobot/internal/config"
	"promobot/internal/media"
	"promobot/internal/notify"
	"promobot/internal/persona"
	"promobot/internal/scheduler"
	"promobot/internal/store"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if err := run(logger); err != nil {
		logger.Error("fatal", "err", err.Error())
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx := context.Background()

	st, err := store.NewMongo(ctx, cfg.MongoURI, cfg.MongoDatabase)
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = st.Close(closeCtx)
	}()

	botClient, err := gotdbot.NewClient(cfg.APIID, cfg.APIHash, cfg.BotToken, botOpts(cfg))
	if err != nil {
		return err
	}
	if err := botClient.Start(); err != nil {
		return err
	}
	logger.Info("bot authorized", "user", botClient.Me.FirstName)

	var aiClient *ai.Client
	if cfg.ChatbotEnabled() {
		url := cfg.ResolveChatURL()
		if url == "" {
			logger.Warn("chatbot enabled but no USER_LLM_CHAT_URL/BASE_URL set")
		} else {
			aiClient = ai.New(cfg.UserLLMAPIKey, url, cfg.UserLLMModel, persona.Config{
				Name:  cfg.PersonaName,
				Age:   cfg.PersonaAge,
				DOB:   cfg.PersonaDOB,
				From:  cfg.PersonaFrom,
				Owner: "meri creator",
			}.System(), cfg.UserLLMFallbackAPIKey)
			aiClient.SetMemory(st, "bot")
			logger.Info("chatbot enabled", "model", aiClient.Model())
		}
	}

	assistantMgr := assistant.NewManager(cfg, st, aiClient)
	if err := assistantMgr.LoadStored(ctx); err != nil {
		logger.Warn("some stored assistant accounts failed to connect", "err", err.Error())
	}
	if !assistantMgr.Connected() {
		if session := strings.TrimSpace(cfg.AssistantSession); session != "" {
			if _, _, err := assistantMgr.AddSession(ctx, session); err != nil {
				logger.Warn("assistant login failed; continuing without it", "err", err.Error())
			}
		}
	}
	if assistantMgr.Connected() {
		logger.Info("assistant accounts ready", "count", len(assistantMgr.AccountLines()), "primary", assistantMgr.SelfID())
	}

	log := notify.New(logger, botClient, cfg.LogGroupID)

	botSend := func(chatID int64, text string, m *store.Media) error {
		if m != nil {
			mm := *m
			// The scheduled announcement text is the caption; fall back to the
			// attachment's own caption only when no text was set.
			if strings.TrimSpace(text) != "" {
				mm.Caption = text
			}
			return media.Send(botClient, chatID, mm)
		}
		_, err := botClient.SendTextMessage(chatID, text, nil)
		return err
	}
	assistantSend := func(chatID int64, text string, m *store.Media) error {
		if m != nil {
			mm := *m
			if strings.TrimSpace(text) != "" {
				mm.Caption = text
			}
			return assistantMgr.SendMedia(chatID, mm)
		}
		return assistantMgr.SendText(chatID, text)
	}

	sched := scheduler.New(cfg, st, log, botSend, assistantSend)

	controller := bot.New(botClient, cfg, st, sched, log, assistantMgr, aiClient)
	controller.SetSessionManager(assistantMgr)
	assistantMgr.SetMoodStickerProvider(controller.MoodStickerProvider)
	controller.Register()
	controller.ApplyStoredPersona(ctx)
	setCommands(botClient, aiClient != nil)

	if err := sched.Start(ctx); err != nil {
		logger.Warn("scheduler start", "err", err.Error())
	}

	logger.Info("promobot started")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	logger.Info("shutting down")
	sched.Stop()
	assistantMgr.Close()
	botClient.Close()
	return nil
}

func botOpts(cfg *config.Config) *gotdbot.ClientOpts {
	opts := gotdbot.DefaultClientConfig()
	opts.LibraryPath = cfg.TDLibLibraryPath
	opts.DatabaseDirectory = cfg.TDLibDatabaseDir
	opts.FilesDirectory = cfg.TDLibFilesDir
	opts.LogVerbosityLevel = cfg.TDLibVerbosity
	opts.ParseMode = ""
	return opts
}

func setCommands(c *gotdbot.Client, aiEnabled bool) {
	cmds := []gotdbot.BotCommand{
		{Command: "start", Description: "Boot animation and main menu"},
		{Command: "addchannel", Description: "Register a channel you admin"},
		{Command: "delchannel", Description: "Unregister a chat (stop posting)"},
		{Command: "leave", Description: "Assistant leaves one registered chat"},
		{Command: "channels", Description: "List registered channels"},
		{Command: "persona", Description: "Show or set the assistant's custom persona"},
		{Command: "newpost", Description: "Create a scheduled announcement"},
		{Command: "posts", Description: "List, toggle or delete scheduled posts"},
		{Command: "broadcast", Description: "Send to channels and/or users"},
		{Command: "users", Description: "Count users reachable with /broadcast -user"},
		{Command: "addsticker", Description: "Save replied sticker to a mood pack"},
		{Command: "addpack", Description: "Import the replied sticker's whole pack into a mood pack"},
		{Command: "kang", Description: "Save replied sticker to a real sticker pack"},
		{Command: "sticker", Description: "Send a random sticker from a mood pack"},
		{Command: "previewsticker", Description: "Send one sticker from a mood pack"},
		{Command: "stickerpacks", Description: "List mood packs"},
		{Command: "setstring", Description: "Connect an assistant account via a session string"},
		{Command: "delstring", Description: "Disconnect an assistant account (or all)"},
		{Command: "accounts", Description: "List connected assistant accounts"},
		{Command: "help", Description: "Show help"},
	}
	if aiEnabled {
		cmds = append(cmds,
			gotdbot.BotCommand{Command: "ask", Description: "Ask the AI assistant"},
			gotdbot.BotCommand{Command: "chat", Description: "Toggle chatbot (on|off) in this chat"},
			gotdbot.BotCommand{Command: "model", Description: "Show or set the active AI model"},
			gotdbot.BotCommand{Command: "reset", Description: "Clear the AI conversation memory"},
		)
	}
	if err := c.SetCommands(cmds, "", nil); err != nil {
		slog.Warn("set commands failed", "err", err.Error())
	}
}
