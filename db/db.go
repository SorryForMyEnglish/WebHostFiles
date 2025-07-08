package db

import (
	"database/sql"

	_ "modernc.org/sqlite"

	"github.com/example/filestoragebot/models"
)

type DB struct {
	*sql.DB
}

func New(path string) (*DB, error) {
	database, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if err := migrate(database); err != nil {
		return nil, err
	}
	return &DB{database}, nil
}

func migrate(db *sql.DB) error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS users(
                        id INTEGER PRIMARY KEY,
                        telegram_id INTEGER UNIQUE,
                        balance REAL DEFAULT 0
                );`,
		`CREATE TABLE IF NOT EXISTS files(
                        id INTEGER PRIMARY KEY,
                        user_id INTEGER,
                        local_name TEXT,
                        storage_name TEXT,
                        link TEXT UNIQUE,
                        notify INTEGER DEFAULT 0,
                        size INTEGER,
                        created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
                );`,
		`CREATE TABLE IF NOT EXISTS payments(
                        id INTEGER PRIMARY KEY,
                        user_id INTEGER,
                        amount REAL,
                        created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
                );`,
	}
	for _, q := range queries {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	// ensure created_at column exists in older databases
	db.Exec("ALTER TABLE files ADD COLUMN created_at TIMESTAMP")
	return nil
}

// GetOrCreateUser returns a user by telegram ID, creating a record if necessary.
func (db *DB) GetOrCreateUser(tgID int64) (int64, error) {
	var id int64
	err := db.QueryRow("SELECT id FROM users WHERE telegram_id=?", tgID).Scan(&id)
	if err == sql.ErrNoRows {
		res, err := db.Exec("INSERT INTO users(telegram_id) VALUES(?)", tgID)
		if err != nil {
			return 0, err
		}
		id, err = res.LastInsertId()
		if err != nil {
			return 0, err
		}
		return id, nil
	}
	return id, err
}

func (db *DB) AdjustBalance(userID int64, delta float64) error {
	_, err := db.Exec("UPDATE users SET balance = balance + ? WHERE id=?", delta, userID)
	return err
}

func (db *DB) SetBalance(userID int64, value float64) error {
	_, err := db.Exec("UPDATE users SET balance = ? WHERE id=?", value, userID)
	return err
}

func (db *DB) GetBalance(userID int64) (float64, error) {
	var b float64
	err := db.QueryRow("SELECT balance FROM users WHERE id=?", userID).Scan(&b)
	return b, err
}

func (db *DB) AddFile(f *models.File) error {
	res, err := db.Exec(`INSERT INTO files(user_id, local_name, storage_name, link, notify, size)
                VALUES(?,?,?,?,?,?)`, f.UserID, f.LocalName, f.StorageName, f.Link, boolToInt(f.Notify), f.Size)
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	f.ID = id
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (db *DB) ListFiles(userID int64) ([]models.File, error) {
	rows, err := db.Query("SELECT id, user_id, local_name, storage_name, link, notify, size, created_at FROM files WHERE user_id=?", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var files []models.File
	for rows.Next() {
		var f models.File
		var notify int
		if err := rows.Scan(&f.ID, &f.UserID, &f.LocalName, &f.StorageName, &f.Link, &notify, &f.Size, &f.CreatedAt); err != nil {
			return nil, err
		}
		f.Notify = notify == 1
		files = append(files, f)
	}
	return files, rows.Err()
}

func (db *DB) GetFileByStorageName(name string) (*models.File, error) {
	row := db.QueryRow("SELECT id, user_id, local_name, storage_name, link, notify, size, created_at FROM files WHERE storage_name=?", name)
	var f models.File
	var notify int
	if err := row.Scan(&f.ID, &f.UserID, &f.LocalName, &f.StorageName, &f.Link, &notify, &f.Size, &f.CreatedAt); err != nil {
		return nil, err
	}
	f.Notify = notify == 1
	return &f, nil
}

func (db *DB) GetFileByLocalName(userID int64, local string) (*models.File, error) {
	row := db.QueryRow("SELECT id, user_id, local_name, storage_name, link, notify, size, created_at FROM files WHERE user_id=? AND local_name=?", userID, local)
	var f models.File
	var notify int
	if err := row.Scan(&f.ID, &f.UserID, &f.LocalName, &f.StorageName, &f.Link, &notify, &f.Size, &f.CreatedAt); err != nil {
		return nil, err
	}
	f.Notify = notify == 1
	return &f, nil
}

// GetFileByLink returns a file record by its full link.
func (db *DB) GetFileByLink(link string) (*models.File, error) {
	row := db.QueryRow("SELECT id, user_id, local_name, storage_name, link, notify, size, created_at FROM files WHERE link=?", link)
	var f models.File
	var notify int
	if err := row.Scan(&f.ID, &f.UserID, &f.LocalName, &f.StorageName, &f.Link, &notify, &f.Size, &f.CreatedAt); err != nil {
		return nil, err
	}
	f.Notify = notify == 1
	return &f, nil
}

func (db *DB) DeleteFile(id int64) error {
	_, err := db.Exec("DELETE FROM files WHERE id=?", id)
	return err
}

func (db *DB) AddPayment(userID int64, amount float64) error {
	_, err := db.Exec(`INSERT INTO payments(user_id, amount) VALUES(?,?)`, userID, amount)
	return err
}

func (db *DB) GetTelegramID(userID int64) (int64, error) {
	var tg int64
	err := db.QueryRow("SELECT telegram_id FROM users WHERE id=?", userID).Scan(&tg)
	return tg, err
}

func (db *DB) ListAllFiles() ([]models.File, error) {
	rows, err := db.Query("SELECT id, user_id, local_name, storage_name, link, notify, size, created_at FROM files")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var files []models.File
	for rows.Next() {
		var f models.File
		var notify int
		if err := rows.Scan(&f.ID, &f.UserID, &f.LocalName, &f.StorageName, &f.Link, &notify, &f.Size, &f.CreatedAt); err != nil {
			return nil, err
		}
		f.Notify = notify == 1
		files = append(files, f)
	}
	return files, rows.Err()
}
