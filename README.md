# Telegram File Storage Bot

This project provides a basic Telegram bot for storing user files with a pay-per-upload model. It is written in Go and uses SQLite for storage.

Features include:

- Receiving documents from users via Telegram.
- Automatic generation of a configuration file on first run.
- Limiting file size with optional paid extra space.
- SQLite database to track users, balances and stored files.
- Управление ботом происходит через меню с кнопками.
- Built-in HTTP/HTTPS server to serve files with optional download notifications.

Payment integration with CryptoBot and xRocket can be enabled by filling the tokens in `config.yml`. By default uploading a file costs `price_upload` and removing it refunds `price_refund` back to the user balance.

## Building

```bash
go build -o filestorage ./cmd
```

Run the binary and fill in required tokens inside `config.yml` before using the bot.
Important fields include:

- `domain` - base URL used for links sent to users
- `http_address` - address for the built-in file server (default `:8080`)
 - `tls_cert` and `tls_key` - paths to certificate and key for HTTPS. When set, the file server will use TLS.
- `admin_id` - Telegram ID администратора с доступом к админ‑панели
- `price_upload` - cost of storing one file
- `price_refund` - refund when a file is deleted

Only this configuration file is stored outside the compiled binary.
Example: `./filestorage` will start an HTTP server. To enable HTTPS run with certificates and set `domain` to https URL.

## Настройка HTTPS

Для работы через TLS необходимо сгенерировать сертификат и приватный ключ,
а затем указать пути к ним в полях `tls_cert` и `tls_key` файла `config.yml`.
Пример создания самоподписанного сертификата с помощью `openssl`:

```bash
openssl req -x509 -newkey rsa:4096 -keyout key.pem -out cert.pem -days 365 -nodes
```

Полученные `cert.pem` и `key.pem` разместите в удобном месте и пропишите их пути
в конфигурации. После перезапуска бот будет обслуживать файлы по HTTPS.

Также в конфигурации появился параметр `admin_id` – Telegram ID администратора,
которому будет доступна админ‑панель бота.
