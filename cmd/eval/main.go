package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"

	"github.com/DeLucca990/argos-bot/internal/dotenv"
	"github.com/DeLucca990/argos-bot/internal/extract"
	"github.com/DeLucca990/argos-bot/internal/receipt"
	"github.com/DeLucca990/argos-bot/internal/store"
)

var types = map[string]string{
	".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png", ".webp": "image/webp", ".gif": "image/gif",
	".pdf": "application/pdf",
}

type expected struct {
	Merchant      string `json:"merchant"`
	MerchantTaxID string `json:"merchant_tax_id"`
	Date          string `json:"date"`
	Total         string `json:"total"`
	PaymentMethod string `json:"payment_method"`
	Account       string `json:"payment_account"`
	CardLast4     string `json:"card_last4"`
	Category      string `json:"category"`
	NFCeAccessKey string `json:"nfce_access_key"`
}

type field struct {
	name  string
	check func(want, got expected) (ok, applies bool)
}

var fields = []field{
	{"total", func(w, g expected) (bool, bool) {
		want, err1 := receipt.ParseCents(w.Total)
		got, err2 := receipt.ParseCents(g.Total)
		return err1 == nil && err2 == nil && want == got, true
	}},
	{"date", func(w, g expected) (bool, bool) { return w.Date == g.Date, true }},
	{"payment", func(w, g expected) (bool, bool) { return w.PaymentMethod == g.PaymentMethod, true }},
	{"category", func(w, g expected) (bool, bool) { return w.Category == g.Category, true }},
	{"merchant", func(w, g expected) (bool, bool) {
		want, got := letters(w.Merchant), letters(g.Merchant)
		return want != "" && got != "" && (strings.Contains(got, want) || strings.Contains(want, got)), w.Merchant != ""
	}},
	{"account", func(w, g expected) (bool, bool) {
		want, got := letters(w.Account), letters(g.Account)
		return want != "" && got != "" && (strings.Contains(got, want) || strings.Contains(want, got)), w.Account != ""
	}},
	{"card", func(w, g expected) (bool, bool) {
		return letters(w.CardLast4) == letters(g.CardLast4), w.CardLast4 != ""
	}},
	{"cnpj", func(w, g expected) (bool, bool) {
		return letters(w.MerchantTaxID) == letters(g.MerchantTaxID), w.MerchantTaxID != ""
	}},
	{"nfce", func(w, g expected) (bool, bool) { return w.NFCeAccessKey == g.NFCeAccessKey, w.NFCeAccessKey != "" }},
}

func main() {
	log.SetFlags(0)
	dir := flag.String("dir", "testdata/receipts", "the receipts, each beside a .json with what it should read as")
	model := flag.String("model", "", "the model to evaluate; ARGOS_MODEL, or claude-opus-5-5, when unset")
	effort := flag.String("effort", "", "its effort; ARGOS_EFFORT, or low, when unset")
	initMode := flag.Bool("init", false, "write a .json for every receipt that has none, from this model's reading, to correct by hand")
	flag.Parse()

	dotenv.LoadEnvVariables()
	*model = nonEmpty(*model, os.Getenv("ARGOS_MODEL"), "claude-opus-5-5")
	*effort = nonEmpty(*effort, os.Getenv("ARGOS_EFFORT"), "low")

	x := extract.New(*model, *effort)
	categories, err := defaultCategories()
	if err != nil {
		log.Fatal(err)
	}
	files, err := receipts(*dir)
	if err != nil {
		log.Fatal(err)
	}
	if len(files) == 0 {
		log.Fatalf("no receipts in %s: put photos or PDFs there, then make eval-init", *dir)
	}

	ctx := context.Background()
	out := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	header := []string{"receipt"}
	for _, f := range fields {
		header = append(header, f.name)
	}
	fmt.Fprintln(out, strings.Join(append(header, "US$"), "\t"))

	hits, counted := map[string]int{}, map[string]int{}
	var notes []string
	var usd float64
	read := 0
	for _, file := range files {
		name := filepath.Base(file)
		wantFile := strings.TrimSuffix(file, filepath.Ext(file)) + ".json"
		var want expected
		raw, err := os.ReadFile(wantFile)
		switch {
		case errors.Is(err, os.ErrNotExist) && !*initMode:
			notes = append(notes, name+": no "+filepath.Base(wantFile)+" yet — make eval-init writes one to review")
			continue
		case errors.Is(err, os.ErrNotExist):
		case err != nil:
			log.Fatal(err)
		default:
			if *initMode {
				continue
			}
			if err := json.Unmarshal(raw, &want); err != nil {
				log.Fatalf("%s: %v", wantFile, err)
			}
		}

		data, err := os.ReadFile(file)
		if err != nil {
			log.Fatal(err)
		}
		res, err := x.Extract(ctx, extract.Input{MIME: types[strings.ToLower(filepath.Ext(file))], Data: data, Today: time.Now(), Categories: categories})
		usd += res.Spend.USD
		read++
		got := expected{Merchant: res.Raw.Merchant, MerchantTaxID: res.Raw.MerchantTaxID, Date: res.Raw.Date,
			Total: res.Raw.Total, PaymentMethod: res.Raw.PaymentMethod, Account: res.Raw.PaymentAccount,
			CardLast4: res.Raw.CardLast4, Category: res.Raw.Category, NFCeAccessKey: res.Raw.NFCeAccessKey}

		if *initMode {
			if err != nil {
				notes = append(notes, name+": "+err.Error())
				continue
			}
			body, _ := json.MarshalIndent(got, "", "  ")
			if err := os.WriteFile(wantFile, append(body, '\n'), 0o600); err != nil {
				log.Fatal(err)
			}
			notes = append(notes, "wrote "+wantFile+" — check every field before trusting it")
			continue
		}

		row := []string{name}
		for _, f := range fields {
			ok, applies := f.check(want, got)
			switch {
			case !applies:
				row = append(row, "·")
				continue
			case err == nil && ok:
				row = append(row, "✓")
				hits[f.name]++
			default:
				row = append(row, "✗")
				if err == nil {
					notes = append(notes, fmt.Sprintf("%s: %s read %s, expected %s", name, f.name, show(f.name, got), show(f.name, want)))
				}
			}
			counted[f.name]++
		}
		if err != nil {
			notes = append(notes, name+": "+err.Error())
		}
		fmt.Fprintln(out, strings.Join(append(row, fmt.Sprintf("%.4f", res.Spend.USD)), "\t"))
	}

	if !*initMode {
		out.Flush()
		fmt.Println()
	}
	for _, n := range notes {
		fmt.Println(n)
	}
	if *initMode || read == 0 {
		return
	}

	var scores []string
	for _, f := range fields {
		if counted[f.name] > 0 {
			scores = append(scores, fmt.Sprintf("%s %d/%d", f.name, hits[f.name], counted[f.name]))
		}
	}
	noun := "receipts"
	if read == 1 {
		noun = "receipt"
	}
	fmt.Printf("\n%s at effort %s: %s — US$ %.4f for %d %s, US$ %.4f each\n",
		*model, *effort, strings.Join(scores, " · "), usd, read, noun, usd/float64(read))
}

func defaultCategories() ([]extract.Category, error) {
	st, err := store.Open(":memory:")
	if err != nil {
		return nil, err
	}
	defer st.Close()
	cats, err := st.Categories(context.Background(), 0)
	if err != nil {
		return nil, err
	}
	var list []extract.Category
	for _, c := range cats {
		list = append(list, extract.Category{Slug: c.Slug, Label: c.Label})
	}
	return list, nil
}

func receipts(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if _, ok := types[strings.ToLower(filepath.Ext(e.Name()))]; ok && !e.IsDir() {
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(files)
	return files, nil
}

func show(name string, e expected) string {
	v := map[string]string{"total": e.Total, "date": e.Date, "payment": e.PaymentMethod, "category": e.Category,
		"merchant": e.Merchant, "account": e.Account, "card": e.CardLast4, "cnpj": e.MerchantTaxID,
		"nfce": e.NFCeAccessKey}[name]
	return fmt.Sprintf("%q", v)
}

func letters(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func nonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
