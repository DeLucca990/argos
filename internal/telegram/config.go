package telegram

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	TokenEnv        = "TELEGRAM_BOT_TOKEN"
	AllowedUsersEnv = "TELEGRAM_ALLOWED_USER_IDS"
	DataDirEnv      = "ARGOS_DATA_DIR"
)

const defaultDataDir = "/data"

type Config struct {
	Token        string
	AllowedUsers map[int64]bool
	DataDir      string
}

func LoadConfig() (Config, error) {
	var problems []string

	cfg := Config{Token: env(TokenEnv)}

	if cfg.Token == "" {
		problems = append(problems, fmt.Sprintf(
			"%s is not set: it is the token @BotFather gave when the bot was created — "+
				"a bot of its own, since two processes polling one token fail with 409 Conflict",
			TokenEnv))
	}

	users, err := parseUserIDs(env(AllowedUsersEnv))
	switch {
	case err != nil:
		problems = append(problems, fmt.Sprintf("%s: %v", AllowedUsersEnv, err))
	case len(users) == 0:
		problems = append(problems, fmt.Sprintf(
			"%s is not set: list the numeric Telegram user ids allowed to use the bot, "+
				"comma-separated. Without it anyone who finds the bot could record expenses",
			AllowedUsersEnv))
	}
	cfg.AllowedUsers = users

	if cfg.DataDir, err = DataDir(); err != nil {
		problems = append(problems, err.Error())
	}

	if len(problems) > 0 {
		return Config{}, fmt.Errorf("configuration:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return cfg, nil
}

func DataDir() (string, error) {
	dir := envOr(DataDirEnv, defaultDataDir)
	if err := writable(dir); err != nil {
		return dir, fmt.Errorf(
			"%s=%s is not writable (%v): under Docker, the host's ./data must belong to the "+
				"container's user — mkdir -p data && sudo chown 1000:1000 data, or PUID/PGID in .env",
			DataDirEnv, dir, err)
	}
	return dir, nil
}

// Checked by writing, not by mode bits: what counts is whether this process,
// under whatever uid Docker gave it, can create a file there.
func writable(dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".write-check-*")
	if err != nil {
		return err
	}
	f.Close()
	return os.Remove(f.Name())
}

func parseUserIDs(raw string) (map[int64]bool, error) {
	ids := map[int64]bool{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("%q is not a numeric user id — usernames change hands, ids do not", part)
		}
		ids[id] = true
	}
	return ids, nil
}

func env(key string) string { return strings.TrimSpace(os.Getenv(key)) }

func envOr(key, fallback string) string {
	if v := env(key); v != "" {
		return v
	}
	return fallback
}
