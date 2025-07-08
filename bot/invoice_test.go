package bot

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/example/filestoragebot/config"
)

func TestInvoiceFlow(t *testing.T) {
	crypto := os.Getenv("CRYPTOBOT_TOKEN")
	xrocket := os.Getenv("XROCKET_TOKEN")
	if crypto == "" && xrocket == "" {
		t.Skip("no provider tokens")
	}
	cfg := &config.Config{CryptoBotToken: crypto, XRocketToken: xrocket}
	b := &Bot{cfg: cfg}
	url, id, provider, err := b.createInvoice(0.01)
	if err != nil {
		t.Fatalf("createInvoice: %v", err)
	}
	if url == "" || id == "" || provider == "" {
		t.Fatalf("invalid invoice data")
	}
	paid, err := b.checkInvoice(id, provider)
	if err != nil {
		t.Fatalf("checkInvoice: %v", err)
	}
	if paid {
		t.Fatalf("invoice unexpectedly paid")
	}
}

func TestCryptoProvider(t *testing.T) {
	token := os.Getenv("CRYPTOBOT_TOKEN")
	if token == "" {
		t.Skip("no token")
	}
	b := &Bot{cfg: &config.Config{CryptoBotToken: token}}
	url, id, err := b.createCryptoInvoice(0.01)
	if err != nil {
		t.Fatalf("createCryptoInvoice: %v", err)
	}
	if url == "" || id == "" {
		t.Fatalf("invalid data")
	}
	paid, err := b.checkCryptoInvoice(id)
	if err != nil {
		t.Fatalf("checkCryptoInvoice: %v", err)
	}
	if paid {
		t.Fatalf("invoice unexpectedly paid")
	}
}

func TestXRocketProvider(t *testing.T) {
	token := os.Getenv("XROCKET_TOKEN")
	if token == "" {
		t.Skip("no token")
	}
	b := &Bot{cfg: &config.Config{XRocketToken: token}}
	url, id, err := b.createXRocketInvoice(0.01)
	if err != nil {
		t.Fatalf("createXRocketInvoice: %v", err)
	}
	if url == "" || id == "" {
		t.Fatalf("invalid data")
	}
	paid, err := b.checkXRocketInvoice(id)
	if err != nil {
		t.Fatalf("checkXRocketInvoice: %v", err)
	}
	if paid {
		t.Fatalf("invoice unexpectedly paid")
	}
}

func TestInvoiceNoProvider(t *testing.T) {
	b := &Bot{cfg: &config.Config{}}
	if _, _, _, err := b.createInvoice(0.01); err == nil {
		t.Fatalf("expected error")
	}
}

func TestCheckCryptoInvoicePaid(t *testing.T) {
	token := os.Getenv("CRYPTOBOT_TOKEN")
	if token == "" {
		t.Skip("no token")
	}
	b := &Bot{cfg: &config.Config{CryptoBotToken: token}}
	paid, err := b.checkCryptoInvoice("29143188")
	if err != nil {
		t.Fatalf("checkCryptoInvoice: %v", err)
	}
	if !paid {
		t.Fatalf("expected invoice to be paid")
	}
}

func TestCheckCryptoInvoiceUnpaid(t *testing.T) {
	token := os.Getenv("CRYPTOBOT_TOKEN")
	if token == "" {
		t.Skip("no token")
	}
	b := &Bot{cfg: &config.Config{CryptoBotToken: token}}
	paid, err := b.checkCryptoInvoice("29104962")
	if err != nil {
		t.Fatalf("checkCryptoInvoice: %v", err)
	}
	if paid {
		t.Fatalf("expected invoice to be unpaid")
	}
}

func TestXRocketDecodeSuccess(t *testing.T) {
	data := []byte(`{"success":true,"data":{"id":"42","link":"https://t.me/xrocket?start=inv_test","status":"active"}}`)
	var res struct {
		Ok      bool `json:"ok"`
		Success bool `json:"success"`
		Result  struct {
			ID  string `json:"id"`
			URL string `json:"url"`
		} `json:"result"`
		Data struct {
			ID     string `json:"id"`
			Link   string `json:"link"`
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !res.Success || res.Data.ID != "42" || res.Data.Link == "" {
		t.Fatalf("unexpected decode result: %+v", res)
	}
}

func TestCheckCryptoInvoiceError(t *testing.T) {
	var method string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		http.Error(w, "fail", http.StatusInternalServerError)
	}))
	defer ts.Close()
	old := cryptoAPIBase
	cryptoAPIBase = ts.URL + "/"
	defer func() { cryptoAPIBase = old }()
	b := &Bot{cfg: &config.Config{CryptoBotToken: "t"}}
	if _, err := b.checkCryptoInvoice("1"); err == nil {
		t.Fatalf("expected error")
	}
	if method != http.MethodPost {
		t.Fatalf("expected POST request, got %s", method)
	}
}
