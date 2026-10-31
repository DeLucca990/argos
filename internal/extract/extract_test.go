package extract

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/DeLucca990/argos/internal/receipt"
)

var (
	ctx        = context.Background()
	today      = time.Date(2026, 10, 20, 15, 0, 0, 0, time.UTC)
	categories = []Category{{"mercado", "Mercado", ""}, {"farmacia", "Farmácia", ""}, {"pet-shop", "Pet Shop", "ração, banho e tosa"}, {"outros", "Outros", ""}}
)

const answer = `{"is_receipt":true,"not_receipt_reason":"","merchant":"Drogaria Boa Saúde","merchant_tax_id":"11222333000181",
"date":"2026-10-19","time":"10:15","total":"42.30","currency":"BRL","payment_method":"debito","category":"farmacia",
"description":"Remédios","nfce_access_key":"","items":[],"notes":""}`

type request struct {
	header http.Header
	body   map[string]any
}

func fake(t *testing.T, response string) (*httptest.Server, *[]request) {
	t.Helper()
	var seen []request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("request body: %v", err)
		}
		seen = append(seen, request{header: r.Header.Clone(), body: body})
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, response)
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func message(model, stop, text string) string {
	content, _ := json.Marshal(text)
	return `{"id":"msg_1","type":"message","role":"assistant","model":"` + model + `",
"content":[{"type":"thinking","thinking":"","signature":"sig"},{"type":"text","text":` + string(content) + `}],
"stop_reason":"` + stop + `","stop_sequence":null,"stop_details":{"type":"refusal","category":"cyber","explanation":""},
"usage":{"input_tokens":5000,"output_tokens":1000,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}`
}

func extractor(t *testing.T, srv *httptest.Server, model string) *Extractor {
	t.Helper()
	return New(model, "low", option.WithBaseURL(srv.URL), option.WithAPIKey("test"), option.WithMaxRetries(0))
}

func smallJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			c := color.RGBA{255, 0, 0, 255}
			if x >= w/2 {
				c = color.RGBA{0, 0, 255, 255}
			}
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestExtractImage(t *testing.T) {
	srv, seen := fake(t, message("claude-opus-5-5", "end_turn", answer))
	photo := smallJPEG(t, 100, 50)

	res, err := extractor(t, srv, "claude-opus-5-5").Extract(ctx, Input{MIME: "image/jpeg", Data: photo, Text: "remédio da vó", Today: today, Categories: categories})
	if err != nil {
		t.Fatal(err)
	}
	if res.Raw.Merchant != "Drogaria Boa Saúde" || res.Raw.Category != "farmacia" || res.Raw.Total != "42.30" {
		t.Errorf("raw: %+v", res.Raw)
	}
	if res.Model != "claude-opus-5-5" || res.Spend.Input != 5000 || res.Spend.Output != 1000 ||
		res.Spend.USD < 0.0399 || res.Spend.USD > 0.0401 {
		t.Errorf("spend: %+v", res.Spend)
	}

	req := (*seen)[0]
	if req.body["model"] != "claude-opus-5-5" || req.body["fallbacks"] != "default" ||
		!strings.Contains(req.header.Get("Anthropic-Beta"), "server-side-fallback-2026-07-01") {
		t.Errorf("model, fallbacks, beta: %v %v %q", req.body["model"], req.body["fallbacks"], req.header.Get("Anthropic-Beta"))
	}
	out := req.body["output_config"].(map[string]any)
	format := out["format"].(map[string]any)
	category := format["schema"].(map[string]any)["properties"].(map[string]any)["category"].(map[string]any)
	if out["effort"] != "low" || format["type"] != "json_schema" || len(category["enum"].([]any)) != 4 ||
		!strings.Contains(category["description"].(string), "pet-shop = Pet Shop (ração, banho e tosa); outros = Outros") {
		t.Errorf("output_config: %v", out)
	}
	if _, set := req.body["thinking"]; set {
		t.Error("thinking was sent; it is a 400 on Opus 5.5 when disabled, and effort is the control")
	}

	content := req.body["messages"].([]any)[0].(map[string]any)["content"].([]any)
	img := content[0].(map[string]any)["source"].(map[string]any)
	if img["media_type"] != "image/jpeg" || img["data"] != base64.StdEncoding.EncodeToString(photo) {
		t.Error("a small upright JPEG was not sent as it came")
	}
	text := content[1].(map[string]any)["text"].(string)
	if !strings.Contains(text, "2026-10-20") || !strings.Contains(text, "remédio da vó") {
		t.Errorf("text block: %q", text)
	}
}

func TestExtractTextWithoutFallbacks(t *testing.T) {
	srv, seen := fake(t, message("claude-haiku-5-5", "end_turn", answer))
	if _, err := extractor(t, srv, "claude-haiku-5-5").Extract(ctx, Input{Text: "almoço 35,90 no pix", Today: today, Categories: categories}); err != nil {
		t.Fatal(err)
	}
	req := (*seen)[0]
	if _, set := req.body["fallbacks"]; set || req.header.Get("Anthropic-Beta") != "" {
		t.Error("Haiku 5.5 has no server-side fallback, and was sent one")
	}
	content := req.body["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if len(content) != 1 || !strings.Contains(content[0].(map[string]any)["text"].(string), "typed this purchase:\n\nalmoço 35,90 no pix") {
		t.Errorf("content: %v", content)
	}
}

func TestExtractFailuresKeepTheirCost(t *testing.T) {
	for name, response := range map[string]string{
		"refusal":    message("claude-opus-5-5", "refusal", ""),
		"max tokens": message("claude-opus-5-5", "max_tokens", `{"is_rec`),
		"not json":   message("claude-opus-5-5", "end_turn", "Desculpe, não consigo."),
	} {
		srv, _ := fake(t, response)
		res, err := extractor(t, srv, "claude-opus-5-5").Extract(ctx, Input{Text: "x 1", Today: today, Categories: categories})
		if err == nil || res.Spend.USD == 0 {
			t.Errorf("%s: err %v, spend %+v", name, err, res.Spend)
		}
	}
}

func TestExtractRefusesUnsupportedFiles(t *testing.T) {
	srv, seen := fake(t, message("claude-opus-5-5", "end_turn", answer))
	_, err := extractor(t, srv, "claude-opus-5-5").Extract(ctx, Input{MIME: "image/heic", Data: []byte("x"), Today: today})
	if !errors.Is(err, ErrUnsupported) || len(*seen) != 0 {
		t.Errorf("heic: %v, %d requests", err, len(*seen))
	}
	if _, err := extractor(t, srv, "claude-opus-5-5").Extract(ctx, Input{Text: "x 1", Today: today}); err == nil || len(*seen) != 0 {
		t.Errorf("no categories: %v, %d requests", err, len(*seen))
	}
}

func TestPrepareImageScalesLargeImages(t *testing.T) {
	var buf bytes.Buffer
	png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 4000, 1000)))

	out, mime, err := prepareImage(buf.Bytes(), "image/png")
	if err != nil {
		t.Fatal(err)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(out))
	if err != nil || mime != "image/jpeg" || cfg.Width != 2576 || cfg.Height != 644 {
		t.Errorf("scaled to %dx%d %s, %v", cfg.Width, cfg.Height, mime, err)
	}
}

func TestPrepareImageAppliesEXIFOrientation(t *testing.T) {
	plain := smallJPEG(t, 40, 20)
	exif := []byte("Exif\x00\x00II\x2a\x00\x08\x00\x00\x00\x01\x00\x12\x01\x03\x00\x01\x00\x00\x00\x06\x00\x00\x00\x00\x00\x00\x00")
	app1 := append([]byte{0xFF, 0xE1, 0, byte(len(exif) + 2)}, exif...)
	rotated := append(append(append([]byte{}, plain[:2]...), app1...), plain[2:]...)

	if got := jpegOrientation(rotated); got != 6 {
		t.Fatalf("orientation %d, want 6", got)
	}
	out, _, err := prepareImage(rotated, "image/jpeg")
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 20 || b.Dy() != 40 {
		t.Fatalf("rotated to %dx%d, want 20x40", b.Dx(), b.Dy())
	}
	top, bottom := img.At(10, 5).(color.YCbCr), img.At(10, 35).(color.YCbCr)
	if r, _, b, _ := color.YCbCrModel.Convert(top).RGBA(); r < b {
		t.Errorf("the left half of the photo should be on top after a quarter turn clockwise")
	}
	if r, _, b, _ := color.YCbCrModel.Convert(bottom).RGBA(); b < r {
		t.Errorf("the right half of the photo should be at the bottom")
	}

	if out, _, _ := prepareImage(plain, "image/jpeg"); !bytes.Equal(out, plain) {
		t.Error("an upright small JPEG was re-encoded")
	}
}

func TestCorrect(t *testing.T) {
	srv, seen := fake(t, message("claude-opus-5-5", "end_turn", answer))
	res, err := extractor(t, srv, "claude-opus-5-5").Correct(ctx, Correction{
		Current: decodeRaw(t, answer), Text: "  foi 45,90 no crédito  ", Today: today, Categories: categories,
	})
	if err != nil || res.Raw.Merchant != "Drogaria Boa Saúde" || res.Spend.USD == 0 {
		t.Fatalf("%+v, %v", res, err)
	}

	req := (*seen)[0]
	system := req.body["system"].([]any)[0].(map[string]any)["text"].(string)
	content := req.body["messages"].([]any)[0].(map[string]any)["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)
	if !strings.Contains(system, "correction applied and everything else exactly as it was") || len(content) != 1 ||
		!strings.Contains(text, `"merchant":"Drogaria Boa Saúde"`) || !strings.HasSuffix(text, "foi 45,90 no crédito") ||
		!strings.Contains(text, "2026-10-20") {
		t.Errorf("system %q\ntext %q", system, text)
	}
	if req.body["output_config"].(map[string]any)["format"] == nil {
		t.Error("a correction must follow the same schema")
	}
}

func decodeRaw(t *testing.T, s string) (r receipt.Raw) {
	t.Helper()
	if err := json.Unmarshal([]byte(s), &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestLessonsGoWithTheReceipt(t *testing.T) {
	srv, seen := fake(t, message("claude-opus-5-5", "end_turn", answer))
	lessons := []Lesson{
		{When: `receipts that look like "tela de Pix, títulos em roxo"`, Field: "payment_account", Value: "Nubank"},
		{When: `payments to "MARIA S SOUZA"`, Field: "merchant", Value: "Personal trainer"},
	}
	if _, err := extractor(t, srv, "claude-opus-5-5").Extract(ctx, Input{MIME: "image/jpeg", Data: smallJPEG(t, 10, 10),
		Today: today, Categories: categories, Lessons: lessons}); err != nil {
		t.Fatal(err)
	}
	req := (*seen)[0]
	content := req.body["messages"].([]any)[0].(map[string]any)["content"].([]any)
	text := content[1].(map[string]any)["text"].(string)
	for _, want := range []string{"newer wins", "List in remembered",
		"\n- receipts that look like \"tela de Pix, títulos em roxo\": payment_account = \"Nubank\"",
		"\n- payments to \"MARIA S SOUZA\": merchant = \"Personal trainer\""} {
		if !strings.Contains(text, want) {
			t.Errorf("the text lacks %q:\n%s", want, text)
		}
	}

	props := req.body["output_config"].(map[string]any)["format"].(map[string]any)["schema"].(map[string]any)["properties"].(map[string]any)
	remembered := props["remembered"].(map[string]any)["items"].(map[string]any)["enum"].([]any)
	if props["layout"] == nil || len(remembered) != 5 {
		t.Errorf("layout %v, remembered %v", props["layout"], remembered)
	}

	srv, seen = fake(t, message("claude-opus-5-5", "end_turn", answer))
	extractor(t, srv, "claude-opus-5-5").Extract(ctx, Input{Text: "x 1", Today: today, Categories: categories})
	content = (*seen)[0].body["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if text := content[0].(map[string]any)["text"].(string); strings.Contains(text, "taught you") {
		t.Errorf("no lessons, no section:\n%s", text)
	}
}
