package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all runtime configuration for Promobot, loaded from environment
// variables and an optional .env file.
type Config struct {
	// Telegram application credentials (from https://my.telegram.org).
	APIID   int32
	APIHash string

	// Bot token from @BotFather. Required.
	BotToken string

	// Owner is the only Telegram user allowed to control the bot.
	OwnerID int64

	// LogGroupID is an optional private chat for structured status logs.
	LogGroupID int64

	// TDLib paths.
	TDLibLibraryPath string
	TDLibDatabaseDir string
	TDLibFilesDir    string
	TDLibVerbosity   int32

	// MediaDir is where media/stickers captured for posts are cached locally so
	// both the bot and the assistant can re-send them.
	MediaDir string

	// Rate limiting for incoming commands (per user).
	RateLimitCount  int
	RateLimitWindow time.Duration

	// Scheduler tuning.
	WarmupMinutes    int
	JitterSeconds    int
	MaxFloodWaitSecs int

	// MongoDB.
	MongoURI      string
	MongoDatabase string

	// Assistant (userbot). Enabled when AssistantPhone is non-empty.
	AssistantPhone       string
	AssistantDatabaseDir string
	AssistantFilesDir    string

	// AssistantSession is a Pyrogram StringSession used to log the assistant
	// in via MTProto (gotd) without an interactive TDLib login. Enables the
	// assistant when non-empty.
	AssistantSession string

	// Chatbot (OpenAI-compatible LLM).
	UserLLMAPIKey  string
	UserLLMBase    string
	UserLLMChatURL string
	UserLLMModel   string

	// Persona (Radhika) identity.
	PersonaName string
	PersonaAge  string
	PersonaDOB  string
	PersonaFrom string
}

// AssistantEnabled reports whether the assistant client should be started.
func (c *Config) AssistantEnabled() bool {
	return strings.TrimSpace(c.AssistantPhone) != "" || strings.TrimSpace(c.AssistantSession) != ""
}

// ChatbotEnabled reports whether an LLM API key is configured.
func (c *Config) ChatbotEnabled() bool {
	return strings.TrimSpace(c.UserLLMAPIKey) != ""
}

// ResolveChatURL returns the chat completions endpoint, deriving it from the
// base URL when an explicit chat URL is not set.
func (c *Config) ResolveChatURL() string {
	if u := strings.TrimSpace(c.UserLLMChatURL); u != "" {
		return u
	}
	base := strings.TrimRight(strings.TrimSpace(c.UserLLMBase), "/")
	if base == "" {
		return ""
	}
	return base + "/chat/completions"
}

// Load reads configuration from the process environment, optionally seeding it
// from a .env file first. Values already present in the environment win.
func Load() (*Config, error) {
	_ = loadDotEnv(".env")

	cfg := &Config{
		APIID:                int32(getInt("API_ID", 0)),
		APIHash:              getString("API_HASH", ""),
		BotToken:             getString("BOT_TOKEN", ""),
		OwnerID:              int64(getInt("OWNER_ID", 0)),
		LogGroupID:           int64(getInt("LOG_GROUP_ID", 0)),
		TDLibLibraryPath:     getString("TDLIB_LIBRARY_PATH", ""),
		TDLibDatabaseDir:     getString("TDLIB_DATABASE_DIR", "./.tdlib/db"),
		TDLibFilesDir:        getString("TDLIB_FILES_DIR", "./.tdlib/files"),
		TDLibVerbosity:       int32(getInt("TDLOG_VERBOSITY", 1)),
		MediaDir:             getString("MEDIA_DIR", "./.tdlib/media"),
		RateLimitCount:       getInt("RATE_LIMIT_COUNT", 20),
		RateLimitWindow:      time.Duration(getInt("RATE_LIMIT_WINDOW_SECONDS", 60)) * time.Second,
		WarmupMinutes:        getInt("WARMUP_MINUTES", 15),
		JitterSeconds:        getInt("JITTER_SECONDS", 45),
		MaxFloodWaitSecs:     getInt("MAX_FLOODWAIT_SECONDS", 3600),
		MongoURI:             getString("MONGO_URI", "mongodb://localhost:27017"),
		MongoDatabase:        getString("MONGO_DATABASE", "promobot"),
		AssistantPhone:       getString("ASSISTANT_PHONE", ""),
		AssistantDatabaseDir: getString("ASSISTANT_DATABASE_DIR", "./.tdlib/assistant_db"),
		AssistantFilesDir:    getString("ASSISTANT_FILES_DIR", "./.tdlib/assistant_files"),
		AssistantSession:     getString("ASSISTANT_SESSION", ""),

		UserLLMAPIKey:  getString("USER_LLM_API_KEY", ""),
		UserLLMBase:    getString("USER_LLM_BASE_URL", ""),
		UserLLMChatURL: getString("USER_LLM_CHAT_URL", ""),
		UserLLMModel:   getString("USER_LLM_MODEL", "free/gemini-4-hydrogen"),

		PersonaName: getString("PERSONA_NAME", "Radhika"),
		PersonaAge:  getString("PERSONA_AGE", "20"),
		PersonaDOB:  getString("PERSONA_DOB", "14 February 2006"),
		PersonaFrom: getString("PERSONA_FROM", "Jaipur, India"),
	}

	if cfg.BotToken == "" {
		return nil, fmt.Errorf("BOT_TOKEN is required")
	}
	if cfg.OwnerID == 0 {
		return nil, fmt.Errorf("OWNER_ID is required")
	}
	if cfg.APIID == 0 || cfg.APIHash == "" {
		return nil, fmt.Errorf("API_ID and API_HASH are required")
	}
	if cfg.MongoURI == "" {
		return nil, fmt.Errorf("MONGO_URI is required")
	}
	return cfg, nil
}

func getString(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func getInt(key string, def int) int {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return def
	}
	return n
}

// loadDotEnv parses a minimal .env file and sets any keys not already present
// in the environment.
func loadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		if len(val) >= 2 {
			if (val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'') {
				val = val[1 : len(val)-1]
			}
		}
		if _, exists := os.LookupEnv(key); !exists {
			_ = os.Setenv(key, val)
		}
	}
	return scanner.Err()
}
