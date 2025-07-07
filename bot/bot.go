package bot

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/example/filestoragebot/config"
	"github.com/example/filestoragebot/db"
	"github.com/example/filestoragebot/models"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type Bot struct {
	api *tgbotapi.BotAPI
	cfg *config.Config
	db  *db.DB

	pendingUploads  map[int64]*uploadState
	changeLink      map[int64]string
	pendingInvoices map[string]*invoiceState
}

type uploadState struct {
	fileID   string
	fileName string
	fileSize int64
	step     int
	storage  string
	local    string
	link     string
	notify   bool
	cost     float64
}

type invoiceState struct {
	userID   int64
	amount   float64
	provider string
}

// Notify sends a message to user by internal DB id.
func (b *Bot) Notify(userID int64, message string) error {
	tgID, err := b.db.GetTelegramID(userID)
	if err != nil {
		return err
	}
	msg := tgbotapi.NewMessage(tgID, message)
	_, err = b.api.Send(msg)
	return err
}

func New(cfg *config.Config, db *db.DB) (*Bot, error) {
	api, err := tgbotapi.NewBotAPI(cfg.TelegramToken)
	if err != nil {
		return nil, err
	}
	return &Bot{
		api:             api,
		cfg:             cfg,
		db:              db,
		pendingUploads:  make(map[int64]*uploadState),
		changeLink:      make(map[int64]string),
		pendingInvoices: make(map[string]*invoiceState),
	}, nil
}

func (b *Bot) Start() {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := b.api.GetUpdatesChan(u)

	for update := range updates {
		if update.Message != nil {
			b.handleMessage(update.Message)
		}
		if update.CallbackQuery != nil {
			b.handleCallback(update.CallbackQuery)
		}
	}
}

func (b *Bot) handleMessage(m *tgbotapi.Message) {
	userID, err := b.db.GetOrCreateUser(m.From.ID)
	if err != nil {
		log.Println("db:", err)
		return
	}
	if m.IsCommand() {
		b.handleCommand(userID, m)
		return
	}

	if st, ok := b.pendingUploads[userID]; ok && m.Document == nil {
		b.handleUploadStep(userID, st, m)
		return
	}

	if linkName, ok := b.changeLink[userID]; ok && m.Document == nil {
		b.finishChangeLink(userID, linkName, m)
		return
	}

	if m.Document != nil {
		b.handleDocument(userID, m)
		return
	}
}

func (b *Bot) handleCommand(userID int64, m *tgbotapi.Message) {
	switch m.Command() {
	case "start":
		msg := tgbotapi.NewMessage(m.Chat.ID, "Welcome to FileStorageBot")
		b.api.Send(msg)
	case "balance":
		bal, err := b.db.GetBalance(userID)
		if err != nil {
			log.Println(err)
			return
		}
		msg := tgbotapi.NewMessage(m.Chat.ID, fmt.Sprintf("Ваш баланс: %.2f", bal))
		b.api.Send(msg)
	case "topup":
		amountStr := strings.TrimSpace(m.CommandArguments())
		if amountStr == "" {
			b.api.Send(tgbotapi.NewMessage(m.Chat.ID, "Укажите сумму"))
			return
		}
		var amount float64
		fmt.Sscanf(amountStr, "%f", &amount)
		if amount <= 0 {
			b.api.Send(tgbotapi.NewMessage(m.Chat.ID, "Неверная сумма"))
			return
		}
		url, id, provider, err := b.createInvoice(amount)
		if err != nil {
			b.api.Send(tgbotapi.NewMessage(m.Chat.ID, "Ошибка создания счёта"))
			return
		}
		b.api.Send(tgbotapi.NewMessage(m.Chat.ID, "Оплатите: "+url))
		// store invoice
		b.pendingInvoices[id] = &invoiceState{userID: userID, amount: amount, provider: provider}
	case "check":
		invID := strings.TrimSpace(m.CommandArguments())
		state, ok := b.pendingInvoices[invID]
		if !ok || state.userID != userID {
			b.api.Send(tgbotapi.NewMessage(m.Chat.ID, "Не найдено"))
			return
		}
		paid, err := b.checkInvoice(invID, state.provider)
		if err != nil {
			b.api.Send(tgbotapi.NewMessage(m.Chat.ID, "Ошибка проверки"))
			return
		}
		if paid {
			b.db.AdjustBalance(userID, state.amount)
			b.db.AddPayment(userID, state.amount)
			delete(b.pendingInvoices, invID)
			b.api.Send(tgbotapi.NewMessage(m.Chat.ID, "Оплата получена"))
		} else {
			b.api.Send(tgbotapi.NewMessage(m.Chat.ID, "Не оплачено"))
		}
	case "files":
		files, err := b.db.ListFiles(userID)
		if err != nil {
			log.Println(err)
			return
		}
		if len(files) == 0 {
			b.api.Send(tgbotapi.NewMessage(m.Chat.ID, "Файлов нет"))
			return
		}
		var rows [][]tgbotapi.InlineKeyboardButton
		for _, f := range files {
			btn := tgbotapi.NewInlineKeyboardButtonData(f.LocalName, "manage:"+f.StorageName)
			rows = append(rows, tgbotapi.NewInlineKeyboardRow(btn))
		}
		kb := tgbotapi.NewInlineKeyboardMarkup(rows...)
		msg := tgbotapi.NewMessage(m.Chat.ID, "Ваши файлы")
		msg.ReplyMarkup = &kb
		b.api.Send(msg)
	case "delete":
		if len(m.CommandArguments()) == 0 {
			b.api.Send(tgbotapi.NewMessage(m.Chat.ID, "Укажите имя файла"))
			return
		}
		name := m.CommandArguments()
		f, err := b.db.GetFileByStorageName(name)
		if err != nil {
			b.api.Send(tgbotapi.NewMessage(m.Chat.ID, "Файл не найден"))
			return
		}
		if f.UserID != userID {
			b.api.Send(tgbotapi.NewMessage(m.Chat.ID, "Нет доступа"))
			return
		}
		os.Remove(filepath.Join(b.cfg.FileStoragePath, f.StorageName))
		b.db.DeleteFile(f.ID)
		b.db.AdjustBalance(userID, b.cfg.PriceRefund)
		b.api.Send(tgbotapi.NewMessage(m.Chat.ID, "Файл удалён"))
	}
}

func (b *Bot) handleDocument(userID int64, m *tgbotapi.Message) {
	if _, ok := b.pendingUploads[userID]; ok {
		b.api.Send(tgbotapi.NewMessage(m.Chat.ID, "Завершите предыдущую загрузку"))
		return
	}

	cost := b.cfg.PriceUpload
	if m.Document.FileSize > int(b.cfg.MaxFileSize) {
		extra := (int64(m.Document.FileSize) - b.cfg.MaxFileSize + 50*1024*1024 - 1) / (50 * 1024 * 1024)
		cost += float64(extra)
	}

	bal, err := b.db.GetBalance(userID)
	if err != nil {
		log.Println(err)
		return
	}
	if bal < cost {
		b.api.Send(tgbotapi.NewMessage(m.Chat.ID, "Недостаточно средств"))
		return
	}

	rand.Seed(time.Now().UnixNano())
	storageName := fmt.Sprintf("%d_%d", userID, rand.Int63())

	b.pendingUploads[userID] = &uploadState{
		fileID:   m.Document.FileID,
		fileName: m.Document.FileName,
		fileSize: int64(m.Document.FileSize),
		step:     1,
		storage:  storageName,
		cost:     cost,
	}

	msg := tgbotapi.NewMessage(m.Chat.ID, "Введите локальное название файла")
	msg.ReplyMarkup = tgbotapi.ForceReply{ForceReply: true}
	b.api.Send(msg)
}

func (b *Bot) handleUploadStep(userID int64, st *uploadState, m *tgbotapi.Message) {
	switch st.step {
	case 1:
		st.local = m.Text
		st.step = 2
		msg := tgbotapi.NewMessage(m.Chat.ID, "Введите часть ссылки")
		msg.ReplyMarkup = tgbotapi.ForceReply{ForceReply: true}
		b.api.Send(msg)
	case 2:
		st.link = strings.TrimSpace(m.Text)
		st.step = 3
		kb := tgbotapi.NewReplyKeyboard(
			tgbotapi.NewKeyboardButtonRow(
				tgbotapi.NewKeyboardButton("Да"),
				tgbotapi.NewKeyboardButton("Нет"),
			))
		msg := tgbotapi.NewMessage(m.Chat.ID, "Включить уведомления о скачиваниях? (Да/Нет)")
		msg.ReplyMarkup = kb
		b.api.Send(msg)
	case 3:
		txt := strings.ToLower(m.Text)
		st.notify = txt == "да"
		if err := b.finalizeUpload(userID, st, m.Chat.ID); err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				st.step = 2
				msg := tgbotapi.NewMessage(m.Chat.ID, "Ссылка уже занята, введите другую")
				msg.ReplyMarkup = tgbotapi.ForceReply{ForceReply: true}
				b.api.Send(msg)
				return
			}
			log.Println(err)
			delete(b.pendingUploads, userID)
			b.api.Send(tgbotapi.NewMessage(m.Chat.ID, "Ошибка сохранения"))
			return
		}
		delete(b.pendingUploads, userID)
		msg := tgbotapi.NewMessage(m.Chat.ID, "Готово")
		msg.ReplyMarkup = tgbotapi.NewRemoveKeyboard(true)
		b.api.Send(msg)
	}
}

func (b *Bot) finalizeUpload(userID int64, st *uploadState, chatID int64) error {
	url, err := b.api.GetFileDirectURL(st.fileID)
	if err != nil {
		log.Println(err)
		return err
	}
	resp, err := http.Get(url)
	if err != nil {
		log.Println(err)
		return err
	}
	defer resp.Body.Close()

	os.MkdirAll(b.cfg.FileStoragePath, 0755)
	path := filepath.Join(b.cfg.FileStoragePath, st.storage)
	out, err := os.Create(path)
	if err != nil {
		log.Println(err)
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, resp.Body); err != nil {
		log.Println(err)
		return err
	}

	link := strings.TrimRight(b.cfg.Domain, "/") + "/" + st.link
	f := &models.File{
		UserID:      userID,
		LocalName:   st.local,
		StorageName: st.storage,
		Link:        link,
		Notify:      st.notify,
		Size:        st.fileSize,
	}
	if err := b.db.AddFile(f); err != nil {
		log.Println(err)
		return err
	}

	if err := b.db.AdjustBalance(userID, -st.cost); err != nil {
		log.Println(err)
	}

	b.api.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("Файл сохранён: %s", link)))
	return nil
}

func (b *Bot) createInvoice(amount float64) (string, string, string, error) {
	if b.cfg.CryptoBotToken != "" {
		url, id, err := b.createCryptoInvoice(amount)
		return url, id, "crypto", err
	}
	if b.cfg.XRocketToken != "" {
		url, id, err := b.createXRocketInvoice(amount)
		return url, id, "xrocket", err
	}
	return "", "", "", fmt.Errorf("нет провайдера оплаты")
}

func (b *Bot) createCryptoInvoice(amount float64) (string, string, error) {
	values := url.Values{}
	values.Set("asset", "USDT")
	values.Set("amount", fmt.Sprintf("%.2f", amount))
	req, err := http.NewRequest("POST", "https://pay.crypt.bot/api/createInvoice", strings.NewReader(values.Encode()))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Crypto-Pay-Token", b.cfg.CryptoBotToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	type res struct {
		Ok     bool `json:"ok"`
		Result struct {
			InvoiceID string `json:"invoice_id"`
			PayURL    string `json:"pay_url"`
		} `json:"result"`
	}
	var r res
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", "", err
	}
	if !r.Ok {
		return "", "", fmt.Errorf("api error")
	}
	return r.Result.PayURL, r.Result.InvoiceID, nil
}

func (b *Bot) createXRocketInvoice(amount float64) (string, string, error) {
	values := url.Values{}
	values.Set("currency", "USDT")
	values.Set("amount", fmt.Sprintf("%.2f", amount))
	req, err := http.NewRequest("POST", "https://api.xrocket.app/v1/createInvoice", strings.NewReader(values.Encode()))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("XRocket-Token", b.cfg.XRocketToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	type res struct {
		Ok     bool `json:"ok"`
		Result struct {
			ID  string `json:"id"`
			URL string `json:"url"`
		} `json:"result"`
	}
	var r res
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", "", err
	}
	if !r.Ok {
		return "", "", fmt.Errorf("api error")
	}
	return r.Result.URL, r.Result.ID, nil
}

func (b *Bot) checkInvoice(id, provider string) (bool, error) {
	if provider == "crypto" && b.cfg.CryptoBotToken != "" {
		return b.checkCryptoInvoice(id)
	}
	if provider == "xrocket" && b.cfg.XRocketToken != "" {
		return b.checkXRocketInvoice(id)
	}
	return false, fmt.Errorf("нет провайдера")
}

func (b *Bot) checkCryptoInvoice(id string) (bool, error) {
	urlStr := "https://pay.crypt.bot/api/getInvoices?invoice_ids=" + id
	req, err := http.NewRequest("GET", urlStr, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Crypto-Pay-Token", b.cfg.CryptoBotToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	type res struct {
		Ok     bool `json:"ok"`
		Result []struct {
			Status string `json:"status"`
		} `json:"result"`
	}
	var r res
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return false, err
	}
	if !r.Ok || len(r.Result) == 0 {
		return false, fmt.Errorf("api error")
	}
	return r.Result[0].Status == "paid", nil
}

func (b *Bot) checkXRocketInvoice(id string) (bool, error) {
	urlStr := "https://api.xrocket.app/v1/invoiceStatus?id=" + id
	req, err := http.NewRequest("GET", urlStr, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("XRocket-Token", b.cfg.XRocketToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	type res struct {
		Ok     bool `json:"ok"`
		Result struct {
			Status string `json:"status"`
		} `json:"result"`
	}
	var r res
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return false, err
	}
	if !r.Ok {
		return false, fmt.Errorf("api error")
	}
	return r.Result.Status == "paid", nil
}

func (b *Bot) handleCallback(q *tgbotapi.CallbackQuery) {
	parts := strings.SplitN(q.Data, ":", 2)
	if len(parts) != 2 {
		return
	}
	action, arg := parts[0], parts[1]
	userID, err := b.db.GetOrCreateUser(q.From.ID)
	if err != nil {
		log.Println(err)
		return
	}
	switch action {
	case "manage":
		f, err := b.db.GetFileByStorageName(arg)
		if err != nil || f.UserID != userID {
			return
		}
		kb := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("Сменить ссылку", "link:"+arg),
				tgbotapi.NewInlineKeyboardButtonData("Уведомления", "notify:"+arg),
				tgbotapi.NewInlineKeyboardButtonData("Удалить", "delete:"+arg),
			),
		)
		msg := tgbotapi.NewMessage(q.Message.Chat.ID, f.LocalName+" -> "+f.Link)
		msg.ReplyMarkup = kb
		b.api.Send(msg)
	case "notify":
		f, err := b.db.GetFileByStorageName(arg)
		if err == nil && f.UserID == userID {
			f.Notify = !f.Notify
			val := 0
			if f.Notify {
				val = 1
			}
			_, err := b.db.Exec("UPDATE files SET notify=? WHERE id=?", val, f.ID)
			if err == nil {
				b.api.Send(tgbotapi.NewCallback(q.ID, "Готово"))
			}
		}
	case "delete":
		f, err := b.db.GetFileByStorageName(arg)
		if err == nil && f.UserID == userID {
			os.Remove(filepath.Join(b.cfg.FileStoragePath, f.StorageName))
			b.db.DeleteFile(f.ID)
			b.db.AdjustBalance(userID, b.cfg.PriceRefund)
			b.api.Send(tgbotapi.NewCallback(q.ID, "Удалено"))
		}
	case "link":
		b.changeLink[userID] = arg
		msg := tgbotapi.NewMessage(q.Message.Chat.ID, "Введите новую ссылку")
		msg.ReplyMarkup = tgbotapi.ForceReply{ForceReply: true}
		b.api.Send(msg)
	}
}

func (b *Bot) finishChangeLink(userID int64, name string, m *tgbotapi.Message) {
	newLink := strings.TrimSpace(m.Text)
	f, err := b.db.GetFileByStorageName(name)
	if err != nil || f.UserID != userID {
		delete(b.changeLink, userID)
		return
	}
	link := strings.TrimRight(b.cfg.Domain, "/") + "/" + newLink
	_, err = b.db.Exec("UPDATE files SET link=? WHERE id=?", link, f.ID)
	if err == nil {
		b.api.Send(tgbotapi.NewMessage(m.Chat.ID, "Ссылка изменена: "+link))
	}
	delete(b.changeLink, userID)
}
