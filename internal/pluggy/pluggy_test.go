package pluggy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var ctx = context.Background()

const item = "6f1b8a3e-2c4d-4e5f-8a9b-0c1d2e3f4a5b"

type fakePluggy struct {
	t      *testing.T
	auths  int
	keys   []string
	reject map[string]bool
	pages  map[string]string
}

func (f *fakePluggy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/auth" {
		var body map[string]string
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &body)
		if body["clientId"] != "id" || body["clientSecret"] != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"message":"Invalid credentials","code":401}`)
			return
		}
		f.auths++
		key := "key-" + string(rune('0'+f.auths))
		io.WriteString(w, `{"apiKey":"`+key+`"}`)
		return
	}
	key := r.Header.Get("X-API-KEY")
	f.keys = append(f.keys, key)
	if f.reject[key] {
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"message":"Expired api key","code":403}`)
		return
	}
	switch r.URL.Path {
	case "/items/" + item:
		io.WriteString(w, `{"id":"`+item+`","connector":{"id":200,"name":"MeuPluggy"},"status":"UPDATED",
			"executionStatus":"SUCCESS","error":null,"lastUpdatedAt":"2026-10-20T09:12:30.123Z","consentExpiresAt":null}`)
	case "/accounts":
		if r.URL.Query().Get("itemId") != item {
			f.t.Errorf("accounts asked for item %q", r.URL.Query().Get("itemId"))
		}
		io.WriteString(w, `{"page":1,"total":1,"totalPages":1,"results":[{"id":"acc-1","type":"CREDIT","subtype":"CREDIT_CARD",
			"name":"Nubank Mastercard","number":"1234","taxNumber":"123.456.789-09","currencyCode":"BRL"}]}`)
	case "/v2/transactions":
		q := r.URL.Query()
		if q.Get("accountId") != "acc-1" || q.Get("dateFrom") != "2026-10-01" {
			f.t.Errorf("transactions asked with %v", q)
		}
		io.WriteString(w, f.pages[q.Get("after")])
	default:
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"message":"Item not found","code":404}`)
	}
}

func client(t *testing.T, f *fakePluggy, secret string) *Client {
	t.Helper()
	f.t = t
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c := New("id", secret)
	c.base = srv.URL
	return c
}

func TestItemAndAccounts(t *testing.T) {
	f := &fakePluggy{}
	c := client(t, f, "secret")

	it, err := c.Item(ctx, item)
	if err != nil {
		t.Fatal(err)
	}
	if it.Connector.Name != "MeuPluggy" || it.Status != "UPDATED" || it.Error != nil || it.LastUpdatedAt == nil ||
		!it.LastUpdatedAt.Equal(time.Date(2026, 10, 20, 9, 12, 30, 123e6, time.UTC)) || it.ConsentExpiresAt != nil {
		t.Errorf("item: %+v", it)
	}
	accounts, err := c.Accounts(ctx, item)
	if err != nil || len(accounts) != 1 || accounts[0].Type != "CREDIT" || accounts[0].Number != "1234" {
		t.Errorf("accounts: %+v, %v", accounts, err)
	}
	if f.auths != 1 || len(f.keys) != 2 || f.keys[0] != "key-1" || f.keys[1] != "key-1" {
		t.Errorf("signed in %d times, keys sent %v", f.auths, f.keys)
	}

	if _, err := c.Item(ctx, "00000000-0000-0000-0000-000000000000"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a missing item: %v", err)
	}
}

func TestTransactionsFollowTheCursor(t *testing.T) {
	f := &fakePluggy{pages: map[string]string{
		"": `{"results":[{"id":"t1","description":"IFD*IFOOD","currencyCode":"BRL","amount":35.9,"date":"2026-10-19T15:04:00.000Z",
			"type":"DEBIT","status":"POSTED","category":null,"operationType":"PAGAMENTO",
			"creditCardMetadata":{"installmentNumber":null,"totalInstallments":null,"cardNumber":"5678"},"merchant":null}],
			"next":"?accountId=acc-1&after=abc%2B1"}`,
		"abc+1": `{"results":[{"id":"t2","description":"Pagamento recebido","currencyCode":"BRL","amount":-150,
			"amountInAccountCurrency":null,"date":"2026-10-10T00:00:00.000Z","type":"CREDIT","status":"POSTED",
			"operationType":"PAGAMENTO_FATURA","paymentData":null}],"next":null}`,
	}}
	c := client(t, f, "secret")

	txs, err := c.Transactions(ctx, "acc-1", time.Date(2026, 10, 1, 0, 0, 0, 0, time.Local))
	if err != nil {
		t.Fatal(err)
	}
	if len(txs) != 2 || txs[0].ID != "t1" || txs[1].ID != "t2" {
		t.Fatalf("got %+v", txs)
	}
	first := txs[0]
	if first.Amount.String() != "35.9" || first.Category != "" || first.CreditCardMetadata.CardNumber != "5678" ||
		!first.Date.Equal(time.Date(2026, 10, 19, 15, 4, 0, 0, time.UTC)) {
		t.Errorf("first: %+v", first)
	}
	if txs[1].Amount.String() != "-150" || txs[1].AmountInAccountCurrency != "" || txs[1].PaymentData != nil {
		t.Errorf("second: %+v", txs[1])
	}
}

func TestAnExpiredKeyIsRenewedOnce(t *testing.T) {
	f := &fakePluggy{reject: map[string]bool{"key-1": true}}
	c := client(t, f, "secret")
	if _, err := c.Item(ctx, item); err != nil {
		t.Fatal(err)
	}
	if f.auths != 2 || strings.Join(f.keys, ",") != "key-1,key-2" {
		t.Errorf("signed in %d times, keys sent %v", f.auths, f.keys)
	}

	f.reject["key-2"], f.reject["key-3"] = true, true
	var apiErr *Error
	if _, err := c.Item(ctx, item); !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden {
		t.Errorf("a key refused twice: %v", err)
	}
}

func TestKeyIsReusedUntilItAges(t *testing.T) {
	f := &fakePluggy{}
	c := client(t, f, "secret")
	clock := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return clock }

	c.Item(ctx, item)
	clock = clock.Add(90 * time.Minute)
	c.Item(ctx, item)
	clock = clock.Add(20 * time.Minute)
	c.Item(ctx, item)
	if f.auths != 2 {
		t.Errorf("signed in %d times over 110 minutes, want 2", f.auths)
	}
}

func TestWrongSecret(t *testing.T) {
	c := client(t, &fakePluggy{}, "wrong")
	_, err := c.Item(ctx, item)
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized || !strings.Contains(err.Error(), "Invalid credentials") {
		t.Errorf("got %v", err)
	}
}

func TestValidID(t *testing.T) {
	for id, want := range map[string]bool{
		item: true, strings.ToUpper(item): true, "": false, "6f1b8a3e2c4d4e5f8a9b0c1d2e3f4a5b": false, item + "x": false,
	} {
		if ValidID(id) != want {
			t.Errorf("ValidID(%q) = %v", id, !want)
		}
	}
}
