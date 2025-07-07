# Telegram File Storage Bot

This project provides a basic Telegram bot for storing user files with a pay-per-upload model. It is written in Go and uses SQLite for storage.

Features include:

- Receiving documents from users via Telegram.
- Automatic generation of a configuration file on first run.
- Limiting file size with optional paid extra space.
- SQLite database to track users, balances and stored files.
- Commands `/balance`, `/topup`, `/check` and `/files` to manage funds and list uploaded files.
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
- `price_upload` - cost of storing one file
- `price_refund` - refund when a file is deleted

Only this configuration file is stored outside the compiled binary.
Example: `./filestorage` will start an HTTP server. To enable HTTPS run with certificates and set `domain` to https URL.
