package bot

import (
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
