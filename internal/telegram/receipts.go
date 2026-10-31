package telegram

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/DeLucca990/argos/internal/extract"
	"github.com/DeLucca990/argos/internal/receipt"
	"github.com/DeLucca990/argos/internal/store"
)

const (
	receiptsDir    = "receipts"
	maxDownload    = 20 << 20
	readTimeout    = 3 * time.Minute
	maxConcurrency = 2
)

var extensions = map[string]string{
	"image/jpeg": ".jpg", "image/png": ".png", "image/gif": ".gif", "image/webp": ".webp", "application/pdf": ".pdf",
}

type extractor interface {
	Extract(ctx context.Context, in extract.Input) (extract.Result, error)
	Correct(ctx context.Context, c extract.Correction) (extract.Result, error)
	Classify(ctx context.Context, s extract.Statement) ([]extract.Filed, extract.Result, error)
}

type incoming struct {
	data   []byte
	mime   string
	source store.Source
	text   string
}

func (b *Bot) onFile(ctx context.Context, msg *models.Message) {
	var (
		fileID string
		size   int64
		in     = incoming{text: msg.Caption}
	)
	switch {
	case len(msg.Photo) > 0:
		photo := msg.Photo[len(msg.Photo)-1]
		fileID, size, in.mime, in.source = photo.FileID, int64(photo.FileSize), "image/jpeg", store.SourcePhoto
	default:
		doc := msg.Document
		fileID, size, in.mime = doc.FileID, doc.FileSize, strings.ToLower(doc.MimeType)
		switch {
		case in.mime == "image/heic" || in.mime == "image/heif":
			b.sendHTML(ctx, msg.Chat.ID, "Ainda não leio fotos HEIC. Mande como foto, não como arquivo, ou exporte em JPEG.")
			return
		case in.mime != "" && !extract.Supported(in.mime):
			b.sendHTML(ctx, msg.Chat.ID, "Não leio arquivos "+escape(in.mime)+". Mande uma foto, uma imagem (JPEG, PNG, WebP) ou um PDF.")
			return
		}
		in.source = store.SourcePhoto
		if in.mime == "application/pdf" {
			in.source = store.SourcePDF
		}
	}
	if size > maxDownload {
		b.sendHTML(ctx, msg.Chat.ID, "O Telegram só deixa bots baixarem arquivos de até 20 MB.")
		return
	}

	b.process(ctx, msg, "🔎 Lendo…", func(ctx context.Context) reply {
		data, err := b.download(ctx, fileID)
		if err != nil {
			log.Printf("chat %d: downloading the file: %v", msg.Chat.ID, err)
			return reply{text: "⚠️ Não consegui baixar o arquivo do Telegram. Tente de novo."}
		}
		if sniffed := http.DetectContentType(data); extract.Supported(sniffed) {
			in.mime = sniffed
		} else if !extract.Supported(in.mime) {
			return reply{text: "Não reconheci o formato do arquivo. Mande uma foto, uma imagem (JPEG, PNG, WebP) ou um PDF."}
		}
		in.data = data
		return b.read(ctx, userFrom(ctx), in)
	})
}

func (b *Bot) onText(ctx context.Context, msg *models.Message) {
	in := incoming{text: msg.Text, source: store.SourceText}
	b.process(ctx, msg, "🔎 Lendo…", func(ctx context.Context) reply { return b.read(ctx, userFrom(ctx), in) })
}

func (b *Bot) process(ctx context.Context, msg *models.Message, label string, work func(context.Context) reply) {
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()

	placeholder := b.replyTo(ctx, msg, label)
	stop := b.typing(ctx, msg.Chat.ID)
	defer stop()

	select {
	case b.slots <- struct{}{}:
		defer func() { <-b.slots }()
	case <-ctx.Done():
		b.show(ctx, msg.Chat.ID, placeholder, reply{text: "⚠️ Demorou demais. Tente de novo."})
		return
	}
	r := work(ctx)
	if id := b.show(ctx, msg.Chat.ID, placeholder, r); id != 0 && r.expenseID != 0 {
		if err := b.store.RememberCard(ctx, msg.Chat.ID, id, r.expenseID); err != nil {
			log.Printf("expense %d: remembering its card: %v", r.expenseID, err)
		}
	}
}

func (b *Bot) read(ctx context.Context, user store.User, in incoming) reply {
	now := b.now()
	var att store.Attachment
	if in.data != nil {
		var err error
		if att, err = b.saveAttachment(ctx, in.data, in.mime); err != nil {
			log.Printf("user %d: saving the file: %v", user.ID, err)
			return reply{text: dbErrorText}
		}
		if dup, found, err := b.store.Duplicate(ctx, store.Expense{AttachmentID: att.ID}); err != nil {
			log.Printf("user %d: looking for the file's expense: %v", user.ID, err)
			return reply{text: dbErrorText}
		} else if found {
			return reply{text: duplicateText(dup)}
		}
	}

	if r, over := b.overBudget(ctx, now); over {
		return r
	}
	cats, err := b.categories(ctx, user.ID)
	if err != nil {
		log.Printf("user %d: %v", user.ID, err)
		return reply{text: dbErrorText}
	}

	res, err := b.extractor.Extract(ctx, extract.Input{MIME: in.mime, Data: in.data, Text: in.text, Today: now,
		Categories: cats.forModel(""), Lessons: b.lessonsFor(ctx, user.ID, cats)})
	record := b.recordOf(user.ID, att.ID, res, err)
	logRead(user, res, err)
	if err != nil {
		b.record(ctx, record)
		if errors.Is(err, extract.ErrUnsupported) {
			return reply{text: "Não reconheci o formato do arquivo. Mande uma foto, uma imagem (JPEG, PNG, WebP) ou um PDF."}
		}
		return reply{text: "⚠️ Não consegui ler: " + escape(friendly(err))}
	}

	r, err := receipt.Parse(res.Raw, now)
	var notReceipt *receipt.NotReceiptError
	switch {
	case errors.As(err, &notReceipt):
		b.record(ctx, record)
		return reply{text: "🤔 Isso não parece um comprovante" + reason(notReceipt.Reason)}
	case errors.Is(err, receipt.ErrNoTotal):
		b.record(ctx, record)
		return reply{text: "Não encontrei o valor total. Mande uma foto mais nítida, ou escreva o gasto, como <i>mercado 87,40</i>."}
	}

	id, err := b.store.AddExpense(ctx, expenseFrom(r, cats, user.ID, in.source, att.ID))
	var dup *store.DuplicateError
	switch {
	case errors.As(err, &dup):
		b.record(ctx, record)
		return reply{text: duplicateText(dup.Existing)}
	case err != nil:
		b.record(ctx, record)
		log.Printf("user %d: recording the expense: %v", user.ID, err)
		return reply{text: dbErrorText}
	}

	record.ExpenseID = id
	b.record(ctx, record)
	log.Printf("user %d: expense %d saved", user.ID, id)
	if absorbed, found, err := b.store.AbsorbBankExpense(ctx, user.ID, id); err != nil {
		log.Printf("user %d: looking for expense %d in the bank's: %v", user.ID, id, err)
	} else if found {
		log.Printf("user %d: expense %d replaces expense %d, imported from the bank", user.ID, id, absorbed.ID)
	}
	e, err := b.store.Expense(ctx, id)
	if err != nil {
		log.Printf("expense %d: reading it back: %v", id, err)
		return reply{text: dbErrorText}
	}
	out := b.card(ctx, e, false)
	out.expenseID = id
	return out
}

func (b *Bot) recordOf(userID, attachmentID int64, res extract.Result, err error) store.Extraction {
	return store.Extraction{
		UserID: userID, AttachmentID: attachmentID, Model: nonEmpty(res.Model, b.cfg.Model), RawJSON: res.RawJSON,
		Failed: err != nil, InputTokens: res.Spend.Input, OutputTokens: res.Spend.Output,
		CacheReadTokens: res.Spend.CacheRead, CacheWriteTokens: res.Spend.CacheWrite, USD: res.Spend.USD,
	}
}

func expenseFrom(r receipt.Receipt, cats catalog, userID int64, source store.Source, attachmentID int64) store.Expense {
	category := r.Category
	if !cats.has(category) {
		category = store.FallbackCategory
	}
	items := make([]store.Item, 0, len(r.Items))
	for _, it := range r.Items {
		items = append(items, store.Item{Description: it.Description, Quantity: it.Quantity, UnitCents: it.UnitCents, TotalCents: it.TotalCents})
	}
	return store.Expense{
		UserID: userID, SpentOn: r.SpentOn, SpentTime: r.SpentTime, Merchant: r.Merchant,
		MerchantTaxID: r.MerchantTaxID, Category: category, PaymentMethod: r.PaymentMethod, Account: r.Account, CardLast4: r.CardLast4,
		TotalCents: r.TotalCents, Currency: r.Currency, Description: r.Description, NFCeKey: r.NFCeKey,
		Source: source, AttachmentID: attachmentID, Items: items, Status: store.Confirmed,
	}
}

func (b *Bot) record(ctx context.Context, x store.Extraction) {
	if _, err := b.store.RecordExtraction(ctx, x); err != nil {
		log.Printf("user %d: recording what the read cost ($%.4f): %v", x.UserID, x.USD, err)
	}
}

func (b *Bot) saveAttachment(ctx context.Context, data []byte, mime string) (store.Attachment, error) {
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	if b.cfg.DeleteFiles {
		return b.store.AddAttachment(ctx, store.Attachment{SHA256: hash, MIME: mime, Size: int64(len(data))})
	}
	rel := filepath.Join(receiptsDir, hash+extensions[mime])
	file := filepath.Join(b.cfg.DataDir, rel)

	if _, err := os.Stat(file); errors.Is(err, os.ErrNotExist) {
		tmp, err := os.CreateTemp(filepath.Dir(file), ".incoming-*")
		if err != nil {
			return store.Attachment{}, err
		}
		_, err = tmp.Write(data)
		if cerr := tmp.Close(); err == nil {
			err = cerr
		}
		if err == nil {
			err = os.Rename(tmp.Name(), file)
		}
		if err != nil {
			os.Remove(tmp.Name())
			return store.Attachment{}, err
		}
	}
	return b.store.AddAttachment(ctx, store.Attachment{SHA256: hash, Path: rel, MIME: mime, Size: int64(len(data))})
}

func (b *Bot) download(ctx context.Context, fileID string) ([]byte, error) {
	f, err := b.tg.GetFile(ctx, &bot.GetFileParams{FileID: fileID})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.tg.FileDownloadLink(f), nil)
	if err != nil {
		return nil, errors.New("building the download request")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("telegram answered %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDownload+1))
	if err == nil && len(data) > maxDownload {
		err = errors.New("the file is over 20 MB")
	}
	return data, err
}

func logRead(user store.User, res extract.Result, err error) {
	line := fmt.Sprintf("user %d: read by %s, $%.4f", user.ID, nonEmpty(res.Model, "nobody"), res.Spend.USD)
	if len(res.Spend.Models) > 1 {
		line += " — attempts by " + strings.Join(res.Spend.Models, ", ")
	}
	if len(res.Spend.Unpriced) > 0 {
		line += " — no price for " + strings.Join(res.Spend.Unpriced, ", ")
	}
	if err != nil {
		line += ", failed: " + err.Error()
	}
	log.Print(line)
}

func friendly(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "declined"):
		return "o modelo se recusou a ler este arquivo."
	case strings.Contains(msg, "ran out of tokens"):
		return "o comprovante é longo demais para uma leitura."
	case strings.Contains(msg, "too large"):
		return "a imagem é grande demais."
	case strings.HasPrefix(msg, "reading the image"), strings.HasPrefix(msg, "decoding the image"):
		return "a imagem parece corrompida."
	}
	return "o serviço de leitura falhou. Tente de novo em instantes."
}

func reason(r string) string {
	if r == "" {
		return "."
	}
	return ": " + escape(strings.TrimSuffix(r, ".")) + "."
}

func hasDigit(s string) bool { return strings.IndexFunc(s, unicode.IsDigit) >= 0 }

func escape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func (b *Bot) purgeFiles(ctx context.Context) error {
	dir := filepath.Join(b.cfg.DataDir, receiptsDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	removed := 0
	for _, e := range entries {
		if e.Type().IsRegular() {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
				return err
			}
			removed++
		}
	}
	forgotten, err := b.store.ForgetFiles(ctx)
	if err != nil {
		return err
	}
	log.Printf("receipts: not kept (%s=true); removed %d file(s) kept before, %d attachment(s) now without one",
		DeleteFilesEnv, removed, forgotten)
	return nil
}
