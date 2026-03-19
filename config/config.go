package config

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

type Config struct {
	FaceitAPIKey           string
	TelegramToken          string
	DataDir                string
	CheckInterval          time.Duration
	TimezoneName           string
	Timezone               *time.Location
	FaceitDownloadsToken   string
	FaceitDownloadsBaseURL string
	MaxTrackedPerChat      int
	MaxTrackedGlobal       int
	CommandCooldown        time.Duration
	HeavyCommandCooldown   time.Duration
	MaxPhotoBytes          int
}

func LoadConfig() *Config {
	_ = loadDotEnv(".env")

	apiKey := strings.TrimSpace(os.Getenv("FACEIT_API_KEY"))
	token := strings.TrimSpace(os.Getenv("TELEGRAM_TOKEN"))
	if apiKey == "" || token == "" {
		log.Fatal("FACEIT_API_KEY and TELEGRAM_TOKEN must be set")
	}

	dataDir := strings.TrimSpace(os.Getenv("DATA_DIR"))
	if dataDir == "" {
		dataDir = "data"
	}

	interval := 15 * time.Minute
	if raw := strings.TrimSpace(os.Getenv("CHECK_INTERVAL")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			log.Fatalf("invalid CHECK_INTERVAL: %v", err)
		}
		interval = parsed
	}

	timezoneName := strings.TrimSpace(os.Getenv("BOT_TIMEZONE"))
	if timezoneName == "" {
		timezoneName = "Local"
	}

	var location *time.Location
	var err error
	if strings.EqualFold(timezoneName, "local") {
		location = time.Local
	} else {
		location, err = time.LoadLocation(timezoneName)
		if err != nil {
			log.Fatalf("invalid BOT_TIMEZONE: %v", err)
		}
	}

	downloadsBaseURL := strings.TrimSpace(os.Getenv("FACEIT_DOWNLOADS_BASE_URL"))
	if downloadsBaseURL == "" {
		downloadsBaseURL = "https://open.faceit.com"
	}

	maxTrackedPerChat := 25
	if raw := strings.TrimSpace(os.Getenv("MAX_TRACKED_PER_CHAT")); raw != "" {
		if _, err := fmt.Sscanf(raw, "%d", &maxTrackedPerChat); err != nil || maxTrackedPerChat < 1 {
			log.Fatalf("invalid MAX_TRACKED_PER_CHAT: %q", raw)
		}
	}

	maxTrackedGlobal := 300
	if raw := strings.TrimSpace(os.Getenv("MAX_TRACKED_GLOBAL")); raw != "" {
		if _, err := fmt.Sscanf(raw, "%d", &maxTrackedGlobal); err != nil || maxTrackedGlobal < 1 {
			log.Fatalf("invalid MAX_TRACKED_GLOBAL: %q", raw)
		}
	}

	commandCooldown := 3 * time.Second
	if raw := strings.TrimSpace(os.Getenv("COMMAND_COOLDOWN")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			log.Fatalf("invalid COMMAND_COOLDOWN: %v", err)
		}
		commandCooldown = parsed
	}

	heavyCommandCooldown := 8 * time.Second
	if raw := strings.TrimSpace(os.Getenv("HEAVY_COMMAND_COOLDOWN")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			log.Fatalf("invalid HEAVY_COMMAND_COOLDOWN: %v", err)
		}
		heavyCommandCooldown = parsed
	}

	maxPhotoBytes := 10 * 1024 * 1024
	if raw := strings.TrimSpace(os.Getenv("MAX_PHOTO_BYTES")); raw != "" {
		if _, err := fmt.Sscanf(raw, "%d", &maxPhotoBytes); err != nil || maxPhotoBytes < 1 {
			log.Fatalf("invalid MAX_PHOTO_BYTES: %q", raw)
		}
	}

	return &Config{
		FaceitAPIKey:           apiKey,
		TelegramToken:          token,
		DataDir:                dataDir,
		CheckInterval:          interval,
		TimezoneName:           timezoneName,
		Timezone:               location,
		FaceitDownloadsToken:   strings.TrimSpace(os.Getenv("FACEIT_DOWNLOADS_TOKEN")),
		FaceitDownloadsBaseURL: downloadsBaseURL,
		MaxTrackedPerChat:      maxTrackedPerChat,
		MaxTrackedGlobal:       maxTrackedGlobal,
		CommandCooldown:        commandCooldown,
		HeavyCommandCooldown:   heavyCommandCooldown,
		MaxPhotoBytes:          maxPhotoBytes,
	}
}

func loadDotEnv(path string) error {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)
		if key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); !exists {
			_ = os.Setenv(key, value)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read .env: %w", err)
	}
	return nil
}
