package pluggy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	defaultBase = "https://api.pluggy.ai"
	keyLifetime = 100 * time.Minute
	maxPages    = 40
	maxBody     = 32 << 20
)

var ErrNotFound = errors.New("pluggy: not found")

var uuid = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func ValidID(id string) bool { return uuid.MatchString(id) }

type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string {
	msg := fmt.Sprintf("pluggy answered %d %s", e.Status, http.StatusText(e.Status))
	if e.Message != "" {
		msg += ": " + e.Message
	}
	return msg
}

type Client struct {
	base   string
	id     string
	secret string
	http   *http.Client
	now    func() time.Time

	mu    sync.Mutex
	key   string
	keyAt time.Time
}

func New(clientID, clientSecret string) *Client {
	return &Client{base: defaultBase, id: clientID, secret: clientSecret, http: &http.Client{Timeout: time.Minute}, now: time.Now}
}

type Item struct {
	ID               string     `json:"id"`
	Connector        Connector  `json:"connector"`
	Status           string     `json:"status"`
	ExecutionStatus  string     `json:"executionStatus"`
	Error            *ItemError `json:"error"`
	LastUpdatedAt    *time.Time `json:"lastUpdatedAt"`
	ConsentExpiresAt *time.Time `json:"consentExpiresAt"`
}

type Connector struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type ItemError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Account struct {
	ID            string `json:"id"`
	Type          string `json:"type"`
	Subtype       string `json:"subtype"`
	Name          string `json:"name"`
	MarketingName string `json:"marketingName"`
	Number        string `json:"number"`
	TaxNumber     string `json:"taxNumber"`
	CurrencyCode  string `json:"currencyCode"`
}

type Transaction struct {
	ID                      string        `json:"id"`
	AccountID               string        `json:"accountId"`
	Description             string        `json:"description"`
	CurrencyCode            string        `json:"currencyCode"`
	Amount                  json.Number   `json:"amount"`
	AmountInAccountCurrency json.Number   `json:"amountInAccountCurrency"`
	Date                    time.Time     `json:"date"`
	Type                    string        `json:"type"`
	Status                  string        `json:"status"`
	Category                string        `json:"category"`
	ProviderID              string        `json:"providerId"`
	OperationType           string        `json:"operationType"`
	PaymentData             *PaymentData  `json:"paymentData"`
	CreditCardMetadata      *CardMetadata `json:"creditCardMetadata"`
	Merchant                *Merchant     `json:"merchant"`
}

type PaymentData struct {
	Payer         *Participant `json:"payer"`
	Receiver      *Participant `json:"receiver"`
	PaymentMethod string       `json:"paymentMethod"`
}

type Participant struct {
	Name           string    `json:"name"`
	DocumentNumber *Document `json:"documentNumber"`
}

type Document struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type CardMetadata struct {
	InstallmentNumber int    `json:"installmentNumber"`
	TotalInstallments int    `json:"totalInstallments"`
	CardNumber        string `json:"cardNumber"`
}

type Merchant struct {
	Name         string `json:"name"`
	BusinessName string `json:"businessName"`
	CNPJ         string `json:"cnpj"`
}

func (c *Client) Item(ctx context.Context, id string) (Item, error) {
	var it Item
	err := c.get(ctx, "/items/"+url.PathEscape(id), nil, &it)
	return it, err
}

func (c *Client) Accounts(ctx context.Context, itemID string) ([]Account, error) {
	var page struct {
		Results []Account `json:"results"`
	}
	err := c.get(ctx, "/accounts", url.Values{"itemId": {itemID}}, &page)
	return page.Results, err
}

func (c *Client) Transactions(ctx context.Context, accountID string, from time.Time) ([]Transaction, error) {
	q := url.Values{"accountId": {accountID}, "dateFrom": {from.Format("2006-01-02")}}
	var all []Transaction
	for range maxPages {
		var page struct {
			Results []Transaction `json:"results"`
			Next    *string       `json:"next"`
		}
		if err := c.get(ctx, "/v2/transactions", q, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Results...)
		if page.Next == nil || *page.Next == "" {
			return all, nil
		}
		next, err := url.ParseQuery(strings.TrimPrefix(*page.Next, "?"))
		if err != nil || next.Get("after") == "" {
			return nil, fmt.Errorf("pluggy: a next page this client cannot follow: %q", *page.Next)
		}
		q.Set("after", next.Get("after"))
	}
	return nil, fmt.Errorf("pluggy: more than %d pages of transactions", maxPages)
}

func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	target := c.base + path
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	for retried := false; ; retried = true {
		key, err := c.apiKey(ctx)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return err
		}
		req.Header.Set("X-API-KEY", key)
		err = c.do(req, out)
		var apiErr *Error
		if !retried && errors.As(err, &apiErr) && (apiErr.Status == http.StatusUnauthorized || apiErr.Status == http.StatusForbidden) {
			c.forget(key)
			continue
		}
		return err
	}
}

func (c *Client) apiKey(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.key != "" && c.now().Sub(c.keyAt) < keyLifetime {
		return c.key, nil
	}
	body, err := json.Marshal(map[string]string{"clientId": c.id, "clientSecret": c.secret})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/auth", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	var out struct {
		APIKey string `json:"apiKey"`
	}
	if err := c.do(req, &out); err != nil {
		return "", fmt.Errorf("pluggy: signing in with the client id and secret: %w", err)
	}
	if out.APIKey == "" {
		return "", errors.New("pluggy: /auth answered no apiKey")
	}
	c.key, c.keyAt = out.APIKey, c.now()
	return c.key, nil
}

func (c *Client) forget(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.key == key {
		c.key = ""
	}
}

func (c *Client) do(req *http.Request, out any) error {
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return fmt.Errorf("pluggy: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return fmt.Errorf("pluggy: %w", err)
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case resp.StatusCode/100 != 2:
		var e struct {
			Message string `json:"message"`
		}
		json.Unmarshal(body, &e)
		return &Error{Status: resp.StatusCode, Message: e.Message}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("pluggy: %s %s answered something that is not JSON: %w", req.Method, req.URL.Path, err)
	}
	return nil
}
