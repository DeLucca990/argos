package telegram

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-telegram/bot/models"

	"github.com/DeLucca990/argos-bot/internal/extract"
	"github.com/DeLucca990/argos-bot/internal/pluggy"
	"github.com/DeLucca990/argos-bot/internal/receipt"
	"github.com/DeLucca990/argos-bot/internal/store"
)

const (
	bankPrefix   = "bk:"
	bankTimeout  = 5 * time.Minute
	bankOverlap  = 30
	bankBatch    = 40
	bankCards    = 10
	replacedDays = 7
	itemGone     = "GONE"
)

var troubled = map[string]string{
	"LOGIN_ERROR":        "a autorização venceu ou foi recusada",
	"OUTDATED":           "a última atualização no banco falhou",
	"WAITING_USER_INPUT": "o banco espera uma ação sua",
	itemGone:             "a Pluggy não encontra mais o item",
}

type bankAPI interface {
	Item(ctx context.Context, id string) (pluggy.Item, error)
	Accounts(ctx context.Context, itemID string) ([]pluggy.Account, error)
	Transactions(ctx context.Context, accountID string, from time.Time) ([]pluggy.Transaction, error)
}

type bankSync struct {
	item      store.BankItem
	connector string
	accounts  []pluggy.Account
	added     []store.Expense
	matched   int
	carried   int
	ignored   int
	waiting   int
}

type bankLine struct {
	tx      store.BankTransaction
	expense store.Expense
	prompt  extract.BankLine
}

func (b *Bot) bankFor(telegramID int64) bankAPI {
	if api, ok := b.ownBanks[telegramID]; ok {
		return api
	}
	return b.bank
}

func (b *Bot) banking() bool { return b.bank != nil || len(b.ownBanks) > 0 }

func credentialKeys(telegramID int64) string {
	return fmt.Sprintf("<code>%s_%d</code> e <code>%s_%d</code>", PluggyIDEnv, telegramID, PluggySecretEnv, telegramID)
}

func bankOffText(telegramID int64) string {
	return "A conexão com bancos não está ligada para você. Ela precisa de uma aplicação sua na Pluggy, " +
		"criada em dashboard.pluggy.ai, com o Client ID e o Client Secret dela no .env do bot, por quem cuida dele: " +
		credentialKeys(telegramID) + "."
}

func (b *Bot) ownNote(telegramID int64) string {
	if b.ownBanks[telegramID] != nil || len(b.cfg.AllowedUsers) < 2 {
		return ""
	}
	return "\n\nCada pessoa usa uma aplicação própria na Pluggy. Se a que o bot usa hoje para você não é sua, " +
		"quem cuida dele põe as credenciais da sua no .env: " + credentialKeys(telegramID) + "."
}

func (b *Bot) connectHelp(telegramID int64) string {
	return "🏦 <b>Conectar um banco</b> — Open Finance, pela Pluggy\n\n" +
		"1. Em <b>meu.pluggy.ai</b>, crie sua conta e conecte nela os seus bancos.\n" +
		"2. Em <b>dashboard.pluggy.ai</b>, com a mesma conta, na sua aplicação — a que o bot usa para você —, " +
		"abra <i>Demo</i> → <i>Connect Account</i>, escolha <b>MeuPluggy</b> e entre.\n" +
		"3. Em <i>Connected Items</i>, abra o item de cada banco e copie o id.\n" +
		"4. Mande aqui: <code>/conectar 6f1b8a3e-2c4d-4e5f-8a9b-0c1d2e3f4a5b</code>\n\n" +
		"Eu importo os gastos desde o dia 1º deste mês e, daí em diante, confiro " + everyText(b.cfg.BankEvery) + ". " +
		"Os comprovantes continuam valendo: um gasto que você já mandou não entra de novo pelo banco." +
		b.ownNote(telegramID)
}

func (b *Bot) onConnect(ctx context.Context, msg *models.Message, arg string) {
	id := strings.ToLower(strings.TrimSpace(arg))
	if b.bankFor(userFrom(ctx).TelegramID) == nil || !pluggy.ValidID(id) {
		r, _ := b.connect(ctx, userFrom(ctx), id)
		b.sendHTML(ctx, msg.Chat.ID, r.text)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, bankTimeout)
	defer cancel()
	placeholder := b.replyTo(ctx, msg, "🔌 Conectando…")
	stop := b.typing(ctx, msg.Chat.ID)
	r, s := b.connect(ctx, userFrom(ctx), id)
	stop()
	b.show(ctx, msg.Chat.ID, placeholder, r)
	b.sendCards(ctx, msg.Chat.ID, s.added)
}

func (b *Bot) connect(ctx context.Context, user store.User, id string) (reply, bankSync) {
	api := b.bankFor(user.TelegramID)
	switch {
	case api == nil:
		return reply{text: bankOffText(user.TelegramID)}, bankSync{}
	case id == "":
		return reply{text: b.connectHelp(user.TelegramID)}, bankSync{}
	case !pluggy.ValidID(id):
		return reply{text: "Isso não parece o id de um item da Pluggy, que tem 36 caracteres, como " +
			"<code>6f1b8a3e-2c4d-4e5f-8a9b-0c1d2e3f4a5b</code>. /conectar sozinho explica onde achá-lo."}, bankSync{}
	}

	it, err := api.Item(ctx, id)
	switch {
	case errors.Is(err, pluggy.ErrNotFound):
		return reply{text: "Não encontrei esse item na Pluggy. Confira o id, e se ele é da sua aplicação." +
			b.ownNote(user.TelegramID)}, bankSync{}
	case err != nil:
		log.Printf("user %d: looking up Pluggy item %s: %v", user.ID, id, err)
		return reply{text: "⚠️ A Pluggy não respondeu. Tente de novo em instantes."}, bankSync{}
	}

	item, created, err := b.store.LinkBankItem(ctx, user.ID, id, firstOfMonth(b.now()))
	switch {
	case errors.Is(err, store.ErrItemTaken):
		return reply{text: "Esse item já está conectado por outra pessoa."}, bankSync{}
	case err != nil:
		log.Printf("user %d: linking Pluggy item %s: %v", user.ID, id, err)
		return reply{text: dbErrorText}, bankSync{}
	case !created:
		return reply{text: "Esse item já está conectado. /bancos mostra as conexões."}, bankSync{}
	}
	log.Printf("user %d: linked Pluggy item %s, connector %q", user.ID, id, it.Connector.Name)

	s, err := b.syncItem(ctx, api, item, true)
	if err != nil {
		log.Printf("user %d: first import from Pluggy item %s: %v", user.ID, id, err)
		return reply{text: "🏦 Conectei, mas a primeira importação falhou. Tento de novo na próxima conferência; " +
			"/bancos mostra como está."}, s
	}
	var text strings.Builder
	text.WriteString("🏦 <b>" + html.EscapeString(nameOf(s.item)) + "</b> conectado.\n")
	if labels := accountLabels(s.connector, s.accounts); len(labels) > 0 {
		text.WriteString("Contas: " + html.EscapeString(strings.Join(labels, ", ")) + "\n")
	}
	text.WriteString("\n" + syncText(s) + "\n\nDaqui em diante eu confiro " + everyText(b.cfg.BankEvery) + " e mando os gastos novos. " +
		"/bancos mostra as conexões.")
	return reply{text: text.String()}, s
}

func (b *Bot) bankMenu(ctx context.Context, user store.User) (reply, error) {
	if b.bankFor(user.TelegramID) == nil {
		return reply{text: bankOffText(user.TelegramID)}, nil
	}
	items, err := b.store.UserBankItems(ctx, user.ID)
	if err != nil {
		return reply{}, err
	}
	if len(items) == 0 {
		return reply{text: "Nenhuma conta conectada ainda.\n\n" + b.connectHelp(user.TelegramID)}, nil
	}

	now := b.now()
	var s strings.Builder
	var rows [][]models.InlineKeyboardButton
	s.WriteString("🏦 <b>Contas conectadas</b>\n")
	for _, it := range items {
		name := nameOf(it)
		s.WriteString("\n<b>" + html.EscapeString(name) + "</b> — " + stateText(it) + "\n")
		if it.Error != "" && troubled[it.Status] != "" {
			s.WriteString("<i>" + html.EscapeString(it.Error) + "</i>\n")
		}
		s.WriteString("Gastos desde " + it.ImportFrom.Format("02/01/2006"))
		if !it.SyncedAt.IsZero() {
			s.WriteString(" · conferido " + it.SyncedAt.In(now.Location()).Format("02/01 às 15:04"))
		}
		s.WriteString("\n")
		if !it.ConsentUntil.IsZero() && it.ConsentUntil.Before(now.AddDate(0, 0, 30)) {
			s.WriteString("⏳ A autorização vence em " + it.ConsentUntil.In(now.Location()).Format("02/01/2006") +
				": renove no MeuPluggy antes disso.\n")
		}
		rows = append(rows, []models.InlineKeyboardButton{
			{Text: "🔄 " + shorten(name, 24), CallbackData: bankCallback(it.ID, "sync")},
			{Text: "🔌 Desconectar", CallbackData: bankCallback(it.ID, "off")},
		})
	}
	s.WriteString("\n🔄 busca agora o que a Pluggy já tem. 🔌 para de importar, e o que já veio fica. " +
		"Para conectar outro banco, /conectar.")
	return reply{text: s.String(), keyboard: &models.InlineKeyboardMarkup{InlineKeyboard: rows}}, nil
}

func bankCallback(id int64, action string) string {
	return bankPrefix + strconv.FormatInt(id, 10) + ":" + action
}

func (b *Bot) onBankCallback(ctx context.Context, q *models.CallbackQuery) {
	user := userFrom(ctx)
	chatID, messageID := callbackTarget(q)
	idText, action, _ := strings.Cut(strings.TrimPrefix(q.Data, bankPrefix), ":")
	id, _ := strconv.ParseInt(idText, 10, 64)
	api := b.bankFor(user.TelegramID)
	if api == nil {
		b.answer(ctx, q, "A conexão com bancos está desligada para você.")
		b.refreshBankMenu(ctx, user, chatID, messageID)
		return
	}

	item, err := b.store.BankItem(ctx, user.ID, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		b.answer(ctx, q, "Essa conexão não existe mais.")
		b.refreshBankMenu(ctx, user, chatID, messageID)
		return
	case err != nil:
		log.Printf("user %d: bank item %d: %v", user.ID, id, err)
		b.answer(ctx, q, dbErrorText)
		return
	}

	switch action {
	case "off":
		if err := b.store.UnlinkBankItem(ctx, user.ID, id); err != nil && !errors.Is(err, store.ErrNotFound) {
			log.Printf("user %d: unlinking bank item %d: %v", user.ID, id, err)
			b.answer(ctx, q, dbErrorText)
			return
		}
		log.Printf("user %d: unlinked Pluggy item %s", user.ID, item.PluggyID)
		b.answer(ctx, q, "Desconectado. O que já veio fica.")
	case "sync":
		b.answer(ctx, q, "🔄 Buscando na Pluggy…")
		ctx, cancel := context.WithTimeout(ctx, bankTimeout)
		defer cancel()
		stop := b.typing(ctx, chatID)
		s, err := b.syncItem(ctx, api, item, true)
		stop()
		if err != nil {
			log.Printf("user %d: syncing bank item %d: %v", user.ID, id, err)
			b.sendHTML(ctx, chatID, "⚠️ A Pluggy não respondeu. Tente de novo em instantes.")
			break
		}
		logSync(s)
		b.sendHTML(ctx, chatID, "🏦 <b>"+html.EscapeString(nameOf(s.item))+"</b>\n"+syncText(s))
		b.sendCards(ctx, chatID, s.added)
	default:
		b.answer(ctx, q, "Botão desconhecido.")
		return
	}
	b.refreshBankMenu(ctx, user, chatID, messageID)
}

func (b *Bot) refreshBankMenu(ctx context.Context, user store.User, chatID int64, messageID int) {
	r, err := b.bankMenu(ctx, user)
	if err != nil {
		log.Printf("user %d: /bancos: %v", user.ID, err)
		return
	}
	b.edit(ctx, chatID, messageID, r)
}

func (b *Bot) runBankSync(ctx context.Context) {
	t := time.NewTicker(b.cfg.BankEvery)
	defer t.Stop()
	for {
		b.syncAll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (b *Bot) syncAll(ctx context.Context) {
	items, err := b.store.BankItems(ctx)
	if err != nil || len(items) == 0 {
		if err != nil {
			log.Printf("bank: listing the connections: %v", err)
		}
		return
	}
	users, err := b.store.Users(ctx)
	if err != nil {
		log.Printf("bank: listing the users: %v", err)
		return
	}
	chats := map[int64]int64{}
	for _, u := range users {
		if b.cfg.AllowedUsers[u.TelegramID] {
			chats[u.ID] = u.TelegramID
		}
	}

	for _, item := range items {
		chat, ok := chats[item.UserID]
		api := b.bankFor(chat)
		if !ok || api == nil {
			continue
		}
		syncCtx, cancel := context.WithTimeout(ctx, bankTimeout)
		s, err := b.syncItem(syncCtx, api, item, false)
		cancel()
		if err != nil {
			log.Printf("bank: user %d, item %d: %v", item.UserID, item.ID, err)
			continue
		}
		logSync(s)
		if text, ok := troubleText(item, s.item); ok {
			if err := b.notify(ctx, chat, text); err != nil {
				log.Printf("bank: user %d, item %d: telling them it stopped: %v", item.UserID, item.ID, err)
			}
		}
		if len(s.added) > 0 {
			b.sendHTML(ctx, chat, "🏦 <b>"+html.EscapeString(nameOf(s.item))+"</b>\n"+syncText(s))
			b.sendCards(ctx, chat, s.added)
		}
	}
}

func (b *Bot) sendCards(ctx context.Context, chatID int64, list []store.Expense) {
	if len(list) > bankCards {
		list = list[len(list)-bankCards:]
	}
	for _, e := range list {
		r := b.card(ctx, e, false)
		r.silent = true
		if id := b.show(ctx, chatID, 0, r); id != 0 {
			if err := b.store.RememberCard(ctx, chatID, id, e.ID); err != nil {
				log.Printf("expense %d: remembering its card: %v", e.ID, err)
			}
		}
	}
}

func (b *Bot) syncItem(ctx context.Context, api bankAPI, item store.BankItem, force bool) (bankSync, error) {
	b.syncMu.Lock()
	defer b.syncMu.Unlock()
	s := bankSync{item: item}
	now := b.now()

	it, err := api.Item(ctx, item.PluggyID)
	if errors.Is(err, pluggy.ErrNotFound) {
		s.item.Status, s.item.Error, s.item.SyncedAt = itemGone, "", now
		return s, b.store.SaveBankItem(ctx, s.item)
	}
	if err != nil {
		return s, err
	}
	s.connector = it.Connector.Name
	s.item.Status, s.item.Error, s.item.ConsentUntil = it.Status, "", time.Time{}
	if it.Error != nil {
		s.item.Error = nonEmpty(it.Error.Message, it.Error.Code)
	}
	if it.ConsentExpiresAt != nil {
		s.item.ConsentUntil = *it.ConsentExpiresAt
	}
	var updated time.Time
	if it.LastUpdatedAt != nil {
		updated = it.LastUpdatedAt.Truncate(time.Second)
	}
	if !force && !updated.IsZero() && updated.Equal(item.ImportedUpTo) {
		s.item.SyncedAt = now
		return s, b.store.SaveBankItem(ctx, s.item)
	}

	if s.accounts, err = api.Accounts(ctx, item.PluggyID); err != nil {
		return s, err
	}
	s.item.Name = itemName(s.connector, s.accounts)
	cats, err := b.categories(ctx, item.UserID)
	if err != nil {
		return s, err
	}

	from := item.ImportFrom
	if !item.SyncedAt.IsZero() {
		if since := startOfDay(item.SyncedAt.In(now.Location())).AddDate(0, 0, -bankOverlap); since.After(from) {
			from = since
		}
	}
	var todo []bankLine
	for _, a := range s.accounts {
		txs, err := api.Transactions(ctx, a.ID, from)
		if err != nil {
			return s, err
		}
		lines, err := b.reconcile(ctx, &s, a, txs, from, now)
		if err != nil {
			return s, err
		}
		todo = append(todo, lines...)
	}
	if err := b.file(ctx, &s, todo, cats, now); err != nil {
		return s, err
	}
	sort.SliceStable(s.added, func(i, j int) bool {
		x, y := s.added[i], s.added[j]
		if !x.SpentOn.Equal(y.SpentOn) {
			return x.SpentOn.Before(y.SpentOn)
		}
		return x.SpentTime < y.SpentTime
	})

	s.item.SyncedAt = now
	if s.waiting == 0 {
		s.item.ImportedUpTo = updated
	}
	return s, b.store.SaveBankItem(ctx, s.item)
}

func (b *Bot) reconcile(ctx context.Context, s *bankSync, a pluggy.Account, txs []pluggy.Transaction, from, now time.Time) ([]bankLine, error) {
	ids := make([]string, len(txs))
	present := map[string]bool{}
	for i, t := range txs {
		ids[i], present[t.ID] = t.ID, true
	}
	known, err := b.store.KnownBankTransactions(ctx, ids)
	if err != nil {
		return nil, err
	}
	recent, err := b.store.BankTransactions(ctx, a.ID, from)
	if err != nil {
		return nil, err
	}
	claimed := map[int64]bool{}
	for _, r := range recent {
		if present[r.PluggyID] && r.ExpenseID != 0 {
			claimed[r.ExpenseID] = true
		}
	}

	var todo []bankLine
	for _, t := range txs {
		if known[t.ID] {
			continue
		}
		line, ok := spendingLine(s.item.UserID, s.connector, a, t, now)
		if !ok {
			continue
		}
		prev, ok, err := b.recorded(ctx, s.item.UserID, recent, present, claimed, line.tx)
		if err != nil {
			return nil, err
		}
		if ok {
			if prev.AccountID != a.ID && prev.ExpenseID != 0 {
				s.carried++
			}
			present[prev.PluggyID] = true
			if prev.ExpenseID != 0 {
				claimed[prev.ExpenseID] = true
			}
			line.tx.ExpenseID = prev.ExpenseID
			if err := b.store.RecordBankTransaction(ctx, line.tx); err != nil {
				return nil, err
			}
			continue
		}
		e, found, err := b.store.MatchExpense(ctx, s.item.UserID, line.tx.TotalCents, line.tx.SpentOn)
		if err != nil {
			return nil, err
		}
		if found {
			line.tx.ExpenseID = e.ID
			if err := b.store.RecordBankTransaction(ctx, line.tx); err != nil {
				return nil, err
			}
			s.matched++
			continue
		}
		todo = append(todo, line)
	}
	return todo, nil
}

// Pluggy's ids change when a transaction is recreated or its item is connected again;
// the Open Finance id, when the bank sends one, does not.
func (b *Bot) recorded(ctx context.Context, userID int64, recent []store.BankTransaction, present map[string]bool, claimed map[int64]bool, t store.BankTransaction) (store.BankTransaction, bool, error) {
	prev, err := b.store.BankTransactionByProvider(ctx, userID, t.ProviderID)
	switch {
	case err == nil:
		return prev, true, nil
	case !errors.Is(err, store.ErrNotFound):
		return store.BankTransaction{}, false, err
	}
	prev, ok := replaced(recent, present, claimed, t)
	return prev, ok, nil
}

// Without an Open Finance id, a recreated transaction is the one of the same amount, within a
// week, that vanished from the account's listing.
func replaced(recent []store.BankTransaction, present map[string]bool, claimed map[int64]bool, t store.BankTransaction) (store.BankTransaction, bool) {
	for _, r := range recent {
		days := r.SpentOn.Sub(t.SpentOn).Hours() / 24
		if !present[r.PluggyID] && r.ExpenseID != 0 && !claimed[r.ExpenseID] && r.TotalCents == t.TotalCents &&
			days <= replacedDays && days >= -replacedDays {
			return r, true
		}
	}
	return store.BankTransaction{}, false
}

func (b *Bot) file(ctx context.Context, s *bankSync, lines []bankLine, cats catalog, now time.Time) error {
	var ask []bankLine
	for _, l := range lines {
		prev, e, found, err := b.store.LastFiled(ctx, s.item.UserID, l.tx.Description)
		if err != nil {
			return err
		}
		switch c := cats.of(e.Category); {
		case !found || l.prompt.Installment != "":
			ask = append(ask, l)
		case prev.ExpenseID == 0:
			if err := b.store.RecordBankTransaction(ctx, l.tx); err != nil {
				return err
			}
			s.ignored++
		case c.ID == 0 || c.Archived:
			ask = append(ask, l)
		default:
			l.expense.Merchant, l.expense.Description, l.expense.Category = e.Merchant, e.Description, e.Category
			if l.expense.MerchantTaxID == "" {
				l.expense.MerchantTaxID = e.MerchantTaxID
			}
			if err := b.addBankExpense(ctx, s, l); err != nil {
				return err
			}
		}
	}

	for len(ask) > 0 {
		chunk := ask[:min(len(ask), bankBatch)]
		filed, err := b.classify(ctx, s.item.UserID, chunk, cats, now)
		if err != nil {
			log.Printf("user %d: filing %d bank transaction(s): %v", s.item.UserID, len(ask), err)
			s.waiting += len(ask)
			return nil
		}
		ask = ask[len(chunk):]
		for i, l := range chunk {
			f := filed[i]
			if !f.Spending {
				if err := b.store.RecordBankTransaction(ctx, l.tx); err != nil {
					return err
				}
				s.ignored++
				continue
			}
			l.expense.Merchant, l.expense.Description = strings.TrimSpace(f.Merchant), strings.TrimSpace(f.Description)
			l.expense.Category = f.Category
			if !cats.has(f.Category) {
				l.expense.Category = store.FallbackCategory
			}
			if err := b.addBankExpense(ctx, s, l); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b *Bot) classify(ctx context.Context, userID int64, lines []bankLine, cats catalog, now time.Time) ([]extract.Filed, error) {
	if _, over := b.overBudget(ctx, now); over {
		return nil, errors.New("the daily budget is spent")
	}
	select {
	case b.slots <- struct{}{}:
		defer func() { <-b.slots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	prompt := make([]extract.BankLine, len(lines))
	for i, l := range lines {
		prompt[i] = l.prompt
	}
	filed, res, err := b.extractor.Classify(ctx, extract.Statement{Lines: prompt, Today: now, Categories: cats.forModel("")})
	record := b.recordOf(userID, 0, res, err)
	record.Kind = store.KindBank
	b.record(ctx, record)
	log.Printf("user %d: %d bank transaction(s) filed by %s, $%.4f", userID, len(lines), nonEmpty(res.Model, "nobody"), res.Spend.USD)
	return filed, err
}

func (b *Bot) addBankExpense(ctx context.Context, s *bankSync, l bankLine) error {
	id, err := b.store.AddBankExpense(ctx, l.tx, l.expense)
	if err != nil {
		return err
	}
	e, err := b.store.Expense(ctx, id)
	if err != nil {
		return err
	}
	s.added = append(s.added, e)
	return nil
}

func spendingLine(userID int64, connector string, a pluggy.Account, t pluggy.Transaction, now time.Time) (bankLine, bool) {
	if t.Type != "DEBIT" || t.OperationType == "PAGAMENTO_FATURA" || ownTransfer(a, t) {
		return bankLine{}, false
	}
	amount, currency := t.Amount, t.CurrencyCode
	if t.AmountInAccountCurrency != "" {
		amount, currency = t.AmountInAccountCurrency, a.CurrencyCode
	}
	cents, err := receipt.ParseCents(amount.String())
	if cents < 0 {
		cents = -cents
	}
	on, clock := whenOf(t.Date)
	if err != nil || cents == 0 || on.After(now) {
		return bankLine{}, false
	}
	currency = strings.ToUpper(nonEmpty(strings.TrimSpace(currency), "BRL"))

	account := accountLabel(connector, a)
	payment := paymentOf(a, t)
	description := strings.TrimSpace(t.Description)
	installment := ""
	if m := t.CreditCardMetadata; m != nil && m.TotalInstallments > 1 && m.InstallmentNumber > 0 {
		installment = fmt.Sprintf("%d/%d", m.InstallmentNumber, m.TotalInstallments)
	}
	kind := "conta corrente"
	switch {
	case a.Type == "CREDIT":
		kind = "cartão de crédito"
	case a.Subtype == "SAVINGS_ACCOUNT":
		kind = "poupança"
	}

	return bankLine{
		tx: store.BankTransaction{PluggyID: t.ID, UserID: userID, AccountID: a.ID, ProviderID: t.ProviderID,
			SpentOn: on, TotalCents: cents, Description: description},
		expense: store.Expense{UserID: userID, Status: store.Confirmed, Source: store.SourceBank, SpentOn: on,
			SpentTime: clock, TotalCents: cents, Currency: currency, PaymentMethod: payment, Account: account,
			CardLast4: cardOf(a, t), MerchantTaxID: taxIDOf(t)},
		prompt: extract.BankLine{Date: on.Format("2006-01-02"), Amount: dotDecimal(cents), Currency: currency,
			Description: description, Account: strings.TrimPrefix(account+", "+kind, ", "),
			Method: nonEmpty(t.OperationType, paymentMethodOf(t)), PaidTo: counterparty(t), Installment: installment,
			BankCategory: t.Category},
	}, true
}

// Pluggy sends instants in UTC. One at midnight UTC is a bare date; any other is a moment,
// read in local time, and only a moment that is not local midnight has a time of day worth showing.
func whenOf(d time.Time) (time.Time, string) {
	if u := d.UTC(); u.Hour() == 0 && u.Minute() == 0 && u.Second() == 0 {
		return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.Local), ""
	}
	l := d.In(time.Local)
	on := time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, time.Local)
	if l.Hour() == 0 && l.Minute() == 0 {
		return on, ""
	}
	return on, l.Format("15:04")
}

func ownTransfer(a pluggy.Account, t pluggy.Transaction) bool {
	p := t.PaymentData
	if p == nil || p.Receiver == nil || p.Receiver.DocumentNumber == nil {
		return false
	}
	to := onlyDigits(p.Receiver.DocumentNumber.Value)
	if to == "" {
		return false
	}
	if p.Payer != nil && p.Payer.DocumentNumber != nil && onlyDigits(p.Payer.DocumentNumber.Value) == to {
		return true
	}
	return onlyDigits(a.TaxNumber) == to
}

func paymentMethodOf(t pluggy.Transaction) string {
	if t.PaymentData != nil {
		return t.PaymentData.PaymentMethod
	}
	return ""
}

func paymentOf(a pluggy.Account, t pluggy.Transaction) string {
	if a.Type == "CREDIT" {
		return "credito"
	}
	switch nonEmpty(paymentMethodOf(t), t.OperationType) {
	case "PIX":
		return "pix"
	case "BOLETO", "CONVENIO_ARRECADACAO":
		return "boleto"
	case "CARTAO":
		return "debito"
	}
	return ""
}

func cardOf(a pluggy.Account, t pluggy.Transaction) string {
	if a.Type != "CREDIT" {
		return ""
	}
	var used string
	if t.CreditCardMetadata != nil {
		used = t.CreditCardMetadata.CardNumber
	}
	for _, n := range []string{used, a.Number} {
		if d := onlyDigits(n); len(d) >= 4 {
			return d[len(d)-4:]
		}
	}
	return ""
}

func taxIDOf(t pluggy.Transaction) string {
	var candidates []string
	if p := t.PaymentData; p != nil && p.Receiver != nil && p.Receiver.DocumentNumber != nil {
		candidates = append(candidates, p.Receiver.DocumentNumber.Value)
	}
	if t.Merchant != nil {
		candidates = append(candidates, t.Merchant.CNPJ)
	}
	for _, c := range candidates {
		id := onlyDigits(c)
		if (len(id) == 14 && receipt.ValidCNPJ(id)) || (len(id) == 11 && receipt.ValidCPF(id)) {
			return id
		}
	}
	return ""
}

func counterparty(t pluggy.Transaction) string {
	if p := t.PaymentData; p != nil && p.Receiver != nil && strings.TrimSpace(p.Receiver.Name) != "" {
		return strings.TrimSpace(p.Receiver.Name)
	}
	if m := t.Merchant; m != nil {
		return strings.TrimSpace(nonEmpty(m.Name, m.BusinessName))
	}
	return ""
}

func viaMeuPluggy(connector string) bool {
	return strings.Contains(strings.ReplaceAll(strings.ToLower(connector), " ", ""), "meupluggy")
}

func accountLabel(connector string, a pluggy.Account) string {
	if connector != "" && !viaMeuPluggy(connector) {
		return connector
	}
	return strings.TrimSpace(nonEmpty(a.MarketingName, a.Name))
}

func accountLabels(connector string, accounts []pluggy.Account) []string {
	var labels []string
	for _, a := range accounts {
		label := strings.TrimSpace(nonEmpty(a.MarketingName, a.Name, a.Type))
		if d := onlyDigits(a.Number); a.Type == "CREDIT" && len(d) >= 4 {
			label += " ···" + d[len(d)-4:]
		}
		labels = append(labels, label)
	}
	return labels
}

func itemName(connector string, accounts []pluggy.Account) string {
	if connector != "" && !viaMeuPluggy(connector) {
		return connector
	}
	for _, a := range accounts {
		if label := accountLabel(connector, a); label != "" {
			return label
		}
	}
	return connector
}

func nameOf(it store.BankItem) string { return nonEmpty(it.Name, "Banco") }

func stateText(it store.BankItem) string {
	switch it.Status {
	case "":
		return "ainda não conferida"
	case "UPDATED":
		return "✅ em dia"
	case "UPDATING", "CREATING", "MERGING":
		return "⏳ atualizando no banco"
	}
	if why, ok := troubled[it.Status]; ok {
		return "⚠️ " + why
	}
	return html.EscapeString(it.Status)
}

func troubleText(before, after store.BankItem) (string, bool) {
	why, bad := troubled[after.Status]
	if !bad || before.Status == after.Status {
		return "", false
	}
	text := "⚠️ <b>" + html.EscapeString(nameOf(after)) + "</b>: " + why
	if after.Error != "" {
		text += " (" + html.EscapeString(after.Error) + ")"
	}
	return text + ".\n\nEnquanto isso, gastos novos dele não chegam. Se a autorização venceu, entre de novo no " +
		"MeuPluggy pelo dashboard da Pluggy — o passo 2 de /conectar; se o id do item mudou, conecte o novo com " +
		"/conectar e desconecte este em /bancos.", true
}

func syncText(s bankSync) string {
	var parts []string
	if n := len(s.added); n == 0 {
		parts = append(parts, "Nenhum gasto novo.")
	} else {
		var total int64
		for _, e := range s.added {
			total += e.TotalCents
		}
		parts = append(parts, fmt.Sprintf("💰 <b>%d %s</b>, %s.", n, plural(n, "gasto novo", "gastos novos"),
			receipt.FormatBRL(total)))
	}
	if s.matched > 0 {
		parts = append(parts, fmt.Sprintf("🔗 %d já %s pelos seus comprovantes.", s.matched,
			plural(s.matched, "estava registrado", "estavam registrados")))
	}
	if s.carried > 0 {
		parts = append(parts, fmt.Sprintf("🔁 %d já %s por outra conexão.", s.carried,
			plural(s.carried, "tinha vindo", "tinham vindo")))
	}
	if s.ignored > 0 {
		parts = append(parts, fmt.Sprintf("↔️ %d %s de fora por não ser gasto: fatura do cartão, transferência "+
			"entre contas suas, investimento ou saque.", s.ignored, plural(s.ignored, "ficou", "ficaram")))
	}
	if s.waiting > 0 {
		parts = append(parts, fmt.Sprintf("⏳ %d %s a leitura — o teto diário foi atingido ou a leitura falhou; "+
			"tento de novo na próxima conferência.", s.waiting, plural(s.waiting, "espera", "esperam")))
	}
	if len(s.added) > bankCards {
		parts = append(parts, fmt.Sprintf("Mostro os %d mais recentes; os outros estão em /ultimos.", bankCards))
	}
	return strings.Join(parts, "\n")
}

func logSync(s bankSync) {
	if len(s.added)+s.matched+s.carried+s.ignored+s.waiting == 0 {
		return
	}
	log.Printf("bank: user %d, item %d: %d added, %d matched to receipts, %d came through another connection, "+
		"%d not spending, %d waiting", s.item.UserID, s.item.ID, len(s.added), s.matched, s.carried, s.ignored, s.waiting)
}

func everyText(d time.Duration) string {
	day := 24 * time.Hour
	switch {
	case d == day:
		return "uma vez por dia"
	case d%day == 0:
		return fmt.Sprintf("a cada %d dias", d/day)
	case d == time.Hour:
		return "a cada hora"
	case d%time.Hour == 0:
		return fmt.Sprintf("a cada %d horas", d/time.Hour)
	case d == time.Minute:
		return "a cada minuto"
	case d < time.Hour:
		return fmt.Sprintf("a cada %d minutos", d/time.Minute)
	}
	return fmt.Sprintf("a cada %dh%02d", d/time.Hour, d%time.Hour/time.Minute)
}

func firstOfMonth(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
}

func onlyDigits(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
}

func shorten(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}
