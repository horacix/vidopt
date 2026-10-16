package database

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// DB wraps the standard sql.DB connection with custom operations.
type DB struct {
	conn *sql.DB
}

// Open initializes and migrates the SQLite database.
func Open(dbPath string) (*DB, error) {
	if dbPath != ":memory:" {
		dir := filepath.Dir(dbPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create db directory %q: %w", dir, err)
		}
	}

	conn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database at %q: %w", dbPath, err)
	}
	conn.SetMaxOpenConns(1)

	// Enable WAL mode for high concurrency & reliability on NAS
	if dbPath != ":memory:" {
		if _, err := conn.Exec(`PRAGMA journal_mode = WAL; PRAGMA busy_timeout = 5000;`); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("failed to set pragmas: %w", err)
		}
	}

	db := &DB{conn: conn}
	if err := db.migrate(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("migration failed: %w", err)
	}

	return db, nil
}

// Close closes the underlying database connection.
func (db *DB) Close() error {
	if db.conn != nil {
		return db.conn.Close()
	}
	return nil
}

// migrate creates required tables and indexes if they do not exist.
func (db *DB) migrate() error {
	query := `
	CREATE TABLE IF NOT EXISTS processed_files (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		file_path TEXT UNIQUE NOT NULL,
		output_path TEXT DEFAULT '',
		file_size_bytes INTEGER NOT NULL,
		mod_time DATETIME NOT NULL,
		duration_seconds REAL DEFAULT 0,
		source_codec TEXT DEFAULT '',
		source_bitrate INTEGER DEFAULT 0,
		source_resolution TEXT DEFAULT '',
		status TEXT NOT NULL,
		probe_savings_ratio REAL DEFAULT 0,
		target_codec TEXT DEFAULT '',
		chosen_crf INTEGER DEFAULT 0,
		vmaf_score REAL DEFAULT 0,
		new_size_bytes INTEGER DEFAULT 0,
		saved_bytes INTEGER DEFAULT 0,
		encoder_used TEXT DEFAULT '',
		processed_at DATETIME,
		error_message TEXT DEFAULT ''
	);

	CREATE INDEX IF NOT EXISTS idx_file_path ON processed_files(file_path);
	CREATE INDEX IF NOT EXISTS idx_status ON processed_files(status);
	`
	_, err := db.conn.Exec(query)
	return err
}
