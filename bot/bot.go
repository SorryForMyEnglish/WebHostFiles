package bot

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/example/filestoragebot/config"
	"github.com/example/filestoragebot/db"
	"github.com/example/filestoragebot/logdb"
	"github.com/example/filestoragebot/models"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type Bot struct {
	api  *tgbotapi.BotAPI
	cfg  *config.Config
	db   *db.DB
	logs *logdb.DB

	pendingUploads  map[int64]*uploadState
	changeLink      map[int64]string
	pendingInvoices map[string]*invoiceState
	pendingTopup    map[int64]*topupState
	adminAction     map[int64]string
	filePage        map[int64]int
	lastMessage     map[int64]int
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

type topupState struct {
	step     int
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

func New(cfg *config.Config, db *db.DB, logs *logdb.DB) (*Bot, error) {
	api, err := tgbotapi.NewBotAPI(cfg.TelegramToken)
	if err != nil {
		return nil, err
	}
	b := &Bot{
		api:             api,
		cfg:             cfg,
		db:              db,
		logs:            logs,
		pendingUploads:  make(map[int64]*uploadState),
		changeLink:      make(map[int64]string),
		pendingInvoices: make(map[string]*invoiceState),
		pendingTopup:    make(map[int64]*topupState),
		adminAction:     make(map[int64]string),
		filePage:        make(map[int64]int),
		lastMessage:     make(map[int64]int),
	}
	b.checkTokens()
	return b, nil
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

func (b *Bot) deleteMessage(chatID int64, id int) {
	_, _ = b.api.Request(tgbotapi.NewDeleteMessage(chatID, id))
}

func (b *Bot) deleteLast(userID int64, chatID int64) {
	if mid, ok := b.lastMessage[userID]; ok {
		b.deleteMessage(chatID, mid)
		delete(b.lastMessage, userID)
	}
}

func (b *Bot) sendTemp(chatID, userID int64, msg tgbotapi.MessageConfig) {
	b.deleteLast(userID, chatID)
	m, err := b.api.Send(msg)
	if err == nil {
		b.lastMessage[userID] = m.MessageID
	}
}

func (b *Bot) handleMessage(m *tgbotapi.Message) {
	userID, err := b.db.GetOrCreateUser(m.From.ID)
	if err != nil {
		log.Println("db:", err)
		return
	}
	if st, ok := b.pendingTopup[userID]; ok && !m.IsCommand() && m.Document == nil {
		b.processTopup(userID, m, st)
		return
	}
	if act, ok := b.adminAction[userID]; ok && !m.IsCommand() && m.Document == nil {
		b.handleAdminInput(userID, act, m)
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

	switch m.Text {
	case "\xE2\x9E\x95 Добавить файл":
		b.deleteLast(userID, m.Chat.ID)
		b.deleteMessage(m.Chat.ID, m.MessageID)
		msg := tgbotapi.NewMessage(m.Chat.ID,
			fmt.Sprintf("\xF0\x9F\x93\x84 Отправьте файл. Стоимость загрузки от %.2f USDT", b.cfg.PriceUpload))
		b.sendTemp(m.Chat.ID, userID, msg)
		return
	case "\xF0\x9F\x93\x82 Мои файлы":
		b.deleteLast(userID, m.Chat.ID)
		b.deleteMessage(m.Chat.ID, m.MessageID)
		b.filePage[userID] = 0
		b.sendFileList(userID, m.Chat.ID, 0)
		return
	case "\xF0\x9F\x92\xB0 Пополнить счёт":
		b.deleteLast(userID, m.Chat.ID)
		b.deleteMessage(m.Chat.ID, m.MessageID)
		b.pendingTopup[userID] = &topupState{step: 1}
		msg := tgbotapi.NewMessage(m.Chat.ID, "\xF0\x9F\x92\xB0 Введите сумму")
		msg.ReplyMarkup = tgbotapi.ForceReply{ForceReply: true}
		b.sendTemp(m.Chat.ID, userID, msg)
		return
	case "\xE2\x9A\x99\xEF\xB8\x8F Админ панель":
		if m.From.ID == b.cfg.AdminID {
			b.deleteLast(userID, m.Chat.ID)
			b.deleteMessage(m.Chat.ID, m.MessageID)
			kb := tgbotapi.NewReplyKeyboard(
				tgbotapi.NewKeyboardButtonRow(tgbotapi.NewKeyboardButton("\xE2\x84\xB9\xEF\xB8\x8F Инфо о пользователе")),
				tgbotapi.NewKeyboardButtonRow(
					tgbotapi.NewKeyboardButton("\xE2\x9E\x95 Добавить баланс"),
					tgbotapi.NewKeyboardButton("\xE2\x9C\x8F\xEF\xB8\x8F Установить баланс"),
				),
				tgbotapi.NewKeyboardButtonRow(tgbotapi.NewKeyboardButton("\xF0\x9F\x93\x82 Список файлов")),
				tgbotapi.NewKeyboardButtonRow(tgbotapi.NewKeyboardButton("↩️ Назад")),
			)
			msg := tgbotapi.NewMessage(m.Chat.ID, "Админ панель")
			msg.ReplyMarkup = kb
			b.sendTemp(m.Chat.ID, userID, msg)
		}
		return
	case "↩️ Назад":
		b.deleteLast(userID, m.Chat.ID)
		b.deleteMessage(m.Chat.ID, m.MessageID)
		b.sendMainMenu(m.Chat.ID, userID, m.From.ID == b.cfg.AdminID)
		return
	case "⬅️":
		if p, ok := b.filePage[userID]; ok {
			if p > 0 {
				p--
			}
			b.filePage[userID] = p
			b.deleteMessage(m.Chat.ID, m.MessageID)
			b.sendFileList(userID, m.Chat.ID, p)
		}
		return
	case "➡️":
		if p, ok := b.filePage[userID]; ok {
			p++
			b.filePage[userID] = p
			b.deleteMessage(m.Chat.ID, m.MessageID)
			b.sendFileList(userID, m.Chat.ID, p)
		}
		return
	}

	if f, err := b.db.GetFileByLocalName(userID, m.Text); err == nil {
		notif := "🔔❌"
		if f.Notify {
			notif = "🔔✅"
		}
		kb := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("🔗", "link:"+f.StorageName),
				tgbotapi.NewInlineKeyboardButtonData(notif, "notify:"+f.StorageName),
				tgbotapi.NewInlineKeyboardButtonData("📄", "log:"+f.StorageName),
				tgbotapi.NewInlineKeyboardButtonData("❌", "delete:"+f.StorageName),
			),
		)
		b.deleteLast(userID, m.Chat.ID)
		b.deleteMessage(m.Chat.ID, m.MessageID)
		msg := tgbotapi.NewMessage(m.Chat.ID, f.LocalName+" -> "+f.Link)
		msg.ReplyMarkup = kb
		b.sendTemp(m.Chat.ID, userID, msg)
		return
	}
}

func (b *Bot) handleCommand(userID int64, m *tgbotapi.Message) {
	switch m.Command() {
	case "start":
		b.sendMainMenu(m.Chat.ID, userID, m.From.ID == b.cfg.AdminID)
		b.deleteMessage(m.Chat.ID, m.MessageID)
	case "help":
		b.sendMainMenu(m.Chat.ID, userID, m.From.ID == b.cfg.AdminID)
		b.deleteMessage(m.Chat.ID, m.MessageID)
	}
}

func (b *Bot) handleDocument(userID int64, m *tgbotapi.Message) {
	if _, ok := b.pendingUploads[userID]; ok {
		b.deleteMessage(m.Chat.ID, m.MessageID)
		msg := tgbotapi.NewMessage(m.Chat.ID, "Завершите предыдущую загрузку")
		b.sendTemp(m.Chat.ID, userID, msg)
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
		b.api.Send(tgbotapi.NewMessage(m.Chat.ID, "\xE2\x9D\x8C Недостаточно средств"))
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

	b.deleteMessage(m.Chat.ID, m.MessageID)
	msg := tgbotapi.NewMessage(m.Chat.ID, "Введите локальное название файла")
	msg.ReplyMarkup = tgbotapi.ForceReply{ForceReply: true}
	b.sendTemp(m.Chat.ID, userID, msg)
}

func (b *Bot) handleUploadStep(userID int64, st *uploadState, m *tgbotapi.Message) {
	switch st.step {
	case 1:
		st.local = m.Text
		st.step = 2
		b.deleteMessage(m.Chat.ID, m.MessageID)
		msg := tgbotapi.NewMessage(m.Chat.ID, "Введите часть ссылки")
		msg.ReplyMarkup = tgbotapi.ForceReply{ForceReply: true}
		b.sendTemp(m.Chat.ID, userID, msg)
	case 2:
		st.link = strings.TrimSpace(m.Text)
		st.step = 3
		b.deleteMessage(m.Chat.ID, m.MessageID)
		kb := tgbotapi.NewReplyKeyboard(
			tgbotapi.NewKeyboardButtonRow(
				tgbotapi.NewKeyboardButton("Да"),
				tgbotapi.NewKeyboardButton("Нет"),
			))
		msg := tgbotapi.NewMessage(m.Chat.ID, "Включить уведомления о скачиваниях? (Да/Нет)")
		msg.ReplyMarkup = kb
		b.sendTemp(m.Chat.ID, userID, msg)
	case 3:
		txt := strings.ToLower(m.Text)
		st.notify = txt == "да"
		b.deleteMessage(m.Chat.ID, m.MessageID)
		if err := b.finalizeUpload(userID, st, m.Chat.ID); err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				st.step = 2
				msg := tgbotapi.NewMessage(m.Chat.ID, "Ссылка уже занята, введите другую")
				msg.ReplyMarkup = tgbotapi.ForceReply{ForceReply: true}
				b.sendTemp(m.Chat.ID, userID, msg)
				return
			}
			log.Println(err)
			delete(b.pendingUploads, userID)
			msg := tgbotapi.NewMessage(m.Chat.ID, "Ошибка сохранения")
			b.sendTemp(m.Chat.ID, userID, msg)
			return
		}
		delete(b.pendingUploads, userID)
		b.sendMainMenu(m.Chat.ID, userID, false)
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

	b.deleteLast(userID, chatID)
	msg := tgbotapi.NewMessage(chatID, fmt.Sprintf("Файл сохранён: %s", link))
	b.api.Send(msg)
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

func (b *Bot) createInvoiceProvider(amount float64, provider string) (string, string, string, error) {
	switch provider {
	case "crypto":
		if b.cfg.CryptoBotToken == "" {
			return "", "", "", fmt.Errorf("provider disabled")
		}
		url, id, err := b.createCryptoInvoice(amount)
		return url, id, "crypto", err
	case "xrocket":
		if b.cfg.XRocketToken == "" {
			return "", "", "", fmt.Errorf("provider disabled")
		}
		url, id, err := b.createXRocketInvoice(amount)
		return url, id, "xrocket", err
	default:
		return b.createInvoice(amount)
	}
}

var cryptoAPIBase = "https://pay.crypt.bot/api/"
var xrocketAPIBase = "https://pay.xrocket.tg/"

func (b *Bot) createCryptoInvoice(amount float64) (string, string, error) {
	body := fmt.Sprintf(`{"asset":"USDT","amount":"%.2f","description":"Пополнение счёта на %.2f$"}`,
		amount, amount)
	req, err := http.NewRequest("POST", cryptoAPIBase+"createInvoice", strings.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Crypto-Pay-API-Token", b.cfg.CryptoBotToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("CryptoBot request: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", fmt.Errorf("CryptoBot read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("CryptoBot HTTP %d: %s", resp.StatusCode, string(data))
	}
	type res struct {
		Ok     bool `json:"ok"`
		Result struct {
			InvoiceID json.Number `json:"invoice_id"`
			PayURL    string      `json:"pay_url"`
		} `json:"result"`
	}
	var r res
	if err := json.Unmarshal(data, &r); err != nil {
		return "", "", fmt.Errorf("CryptoBot decode: %w", err)
	}
	if !r.Ok {
		return "", "", fmt.Errorf("CryptoBot API error: %s", string(data))
	}
	return r.Result.PayURL, r.Result.InvoiceID.String(), nil
}

func (b *Bot) createXRocketInvoice(amount float64) (string, string, error) {
	body := fmt.Sprintf(`{"amount":%.2f,"numPayments":1,"currency":"USDT","description":"Пополнение счёта на %.2f$"}`,
		amount, amount)
	req, err := http.NewRequest("POST", xrocketAPIBase+"tg-invoices", strings.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Rocket-Pay-Key", b.cfg.XRocketToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("XRocket request: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", fmt.Errorf("XRocket read: %w", err)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", "", fmt.Errorf("XRocket HTTP %d: %s", resp.StatusCode, string(data))
	}
	type res struct {
		Ok      bool `json:"ok"`
		Success bool `json:"success"`
		Result  struct {
			ID  string `json:"id"`
			URL string `json:"url"`
		} `json:"result"`
		Data struct {
			ID   string `json:"id"`
			Link string `json:"link"`
		} `json:"data"`
	}
	var r res
	if err := json.Unmarshal(data, &r); err != nil {
		return "", "", fmt.Errorf("XRocket decode: %w", err)
	}
	switch {
	case r.Ok:
		return r.Result.URL, r.Result.ID, nil
	case r.Success:
		return r.Data.Link, r.Data.ID, nil
	default:
		return "", "", fmt.Errorf("XRocket API error: %s", string(data))
	}
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
	urlStr := cryptoAPIBase + "getInvoices"
	body := fmt.Sprintf(`{"invoice_ids":[%s]}`, id)
	req, err := http.NewRequest("POST", urlStr, strings.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Crypto-Pay-API-Token", b.cfg.CryptoBotToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("CryptoBot request: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, fmt.Errorf("CryptoBot read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("CryptoBot HTTP %d: %s", resp.StatusCode, string(data))
	}
	type res struct {
		Ok     bool `json:"ok"`
		Result struct {
			Items []struct {
				Status string `json:"status"`
			} `json:"items"`
		} `json:"result"`
	}
	var r res
	if err := json.Unmarshal(data, &r); err != nil {
		return false, fmt.Errorf("CryptoBot decode: %w", err)
	}
	if !r.Ok || len(r.Result.Items) == 0 {
		return false, fmt.Errorf("CryptoBot API error: %s", string(data))
	}
	return r.Result.Items[0].Status == "paid", nil
}

func (b *Bot) checkXRocketInvoice(id string) (bool, error) {
	urlStr := xrocketAPIBase + "tg-invoices/" + id
	req, err := http.NewRequest("GET", urlStr, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Rocket-Pay-Key", b.cfg.XRocketToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("XRocket request: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, fmt.Errorf("XRocket read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("XRocket HTTP %d: %s", resp.StatusCode, string(data))
	}
	type res struct {
		Ok      bool `json:"ok"`
		Success bool `json:"success"`
		Result  struct {
			Status string `json:"status"`
		} `json:"result"`
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	var r res
	if err := json.Unmarshal(data, &r); err != nil {
		return false, fmt.Errorf("XRocket decode: %w", err)
	}
	switch {
	case r.Ok:
		return r.Result.Status == "paid", nil
	case r.Success:
		return r.Data.Status == "paid", nil
	default:
		return false, fmt.Errorf("XRocket API error: %s", string(data))
	}
}

func (b *Bot) handleCallback(q *tgbotapi.CallbackQuery) {
	parts := strings.SplitN(q.Data, ":", 2)
	action := parts[0]
	arg := ""
	if len(parts) == 2 {
		arg = parts[1]
	}
	userID, err := b.db.GetOrCreateUser(q.From.ID)
	if err != nil {
		log.Println(err)
		return
	}
	switch action {
	case "myfiles":
		files, err := b.db.ListFiles(userID)
		if err != nil || len(files) == 0 {
			b.api.Send(tgbotapi.NewCallback(q.ID, "Файлов нет"))
			return
		}
		var rows [][]tgbotapi.InlineKeyboardButton
		for _, f := range files {
			btn := tgbotapi.NewInlineKeyboardButtonData(f.LocalName, "manage:"+f.StorageName)
			rows = append(rows, tgbotapi.NewInlineKeyboardRow(btn))
		}
		kb := tgbotapi.NewInlineKeyboardMarkup(rows...)
		msg := tgbotapi.NewMessage(q.Message.Chat.ID, "Ваши файлы")
		msg.ReplyMarkup = &kb
		b.api.Send(msg)
	case "topup":
		b.pendingTopup[userID] = &topupState{step: 1}
		msg := tgbotapi.NewMessage(q.Message.Chat.ID, "\xF0\x9F\x92\xB0 Введите сумму")
		msg.ReplyMarkup = tgbotapi.ForceReply{ForceReply: true}
		b.sendTemp(q.Message.Chat.ID, userID, msg)
	case "checkpay":
		state, ok := b.pendingInvoices[arg]
		if !ok || state.userID != userID {
			b.api.Send(tgbotapi.NewCallback(q.ID, "Не найдено"))
			return
		}
		paid, err := b.checkInvoice(arg, state.provider)
		if err != nil {
			log.Println("check invoice:", err)
			b.api.Send(tgbotapi.NewCallback(q.ID, "Ошибка"))
			return
		}
		if paid {
			b.db.AdjustBalance(userID, state.amount)
			b.db.AddPayment(userID, state.amount)
			delete(b.pendingInvoices, arg)
			b.api.Send(tgbotapi.NewCallback(q.ID, "Оплачено"))
			b.sendMainMenu(q.Message.Chat.ID, userID, q.From.ID == b.cfg.AdminID)
		} else {
			b.api.Send(tgbotapi.NewCallback(q.ID, "Не оплачено"))
		}
	case "admin":
		if q.From.ID != b.cfg.AdminID {
			return
		}
		kb := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("\xE2\x84\xB9\xEF\xB8\x8F Инфо о пользователе", "a_userinfo"),
			),
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("\xE2\x9E\x95 Добавить баланс", "a_addbal"),
				tgbotapi.NewInlineKeyboardButtonData("\xE2\x9C\x8F\xEF\xB8\x8F Установить баланс", "a_setbal"),
			),
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("\xF0\x9F\x93\x82 Список файлов", "a_files"),
			),
		)
		msg := tgbotapi.NewMessage(q.Message.Chat.ID, "Админ панель")
		msg.ReplyMarkup = kb
		b.api.Send(msg)
	case "a_userinfo":
		if q.From.ID != b.cfg.AdminID {
			return
		}
		b.adminAction[userID] = "userinfo"
		msg := tgbotapi.NewMessage(q.Message.Chat.ID, "Введите telegram id")
		msg.ReplyMarkup = tgbotapi.ForceReply{ForceReply: true}
		b.api.Send(msg)
	case "a_addbal":
		if q.From.ID != b.cfg.AdminID {
			return
		}
		b.adminAction[userID] = "addbal"
		msg := tgbotapi.NewMessage(q.Message.Chat.ID, "Введите telegram id и сумму через пробел")
		msg.ReplyMarkup = tgbotapi.ForceReply{ForceReply: true}
		b.api.Send(msg)
	case "a_setbal":
		if q.From.ID != b.cfg.AdminID {
			return
		}
		b.adminAction[userID] = "setbal"
		msg := tgbotapi.NewMessage(q.Message.Chat.ID, "Введите telegram id и новый баланс")
		msg.ReplyMarkup = tgbotapi.ForceReply{ForceReply: true}
		b.api.Send(msg)
	case "a_files":
		if q.From.ID != b.cfg.AdminID {
			return
		}
		files, err := b.db.ListAllFiles()
		if err != nil || len(files) == 0 {
			b.api.Send(tgbotapi.NewMessage(q.Message.Chat.ID, "Файлов нет"))
			return
		}
		var sb strings.Builder
		for _, f := range files {
			notif := "выкл"
			if f.Notify {
				notif = "вкл"
			}
			sb.WriteString(fmt.Sprintf("%s | %s | %s\n", f.CreatedAt, notif, f.Link))
		}
		b.api.Send(tgbotapi.NewMessage(q.Message.Chat.ID, sb.String()))
	case "manage":
		f, err := b.db.GetFileByStorageName(arg)
		if err != nil || f.UserID != userID {
			return
		}
		notif := "🔔❌"
		if f.Notify {
			notif = "🔔✅"
		}
		kb := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("🔗", "link:"+arg),
				tgbotapi.NewInlineKeyboardButtonData(notif, "notify:"+arg),
				tgbotapi.NewInlineKeyboardButtonData("📄", "log:"+arg),
				tgbotapi.NewInlineKeyboardButtonData("❌", "delete:"+arg),
			),
		)
		msg := tgbotapi.NewMessage(q.Message.Chat.ID, f.LocalName+" -> "+f.Link)
		msg.ReplyMarkup = kb
		b.api.Send(msg)
	case "log":
		f, err := b.db.GetFileByStorageName(arg)
		if err != nil || f.UserID != userID {
			return
		}
		entries, err := b.logs.List(f.ID)
		if err != nil {
			return
		}
		html := buildLogHTML(f, entries)
		tmp := fmt.Sprintf("log_%d.html", f.ID)
		os.WriteFile(tmp, []byte(html), 0644)
		doc := tgbotapi.NewDocument(q.Message.Chat.ID, tgbotapi.FilePath(tmp))
		doc.Caption = "Лог скачиваний"
		b.api.Send(doc)
		os.Remove(tmp)
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
			b.logs.Drop(f.ID)
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
	b.deleteMessage(m.Chat.ID, m.MessageID)
	newLink := strings.TrimSpace(m.Text)
	f, err := b.db.GetFileByStorageName(name)
	if err != nil || f.UserID != userID {
		delete(b.changeLink, userID)
		return
	}
	link := strings.TrimRight(b.cfg.Domain, "/") + "/" + newLink
	_, err = b.db.Exec("UPDATE files SET link=? WHERE id=?", link, f.ID)
	if err == nil {
		msg := tgbotapi.NewMessage(m.Chat.ID, "Ссылка изменена: "+link)
		b.sendTemp(m.Chat.ID, userID, msg)
	}
	delete(b.changeLink, userID)
}

func (b *Bot) sendMainMenu(chatID, userID int64, isAdmin bool) {
	b.deleteLast(userID, chatID)
	rows := [][]tgbotapi.KeyboardButton{
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton("\xE2\x9E\x95 Добавить файл"),
		),
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton("\xF0\x9F\x93\x82 Мои файлы"),
			tgbotapi.NewKeyboardButton("\xF0\x9F\x92\xB0 Пополнить счёт"),
		),
	}
	if isAdmin {
		rows = append(rows, tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton("\xE2\x9A\x99\xEF\xB8\x8F Админ панель"),
		))
	}
	kb := tgbotapi.NewReplyKeyboard(rows...)
	bal, _ := b.db.GetBalance(userID)
	txt := b.cfg.MenuText
	if txt == "" {
		txt = "\xF0\x9F\x92\xB0 Ваш баланс: %%bal%%\n\xF0\x9F\x93\x84 Загрузка: %%price%% USDT\n\xE2\x9E\x95 Возврат за удаление: %%refund%% USDT\nВыберите действие:"
	}
	txt = strings.ReplaceAll(txt, "%%bal%%", fmt.Sprintf("%.2f", bal))
	txt = strings.ReplaceAll(txt, "%%price%%", fmt.Sprintf("%.2f", b.cfg.PriceUpload))
	txt = strings.ReplaceAll(txt, "%%refund%%", fmt.Sprintf("%.2f", b.cfg.PriceRefund))
	msg := tgbotapi.NewMessage(chatID, txt)
	msg.ReplyMarkup = kb
	b.sendTemp(chatID, userID, msg)
}

func (b *Bot) processTopup(userID int64, m *tgbotapi.Message, st *topupState) {
	b.deleteMessage(m.Chat.ID, m.MessageID)
	if st.step == 1 {
		amountStr := strings.TrimSpace(m.Text)
		var amount float64
		fmt.Sscanf(amountStr, "%f", &amount)
		if amount <= 0 || amount > 10000 {
			if amount > 10000 {
				msg := tgbotapi.NewMessage(m.Chat.ID, "Максимальная сумма 10000")
				b.sendTemp(m.Chat.ID, userID, msg)
			} else {
				msg := tgbotapi.NewMessage(m.Chat.ID, "Неверная сумма")
				b.sendTemp(m.Chat.ID, userID, msg)
			}
			delete(b.pendingTopup, userID)
			return
		}
		st.amount = amount
		if b.cfg.CryptoBotToken != "" && b.cfg.XRocketToken != "" {
			st.step = 2
			kb := tgbotapi.NewReplyKeyboard(
				tgbotapi.NewKeyboardButtonRow(
					tgbotapi.NewKeyboardButton("CryptoBot"),
					tgbotapi.NewKeyboardButton("XRocket"),
				),
			)
			msg := tgbotapi.NewMessage(m.Chat.ID, "Выберите провайдера оплаты")
			msg.ReplyMarkup = kb
			b.sendTemp(m.Chat.ID, userID, msg)
			return
		}
		provider := ""
		if b.cfg.CryptoBotToken != "" {
			provider = "crypto"
		} else if b.cfg.XRocketToken != "" {
			provider = "xrocket"
		}
		if provider == "" {
			msg := tgbotapi.NewMessage(m.Chat.ID, "Нет провайдера оплаты")
			b.sendTemp(m.Chat.ID, userID, msg)
			delete(b.pendingTopup, userID)
			return
		}
		if (provider == "crypto" && st.amount < b.cfg.CryptoMinTopup && b.cfg.CryptoMinTopup > 0) ||
			(provider == "xrocket" && st.amount < b.cfg.XRocketMinTopup && b.cfg.XRocketMinTopup > 0) {
			min := b.cfg.CryptoMinTopup
			if provider == "xrocket" {
				min = b.cfg.XRocketMinTopup
			}
			msg := tgbotapi.NewMessage(m.Chat.ID, fmt.Sprintf("Минимальная сумма %.2f", min))
			b.sendTemp(m.Chat.ID, userID, msg)
			delete(b.pendingTopup, userID)
			return
		}
		if err := b.finishInvoice(userID, m.Chat.ID, st.amount, provider); err != nil {
			log.Println("invoice:", err)
			msg := tgbotapi.NewMessage(m.Chat.ID, "Ошибка создания счёта")
			b.sendTemp(m.Chat.ID, userID, msg)
			delete(b.pendingTopup, userID)
			return
		}
	} else if st.step == 2 {
		provider := strings.ToLower(strings.TrimSpace(m.Text))
		if provider == "cryptobot" {
			provider = "crypto"
		} else if provider == "xrocket" {
			provider = "xrocket"
		} else {
			msg := tgbotapi.NewMessage(m.Chat.ID, "Неверный провайдер")
			b.sendTemp(m.Chat.ID, userID, msg)
			return
		}
		if (provider == "crypto" && st.amount < b.cfg.CryptoMinTopup && b.cfg.CryptoMinTopup > 0) ||
			(provider == "xrocket" && st.amount < b.cfg.XRocketMinTopup && b.cfg.XRocketMinTopup > 0) {
			min := b.cfg.CryptoMinTopup
			if provider == "xrocket" {
				min = b.cfg.XRocketMinTopup
			}
			msg := tgbotapi.NewMessage(m.Chat.ID, fmt.Sprintf("Минимальная сумма %.2f", min))
			b.sendTemp(m.Chat.ID, userID, msg)
			delete(b.pendingTopup, userID)
			return
		}
		if err := b.finishInvoice(userID, m.Chat.ID, st.amount, provider); err != nil {
			log.Println("invoice:", err)
			msg := tgbotapi.NewMessage(m.Chat.ID, "Ошибка создания счёта")
			b.sendTemp(m.Chat.ID, userID, msg)
			delete(b.pendingTopup, userID)
			return
		}
	}
}

func (b *Bot) finishInvoice(userID int64, chatID int64, amount float64, provider string) error {
	if amount > 10000 {
		return fmt.Errorf("amount limit")
	}
	url, id, prov, err := b.createInvoiceProvider(amount, provider)
	if err != nil {
		return err
	}
	rm := tgbotapi.NewMessage(chatID, " ")
	rm.ReplyMarkup = tgbotapi.NewRemoveKeyboard(false)
	b.api.Send(rm)
	b.deleteLast(userID, chatID)
	kb := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("\xE2\x9C\x85 Проверить оплату", "checkpay:"+id)),
	)
	msg := tgbotapi.NewMessage(chatID, "Оплатите: "+url)
	msg.ReplyMarkup = kb
	b.sendTemp(chatID, userID, msg)
	b.pendingInvoices[id] = &invoiceState{userID: userID, amount: amount, provider: prov}
	delete(b.pendingTopup, userID)
	return nil
}

func (b *Bot) sendFileList(userID int64, chatID int64, page int) {
	const pageSize = 8
	files, err := b.db.ListFiles(userID)
	if err != nil {
		log.Println(err)
		msg := tgbotapi.NewMessage(chatID, "Ошибка")
		b.sendTemp(chatID, userID, msg)
		return
	}
	if len(files) == 0 {
		msg := tgbotapi.NewMessage(chatID, "Файлов нет")
		b.sendTemp(chatID, userID, msg)
		return
	}
	total := len(files)
	if page*pageSize >= total {
		page = 0
	}
	start := page * pageSize
	end := start + pageSize
	if end > total {
		end = total
	}
	var rows [][]tgbotapi.KeyboardButton
	for _, f := range files[start:end] {
		rows = append(rows, tgbotapi.NewKeyboardButtonRow(tgbotapi.NewKeyboardButton(f.LocalName)))
	}
	nav := []tgbotapi.KeyboardButton{}
	if start > 0 {
		nav = append(nav, tgbotapi.NewKeyboardButton("⬅️"))
	}
	if end < total {
		nav = append(nav, tgbotapi.NewKeyboardButton("➡️"))
	}
	if len(nav) > 0 {
		rows = append(rows, nav)
	}
	rows = append(rows, tgbotapi.NewKeyboardButtonRow(tgbotapi.NewKeyboardButton("↩️ Назад")))
	msg := tgbotapi.NewMessage(chatID, "Ваши файлы:")
	msg.ReplyMarkup = tgbotapi.NewReplyKeyboard(rows...)
	b.sendTemp(chatID, userID, msg)
}

func (b *Bot) handleAdminInput(userID int64, act string, m *tgbotapi.Message) {
	b.deleteMessage(m.Chat.ID, m.MessageID)
	switch act {
	case "userinfo":
		var tg int64
		fmt.Sscanf(m.Text, "%d", &tg)
		row := b.db.QueryRow("SELECT id, balance FROM users WHERE telegram_id=?", tg)
		var id int64
		var bal float64
		if err := row.Scan(&id, &bal); err != nil {
			msg := tgbotapi.NewMessage(m.Chat.ID, "Пользователь не найден")
			b.sendTemp(m.Chat.ID, userID, msg)
		} else {
			msg := tgbotapi.NewMessage(m.Chat.ID, fmt.Sprintf("ID: %d\nБаланс: %.2f", id, bal))
			b.sendTemp(m.Chat.ID, userID, msg)
		}
	case "addbal":
		var tg int64
		var delta float64
		fmt.Sscanf(m.Text, "%d %f", &tg, &delta)
		row := b.db.QueryRow("SELECT id FROM users WHERE telegram_id=?", tg)
		var id int64
		if err := row.Scan(&id); err != nil {
			msg := tgbotapi.NewMessage(m.Chat.ID, "Пользователь не найден")
			b.sendTemp(m.Chat.ID, userID, msg)
		} else {
			b.db.AdjustBalance(id, delta)
			msg := tgbotapi.NewMessage(m.Chat.ID, "Баланс изменён")
			b.sendTemp(m.Chat.ID, userID, msg)
		}
	case "setbal":
		var tg int64
		var val float64
		fmt.Sscanf(m.Text, "%d %f", &tg, &val)
		row := b.db.QueryRow("SELECT id FROM users WHERE telegram_id=?", tg)
		var id int64
		if err := row.Scan(&id); err != nil {
			msg := tgbotapi.NewMessage(m.Chat.ID, "Пользователь не найден")
			b.sendTemp(m.Chat.ID, userID, msg)
		} else {
			b.db.SetBalance(id, val)
			msg := tgbotapi.NewMessage(m.Chat.ID, "Баланс установлен")
			b.sendTemp(m.Chat.ID, userID, msg)
		}
	}
	delete(b.adminAction, userID)
}

func (b *Bot) checkTokens() {
	if b.cfg.CryptoBotToken != "" {
		req, _ := http.NewRequest("GET", "https://pay.crypt.bot/api/getMe", nil)
		req.Header.Set("Crypto-Pay-API-Token", b.cfg.CryptoBotToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			log.Println("[CryptoBot]", err)
		} else {
			defer resp.Body.Close()
			data, _ := io.ReadAll(resp.Body)
			if resp.StatusCode == http.StatusOK {
				log.Println("[CryptoBot] Success connected")
			} else {
				log.Printf("[CryptoBot] %s", strings.TrimSpace(string(data)))
			}
		}
	}
	if b.cfg.XRocketToken != "" {
		req, _ := http.NewRequest("GET", "https://pay.xrocket.tg/app/info", nil)
		req.Header.Set("Rocket-Pay-Key", b.cfg.XRocketToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			log.Println("[xRocket]", err)
		} else {
			defer resp.Body.Close()
			data, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != http.StatusOK {
				log.Printf("[xRocket] %s", strings.TrimSpace(string(data)))
			} else {
				var r struct {
					Success bool `json:"success"`
					Data    struct {
						Name string `json:"name"`
					} `json:"data"`
				}
				if err := json.Unmarshal(data, &r); err == nil && r.Success {
					log.Printf("[xRocket] Success connected to shop %s", r.Data.Name)
				} else {
					log.Printf("[xRocket] %s", strings.TrimSpace(string(data)))
				}
			}
		}
	}
}

func buildLogHTML(f *models.File, entries []logdb.Entry) string {
	var sb strings.Builder
	sb.WriteString(`<!DOCTYPE html><html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Log</title><style>body{background:#111;color:#eee;font-family:sans-serif;padding:20px}table{width:100%;border-collapse:collapse}th,td{padding:8px;border:1px solid #444}th{background:#222}</style></head><body>`)
	sb.WriteString(fmt.Sprintf("<h2>%s</h2>", f.LocalName))
	sb.WriteString(fmt.Sprintf("<p>Ссылка: %s</p>", f.Link))
	sb.WriteString(fmt.Sprintf("<p>Количество скачиваний: %d</p>", len(entries)))
	sb.WriteString("<table><tr><th>#</th><th>IP</th><th>Страна</th><th>Город</th><th>Платформа</th><th>Модель</th><th>ОС</th><th>Браузер</th><th>Дата</th></tr>")
	for i, e := range entries {
		sb.WriteString("<tr>")
		sb.WriteString(fmt.Sprintf("<td>%d</td>", i+1))
		sb.WriteString(fmt.Sprintf("<td>%s</td>", e.IP))
		sb.WriteString(fmt.Sprintf("<td>%s</td>", e.Country))
		sb.WriteString(fmt.Sprintf("<td>%s</td>", e.City))
		sb.WriteString(fmt.Sprintf("<td>%s</td>", e.Platform))
		sb.WriteString(fmt.Sprintf("<td>%s</td>", e.Model))
		sb.WriteString(fmt.Sprintf("<td>%s %s</td>", e.OSName, e.OSVersion))
		sb.WriteString(fmt.Sprintf("<td>%s %s</td>", e.BrowserName, e.BrowserVer))
		sb.WriteString(fmt.Sprintf("<td>%s</td>", e.CreatedAt))
		sb.WriteString("</tr>")
	}
	sb.WriteString("</table></body></html>")
	return sb.String()
}
