package telegram

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
)

const (
	TokenEnv        = "TELEGRAM_BOT_TOKEN"
	AllowedUsersEnv = "TELEGRAM_ALLOWED_USER_IDS"
	DataDirEnv      = "ARGOS_DATA_DIR"
	ModelEnv        = "ARGOS_MODEL"
	EffortEnv       = "ARGOS_EFFORT"

	anthropicKeyEnv   = "ANTHROPIC_API_KEY"
	anthropicTokenEnv = "ANTHROPIC_AUTH_TOKEN"
)

const (
	defaultDataDir = "/data"
	defaultModel   = "claude-opus-5-5"
	defaultEffort  = "low"
)

var efforts = []string{"low", "medium", "high", "xhigh", "max"}

type Config struct {
	Token        string
	AllowedUsers map[int64]bool
	DataDir      string
	Model        string
	Effort       string
}

func LoadConfig() (Config, error) {
	var problems []string

	cfg := Config{
		Token:  env(TokenEnv),
		Model:  envOr(ModelEnv, defaultModel),
		Effort: strings.ToLower(envOr(EffortEnv, defaultEffort)),
	}

	if env(anthropicKeyEnv) == "" && env(anthropicTokenEnv) == "" {
		problems = append(problems, fmt.Sprintf("%s is not set: the bot reads receipts with Claude", anthropicKeyEnv))
	}
	if !slices.Contains(efforts, cfg.Effort) {
		problems = append(problems, fmt.Sprintf("%s=%q: must be one of %s", EffortEnv, cfg.Effort, strings.Join(efforts, ", ")))
	}

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
			"%s=%s is not writable (%v): under Docker it is the argos-data volume, which the image "+
				"creates owned by its user; one created first by another container belongs to root — "+
				"docker run --rm -v argos-data:/data alpine chown -R 65532:65532 /data",
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
