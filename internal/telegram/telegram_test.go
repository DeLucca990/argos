package telegram

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/DeLucca990/argos-bot/internal/store"
)

func TestRememberSavesTheSender(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "argos.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	b := &Bot{store: st}
	var seen store.User
	next := b.remember(func(ctx context.Context, _ *bot.Bot, _ *models.Update) { seen = userFrom(ctx) })
	next(context.Background(), nil, &models.Update{Message: &models.Message{
		From: &models.User{ID: 8683784233, FirstName: "Ana", Username: "ana", LanguageCode: "pt-br"},
		Chat: models.Chat{ID: 8683784233, Type: models.ChatTypePrivate},
	}})

	if seen.ID == 0 || seen.TelegramID != 8683784233 {
		t.Fatalf("the handler got user %+v", seen)
	}
	u, err := st.User(context.Background(), seen.ID)
	if err != nil || u.FirstName != "Ana" || u.Username != "ana" || u.LanguageCode != "pt-br" {
		t.Errorf("saved: %+v, %v", u, err)
	}
}

func setEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for _, k := range []string{TokenEnv, AllowedUsersEnv, DataDirEnv, ModelEnv, EffortEnv, anthropicKeyEnv, anthropicTokenEnv,
		PluggyIDEnv, PluggySecretEnv, BackupEveryEnv, BankEveryEnv} {
		t.Setenv(k, kv[k])
	}
}

func TestConfigReadsUsersAndDataDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	setEnv(t, map[string]string{
		TokenEnv:        "123:abc",
		AllowedUsersEnv: "42, 7",
		DataDirEnv:      dir,
		anthropicKeyEnv: "sk-ant-test",
	})
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AllowedUsers[42] || !cfg.AllowedUsers[7] || len(cfg.AllowedUsers) != 2 {
		t.Errorf("users: %v", cfg.AllowedUsers)
	}
	if cfg.Model != "claude-opus-5-5" || cfg.Effort != "low" {
		t.Errorf("model %q at effort %q", cfg.Model, cfg.Effort)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Errorf("the data directory was not created: %v", err)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Errorf("the write check left %d file(s) behind", len(left))
	}
}

func TestConfigReportsEveryProblemAtOnce(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes through read-only permissions")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })

	setEnv(t, map[string]string{DataDirEnv: dir, EffortEnv: "huge"})
	_, err := LoadConfig()
	if err == nil {
		t.Fatal("an empty environment was accepted")
	}
	for _, want := range []string{TokenEnv, AllowedUsersEnv, DataDirEnv, anthropicKeyEnv, EffortEnv} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s:\n%v", want, err)
		}
	}
}

func TestConfigDeleteReceipts(t *testing.T) {
	base := map[string]string{TokenEnv: "123:abc", AllowedUsersEnv: "42", DataDirEnv: t.TempDir(), anthropicKeyEnv: "x"}
	for raw, want := range map[string]bool{"": false, "true": true, "1": true, "false": false} {
		base[DeleteFilesEnv] = raw
		setEnv(t, base)
		t.Setenv(DeleteFilesEnv, raw)
		if cfg, err := LoadConfig(); err != nil || cfg.DeleteFiles != want {
			t.Errorf("%q: %v, %v", raw, cfg.DeleteFiles, err)
		}
	}
	t.Setenv(DeleteFilesEnv, "talvez")
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), DeleteFilesEnv) {
		t.Errorf("talvez: %v", err)
	}
}

func TestConfigPluggy(t *testing.T) {
	base := map[string]string{TokenEnv: "123:abc", AllowedUsersEnv: "42", DataDirEnv: t.TempDir(), anthropicKeyEnv: "x"}
	setEnv(t, base)
	if cfg, err := LoadConfig(); err != nil || cfg.Pluggy.ID != "" || len(cfg.PluggyUsers) != 0 {
		t.Errorf("without Pluggy: %+v, %v", cfg, err)
	}
	base[PluggyIDEnv], base[PluggySecretEnv] = "id", "secret"
	setEnv(t, base)
	if cfg, err := LoadConfig(); err != nil || cfg.Pluggy != (PluggyCredentials{"id", "secret"}) {
		t.Errorf("with Pluggy: %+v, %v", cfg, err)
	}
	base[PluggySecretEnv] = ""
	setEnv(t, base)
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), PluggySecretEnv) {
		t.Errorf("an id without a secret: %v", err)
	}
}

func TestConfigPluggyPerPerson(t *testing.T) {
	setEnv(t, map[string]string{TokenEnv: "123:abc", AllowedUsersEnv: "42, 7", DataDirEnv: t.TempDir(), anthropicKeyEnv: "x"})
	t.Setenv(PluggyIDEnv+"_7", "bia-id")
	t.Setenv(PluggySecretEnv+"_7", "bia-secret")
	t.Setenv(PluggyIDEnv+"_99", "")
	t.Setenv(PluggySecretEnv+"_99", "")
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.PluggyUsers) != 1 || cfg.PluggyUsers[7] != (PluggyCredentials{"bia-id", "bia-secret"}) || cfg.Pluggy.ID != "" {
		t.Errorf("per person: %+v, default %+v", cfg.PluggyUsers, cfg.Pluggy)
	}

	t.Setenv(PluggyIDEnv+"_bia", "x")
	t.Setenv(PluggySecretEnv+"_bia", "y")
	t.Setenv(PluggyIDEnv+"_99", "z")
	t.Setenv(PluggySecretEnv+"_99", "w")
	t.Setenv(PluggySecretEnv+"_42", "only the secret")
	_, err = LoadConfig()
	for _, want := range []string{
		"PLUGGY_CLIENT_ID_bia, PLUGGY_CLIENT_SECRET_bia: what follows PLUGGY_CLIENT_ID_ is the numeric Telegram id",
		"PLUGGY_CLIENT_ID_99: 99 is not in " + AllowedUsersEnv,
		"PLUGGY_CLIENT_ID_42 and PLUGGY_CLIENT_SECRET_42 go together",
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("the error lacks %q:\n%v", want, err)
		}
	}
}

func TestConfigIntervals(t *testing.T) {
	base := map[string]string{TokenEnv: "123:abc", AllowedUsersEnv: "42", DataDirEnv: t.TempDir(), anthropicKeyEnv: "x"}
	setEnv(t, base)
	if cfg, err := LoadConfig(); err != nil || cfg.BackupEvery != 24*time.Hour || cfg.BankEvery != time.Hour {
		t.Errorf("defaults: backups every %v, bank every %v, %v", cfg.BackupEvery, cfg.BankEvery, err)
	}
	for raw, want := range map[string]time.Duration{"6h": 6 * time.Hour, "1d": 24 * time.Hour, "7d": 7 * 24 * time.Hour,
		"90m": 90 * time.Minute, "1h30m": 90 * time.Minute, "1m": time.Minute} {
		base[BackupEveryEnv], base[BankEveryEnv] = raw, raw
		setEnv(t, base)
		if cfg, err := LoadConfig(); err != nil || cfg.BackupEvery != want || cfg.BankEvery != want {
			t.Errorf("%q: %v and %v, %v", raw, cfg.BackupEvery, cfg.BankEvery, err)
		}
	}
	for _, raw := range []string{"0", "30s", "90s", "-1h", "0d", "uma hora", "d"} {
		base[BackupEveryEnv], base[BankEveryEnv] = raw, raw
		setEnv(t, base)
		_, err := LoadConfig()
		if err == nil || !strings.Contains(err.Error(), BackupEveryEnv) || !strings.Contains(err.Error(), BankEveryEnv) {
			t.Errorf("%q was accepted: %v", raw, err)
		}
	}
}

func TestConfigRefusesUsernames(t *testing.T) {
	setEnv(t, map[string]string{
		TokenEnv:        "123:abc",
		AllowedUsersEnv: "42,@pedro",
		DataDirEnv:      t.TempDir(),
	})
	_, err := LoadConfig()
	if err == nil || !strings.Contains(err.Error(), `"@pedro"`) {
		t.Fatalf("a username was accepted as an id: %v", err)
	}
}

func TestParseCommand(t *testing.T) {
	for _, c := range []struct{ in, name, target, rest string }{
		{"/help", "help", "", ""},
		{"/Start@argos_bot", "start", "argos_bot", ""},
		{"/mes  2026-10 ", "mes", "", "2026-10"},
		{"almoço 35,90", "", "", ""},
	} {
		name, target, rest := parseCommand(c.in)
		if name != c.name || target != c.target || rest != c.rest {
			t.Errorf("parseCommand(%q) = %q, %q, %q", c.in, name, target, rest)
		}
	}
}

func TestAuthorize(t *testing.T) {
	b := &Bot{cfg: Config{AllowedUsers: map[int64]bool{42: true}}}
	private := models.Chat{ID: 42, Type: models.ChatTypePrivate}

	for _, c := range []struct {
		name string
		msg  *models.Message
		pass bool
	}{
		{"allowed, private", &models.Message{From: &models.User{ID: 42}, Chat: private}, true},
		{"stranger", &models.Message{From: &models.User{ID: 7}, Chat: models.Chat{ID: 7, Type: models.ChatTypePrivate}}, false},
		{"allowed, in a group", &models.Message{From: &models.User{ID: 42}, Chat: models.Chat{ID: -100, Type: models.ChatTypeGroup}}, false},
		{"no sender", &models.Message{Chat: private}, false},
	} {
		passed := false
		next := b.authorize(func(context.Context, *bot.Bot, *models.Update) { passed = true })
		next(context.Background(), nil, &models.Update{Message: c.msg})
		if passed != c.pass {
			t.Errorf("%s: passed=%v, want %v", c.name, passed, c.pass)
		}
	}
}
