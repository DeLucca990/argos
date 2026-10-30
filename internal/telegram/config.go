package telegram

import (
	"errors"
	"fmt"
	"maps"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	TokenEnv        = "TELEGRAM_BOT_TOKEN"
	AllowedUsersEnv = "TELEGRAM_ALLOWED_USER_IDS"
	DataDirEnv      = "ARGOS_DATA_DIR"
	ModelEnv        = "ARGOS_MODEL"
	EffortEnv       = "ARGOS_EFFORT"
	DailyBudgetEnv  = "ARGOS_DAILY_BUDGET_USD"
	BackupKeepEnv   = "ARGOS_BACKUP_KEEP"
	BackupEveryEnv  = "ARGOS_BACKUP_EVERY"
	DigestAtEnv     = "ARGOS_DIGEST_AT"
	DeleteFilesEnv  = "ARGOS_DELETE_RECEIPTS"
	PluggyIDEnv     = "PLUGGY_CLIENT_ID"
	PluggySecretEnv = "PLUGGY_CLIENT_SECRET"
	BankEveryEnv    = "PLUGGY_SYNC_EVERY"

	anthropicKeyEnv   = "ANTHROPIC_API_KEY"
	anthropicTokenEnv = "ANTHROPIC_AUTH_TOKEN"
)

const (
	defaultDataDir = "/data"
	defaultModel   = "claude-opus-5-5"
	defaultEffort  = "low"
	defaultKeep    = 14
	defaultDigest  = "09:00"
	defaultBackup  = 24 * time.Hour
	defaultBank    = time.Hour
)

var efforts = []string{"low", "medium", "high", "xhigh", "max"}

type Config struct {
	Token        string
	AllowedUsers map[int64]bool
	DataDir      string
	Model        string
	Effort       string
	DailyBudget  float64
	BackupKeep   int
	BackupEvery  time.Duration
	BankEvery    time.Duration
	DigestAt     *DailyAt
	DeleteFiles  bool
	Pluggy       PluggyCredentials
	PluggyUsers  map[int64]PluggyCredentials
}

type PluggyCredentials struct{ ID, Secret string }

type DailyAt struct{ Hour, Minute int }

func (d DailyAt) String() string { return fmt.Sprintf("%02d:%02d", d.Hour, d.Minute) }

func LoadConfig() (Config, error) {
	var problems []string

	cfg := Config{
		Token:  env(TokenEnv),
		Model:  envOr(ModelEnv, defaultModel),
		Effort: strings.ToLower(envOr(EffortEnv, defaultEffort)),

		Pluggy: PluggyCredentials{ID: env(PluggyIDEnv), Secret: env(PluggySecretEnv)},
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

	if raw := env(DailyBudgetEnv); raw != "" {
		v, err := strconv.ParseFloat(strings.Replace(raw, ",", ".", 1), 64)
		if err != nil || !(v > 0) || math.IsInf(v, 0) {
			problems = append(problems, fmt.Sprintf("%s=%q: must be a positive amount of dollars such as 1.50", DailyBudgetEnv, raw))
		}
		cfg.DailyBudget = v
	}

	cfg.BackupKeep = defaultKeep
	if raw := env(BackupKeepEnv); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			problems = append(problems, fmt.Sprintf("%s=%q: must be how many backups to keep, 0 for none", BackupKeepEnv, raw))
		}
		cfg.BackupKeep = n
	}

	for _, every := range []struct {
		key      string
		fallback time.Duration
		to       *time.Duration
	}{{BackupEveryEnv, defaultBackup, &cfg.BackupEvery}, {BankEveryEnv, defaultBank, &cfg.BankEvery}} {
		*every.to = every.fallback
		if raw := env(every.key); raw != "" {
			d, err := parseEvery(raw)
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s=%q: %v", every.key, raw, err))
			}
			*every.to = d
		}
	}

	if raw := env(DeleteFilesEnv); raw != "" {
		v, err := strconv.ParseBool(raw)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s=%q: must be true or false", DeleteFilesEnv, raw))
		}
		cfg.DeleteFiles = v
	}

	if (cfg.Pluggy.ID == "") != (cfg.Pluggy.Secret == "") {
		problems = append(problems, fmt.Sprintf("%s and %s go together: both, from the Pluggy dashboard, "+
			"to import expenses from banks; neither to send receipts only", PluggyIDEnv, PluggySecretEnv))
	}
	var pluggyProblems []string
	cfg.PluggyUsers, pluggyProblems = pluggyUsers(os.Environ(), users)
	problems = append(problems, pluggyProblems...)

	if raw := strings.ToLower(envOr(DigestAtEnv, defaultDigest)); raw != "off" {
		t, err := time.Parse("15:04", raw)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s=%q: must be a time of day such as 09:00, or off", DigestAtEnv, raw))
		} else {
			cfg.DigestAt = &DailyAt{Hour: t.Hour(), Minute: t.Minute()}
		}
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
			"%s=%s is not writable (%v): under Docker, a folder mounted there must belong to the "+
				"user the service runs as — sudo chown -R 1000:1000 data — and the argos-data volume "+
				"to the image's user — docker run --rm -v argos-data:/data alpine chown -R 65532:65532 /data",
			DataDirEnv, dir, err)
	}
	return dir, nil
}

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

func parseEvery(raw string) (time.Duration, error) {
	d, err := time.ParseDuration(raw)
	if days, ok := strings.CutSuffix(raw, "d"); ok {
		n, nerr := strconv.Atoi(days)
		d, err = time.Duration(n)*24*time.Hour, nerr
	}
	if err != nil || d < time.Minute || d%time.Minute != 0 {
		return 0, errors.New("must be whole minutes, at least 1m, such as 30m, 6h or 1d")
	}
	return d, nil
}

func pluggyUsers(environ []string, allowed map[int64]bool) (map[int64]PluggyCredentials, []string) {
	suffixes := map[string]bool{}
	for _, kv := range environ {
		key, _, _ := strings.Cut(kv, "=")
		for _, prefix := range []string{PluggyIDEnv + "_", PluggySecretEnv + "_"} {
			if suffix, ok := strings.CutPrefix(key, prefix); ok {
				suffixes[suffix] = true
			}
		}
	}

	own := map[int64]PluggyCredentials{}
	var problems []string
	for _, suffix := range slices.Sorted(maps.Keys(suffixes)) {
		idKey, secretKey := PluggyIDEnv+"_"+suffix, PluggySecretEnv+"_"+suffix
		creds := PluggyCredentials{ID: env(idKey), Secret: env(secretKey)}
		if creds.ID == "" && creds.Secret == "" {
			continue
		}
		id, err := strconv.ParseInt(suffix, 10, 64)
		switch {
		case err != nil || id <= 0:
			problems = append(problems, fmt.Sprintf("%s, %s: what follows %s_ is the numeric Telegram id of "+
				"the person whose Pluggy application it is", idKey, secretKey, PluggyIDEnv))
		case creds.ID == "" || creds.Secret == "":
			problems = append(problems, fmt.Sprintf("%s and %s go together", idKey, secretKey))
		case len(allowed) > 0 && !allowed[id]:
			problems = append(problems, fmt.Sprintf("%s: %d is not in %s", idKey, id, AllowedUsersEnv))
		default:
			own[id] = creds
		}
	}
	return own, problems
}

func env(key string) string { return strings.TrimSpace(os.Getenv(key)) }

func envOr(key, fallback string) string {
	if v := env(key); v != "" {
		return v
	}
	return fallback
}
