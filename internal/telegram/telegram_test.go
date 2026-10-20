package telegram

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	for _, k := range []string{TokenEnv, AllowedUsersEnv, DataDirEnv} {
		t.Setenv(k, kv[k])
	}
}

func TestConfigReadsUsersAndDataDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	setEnv(t, map[string]string{
		TokenEnv:        "123:abc",
		AllowedUsersEnv: "42, 7",
		DataDirEnv:      dir,
	})
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AllowedUsers[42] || !cfg.AllowedUsers[7] || len(cfg.AllowedUsers) != 2 {
		t.Errorf("users: %v", cfg.AllowedUsers)
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

	setEnv(t, map[string]string{DataDirEnv: dir})
	_, err := LoadConfig()
	if err == nil {
		t.Fatal("an empty environment was accepted")
	}
	for _, want := range []string{TokenEnv, AllowedUsersEnv, DataDirEnv} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s:\n%v", want, err)
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
