package logdb

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// DB handles download logs in a separate SQLite database.
type DB struct {
	*sql.DB
}

// Entry represents a single download event.
type Entry struct {
	ID          int64
	CreatedAt   string
	IP          string
	City        string
	Country     string
	Platform    string
	Model       string
	OSName      string
	OSVersion   string
	BrowserName string
	BrowserVer  string
}

// New opens database at given path creating file if needed.
func New(path string) (*DB, error) {
	database, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	return &DB{database}, nil
}

func tableName(fileID int64) string {
	return fmt.Sprintf("log_%d", fileID)
}

func (db *DB) ensureTable(fileID int64) error {
	q := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
        id INTEGER PRIMARY KEY,
        created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
        ip TEXT,
        city TEXT,
        country TEXT,
        platform TEXT,
        model TEXT,
        os_name TEXT,
        os_version TEXT,
        browser_name TEXT,
        browser_ver TEXT
    );`, tableName(fileID))
	_, err := db.Exec(q)
	return err
}

// Add stores a download entry for given file.
func (db *DB) Add(fileID int64, e *Entry) error {
	if err := db.ensureTable(fileID); err != nil {
		return err
	}
	q := fmt.Sprintf(`INSERT INTO %s(ip, city, country, platform, model, os_name, os_version, browser_name, browser_ver)
        VALUES(?,?,?,?,?,?,?,?,?)`, tableName(fileID))
	_, err := db.Exec(q, e.IP, e.City, e.Country, e.Platform, e.Model, e.OSName, e.OSVersion, e.BrowserName, e.BrowserVer)
	return err
}

// List returns all entries for file sorted by creation time ascending.
func (db *DB) List(fileID int64) ([]Entry, error) {
	if err := db.ensureTable(fileID); err != nil {
		return nil, err
	}
	q := fmt.Sprintf(`SELECT id, created_at, ip, city, country, platform, model, os_name, os_version, browser_name, browser_ver FROM %s ORDER BY created_at ASC`, tableName(fileID))
	rows, err := db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.CreatedAt, &e.IP, &e.City, &e.Country, &e.Platform, &e.Model, &e.OSName, &e.OSVersion, &e.BrowserName, &e.BrowserVer); err != nil {
			return nil, err
		}
		res = append(res, e)
	}
	return res, rows.Err()
}

// Drop removes log table for file if exists.
func (db *DB) Drop(fileID int64) error {
	q := fmt.Sprintf(`DROP TABLE IF EXISTS %s`, tableName(fileID))
	_, err := db.Exec(q)
	return err
}
