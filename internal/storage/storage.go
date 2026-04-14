// Package storage provides SQLite persistence for MAC traffic accounting.
package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// MAC record keys.
type RecordKey struct {
	InterfaceName string
	Ifindex       uint32
	MAC           [6]byte
}

// MAC traffic counters.
type TrafficCounters struct {
	Bytes          uint64
	Packets        uint64
	IngressBytes   uint64
	IngressPackets uint64
	EgressBytes    uint64
	EgressPackets  uint64
}

// Daily record for UTC daily bucket.
type DailyRecord struct {
	Key
	DateUTC        string
	Bytes          uint64
	Packets        uint64
	IngressBytes   uint64
	IngressPackets uint64
	EgressBytes    uint64
	EgressPackets  uint64
}

// Total record for all-time totals.
type TotalRecord struct {
	Key
	Bytes          uint64
	Packets        uint64
	IngressBytes   uint64
	IngressPackets uint64
	EgressBytes    uint64
	EgressPackets  uint64
}

// Checkpoint record for collector state recovery.
type Checkpoint struct {
	Key
	DateUTC            string
	LastRawBytes       uint64
	LastRawPackets     uint64
	LastIngressBytes   uint64
	LastIngressPackets uint64
	LastEgressBytes    uint64
	LastEgressPackets  uint64
}

// Key embeds RecordKey for embedding in records.
type Key struct {
	InterfaceName string
	Ifindex       uint32
	MAC           [6]byte
}

// Repository provides SQLite persistence operations.
type Repository struct {
	db *sql.DB
}

// New creates a new Repository with the given SQLite file path.
func New(dbPath string) (*Repository, error) {
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	// Enable foreign keys and WAL mode pragmas.
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		db.Close()
		return nil, fmt.Errorf("enabling foreign keys: %w", err)
	}

	repo := &Repository{db: db}
	if err := repo.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("initializing schema: %w", err)
	}

	return repo, nil
}

// Close closes the database connection.
func (r *Repository) Close() error {
	return r.db.Close()
}

// DB returns the underlying database handle for testing purposes.
func (r *Repository) DB() *sql.DB {
	return r.db
}

// initSchema creates all required tables and indexes.
func (r *Repository) initSchema() error {
	tx, err := r.db.Begin()
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback()

	// mac_traffic_totals: all-time totals unique on (interface_name, ifindex, mac).
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS mac_traffic_totals (
			interface_name TEXT NOT NULL,
			ifindex         INTEGER NOT NULL,
			mac             BLOB NOT NULL,
			bytes           INTEGER NOT NULL DEFAULT 0,
			packets         INTEGER NOT NULL DEFAULT 0,
			ingress_bytes   INTEGER NOT NULL DEFAULT 0,
			ingress_packets INTEGER NOT NULL DEFAULT 0,
			egress_bytes    INTEGER NOT NULL DEFAULT 0,
			egress_packets  INTEGER NOT NULL DEFAULT 0,
			updated_at      TEXT NOT NULL DEFAULT (datetime('now')),
			PRIMARY KEY (interface_name, ifindex, mac)
		) WITHOUT ROWID
	`); err != nil {
		return fmt.Errorf("creating mac_traffic_totals: %w", err)
	}

	// mac_traffic_daily: UTC daily buckets unique on (interface_name, ifindex, mac, date_utc).
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS mac_traffic_daily (
			interface_name  TEXT NOT NULL,
			ifindex         INTEGER NOT NULL,
			mac             BLOB NOT NULL,
			date_utc        TEXT NOT NULL,
			bytes           INTEGER NOT NULL DEFAULT 0,
			packets         INTEGER NOT NULL DEFAULT 0,
			ingress_bytes   INTEGER NOT NULL DEFAULT 0,
			ingress_packets INTEGER NOT NULL DEFAULT 0,
			egress_bytes    INTEGER NOT NULL DEFAULT 0,
			egress_packets  INTEGER NOT NULL DEFAULT 0,
			updated_at      TEXT NOT NULL DEFAULT (datetime('now')),
			PRIMARY KEY (interface_name, ifindex, mac, date_utc)
		) WITHOUT ROWID
	`); err != nil {
		return fmt.Errorf("creating mac_traffic_daily: %w", err)
	}

	// collector_checkpoints: last-seen raw counter values for crash-safe recovery.
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS collector_checkpoints (
			interface_name       TEXT NOT NULL,
			ifindex              INTEGER NOT NULL,
			mac                  BLOB NOT NULL,
			date_utc             TEXT NOT NULL,
			last_raw_bytes       INTEGER NOT NULL DEFAULT 0,
			last_raw_packets     INTEGER NOT NULL DEFAULT 0,
			last_ingress_bytes       INTEGER NOT NULL DEFAULT 0,
			last_ingress_packets     INTEGER NOT NULL DEFAULT 0,
			last_egress_bytes        INTEGER NOT NULL DEFAULT 0,
			last_egress_packets      INTEGER NOT NULL DEFAULT 0,
			updated_at           TEXT NOT NULL DEFAULT (datetime('now')),
			PRIMARY KEY (interface_name, ifindex, mac)
		) WITHOUT ROWID
	`); err != nil {
		return fmt.Errorf("creating collector_checkpoints: %w", err)
	}

	// Index on date_utc for pruning cutoffs.
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_daily_date_utc ON mac_traffic_daily(date_utc)`); err != nil {
		return fmt.Errorf("creating idx_daily_date_utc: %w", err)
	}

	// Index on interface_name for filtered queries.
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_totals_interface ON mac_traffic_totals(interface_name)`); err != nil {
		return fmt.Errorf("creating idx_totals_interface: %w", err)
	}

	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_daily_interface ON mac_traffic_daily(interface_name)`); err != nil {
		return fmt.Errorf("creating idx_daily_interface: %w", err)
	}

	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_checkpoints_interface ON collector_checkpoints(interface_name)`); err != nil {
		return fmt.Errorf("creating idx_checkpoints_interface: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing schema: %w", err)
	}

	return nil
}

// UpsertDaily upserts a daily bucket record, accumulating counters on conflict.
func (r *Repository) UpsertDaily(ctx context.Context, key *RecordKey, dateUTC string, delta *TrafficCounters) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback()

	query := `
		INSERT INTO mac_traffic_daily (interface_name, ifindex, mac, date_utc, bytes, packets, ingress_bytes, ingress_packets, egress_bytes, egress_packets)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(interface_name, ifindex, mac, date_utc) DO UPDATE SET
			bytes           = bytes           + excluded.bytes,
			packets         = packets         + excluded.packets,
			ingress_bytes   = ingress_bytes   + excluded.ingress_bytes,
			ingress_packets = ingress_packets + excluded.ingress_packets,
			egress_bytes    = egress_bytes    + excluded.egress_bytes,
			egress_packets  = egress_packets  + excluded.egress_packets,
			updated_at      = datetime('now')
	`

	_, err = tx.ExecContext(ctx, query,
		key.InterfaceName,
		key.Ifindex,
		key.MAC[:],
		dateUTC,
		delta.Bytes,
		delta.Packets,
		delta.IngressBytes,
		delta.IngressPackets,
		delta.EgressBytes,
		delta.EgressPackets,
	)
	if err != nil {
		return fmt.Errorf("upserting daily record: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing daily upsert: %w", err)
	}

	return nil
}

// UpsertTotal upserts an all-time totals record, accumulating counters on conflict.
func (r *Repository) UpsertTotal(ctx context.Context, key *RecordKey, delta *TrafficCounters) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback()

	query := `
		INSERT INTO mac_traffic_totals (interface_name, ifindex, mac, bytes, packets, ingress_bytes, ingress_packets, egress_bytes, egress_packets)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(interface_name, ifindex, mac) DO UPDATE SET
			bytes           = bytes           + excluded.bytes,
			packets         = packets         + excluded.packets,
			ingress_bytes   = ingress_bytes   + excluded.ingress_bytes,
			ingress_packets = ingress_packets + excluded.ingress_packets,
			egress_bytes    = egress_bytes    + excluded.egress_bytes,
			egress_packets  = egress_packets  + excluded.egress_packets,
			updated_at      = datetime('now')
	`

	_, err = tx.ExecContext(ctx, query,
		key.InterfaceName,
		key.Ifindex,
		key.MAC[:],
		delta.Bytes,
		delta.Packets,
		delta.IngressBytes,
		delta.IngressPackets,
		delta.EgressBytes,
		delta.EgressPackets,
	)
	if err != nil {
		return fmt.Errorf("upserting total record: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing total upsert: %w", err)
	}

	return nil
}

// UpsertCheckpoint upserts collector checkpoint for a key.
func (r *Repository) UpsertCheckpoint(ctx context.Context, key *RecordKey, dateUTC string, raw *TrafficCounters) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback()

	query := `
		INSERT INTO collector_checkpoints (interface_name, ifindex, mac, date_utc, last_raw_bytes, last_raw_packets, last_ingress_bytes, last_ingress_packets, last_egress_bytes, last_egress_packets)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(interface_name, ifindex, mac) DO UPDATE SET
			date_utc               = excluded.date_utc,
			last_raw_bytes         = excluded.last_raw_bytes,
			last_raw_packets       = excluded.last_raw_packets,
			last_ingress_bytes     = excluded.last_ingress_bytes,
			last_ingress_packets   = excluded.last_ingress_packets,
			last_egress_bytes      = excluded.last_egress_bytes,
			last_egress_packets    = excluded.last_egress_packets,
			updated_at             = datetime('now')
	`

	_, err = tx.ExecContext(ctx, query,
		key.InterfaceName,
		key.Ifindex,
		key.MAC[:],
		dateUTC,
		raw.Bytes,
		raw.Packets,
		raw.IngressBytes,
		raw.IngressPackets,
		raw.EgressBytes,
		raw.EgressPackets,
	)
	if err != nil {
		return fmt.Errorf("upserting checkpoint: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing checkpoint: %w", err)
	}

	return nil
}

// GetCheckpoint retrieves the last checkpoint for a key, or nil if none exists.
func (r *Repository) GetCheckpoint(ctx context.Context, key *RecordKey) (*Checkpoint, error) {
	query := `
		SELECT interface_name, ifindex, mac, date_utc, last_raw_bytes, last_raw_packets,
		       last_ingress_bytes, last_ingress_packets, last_egress_bytes, last_egress_packets
		FROM collector_checkpoints
		WHERE interface_name = ? AND ifindex = ? AND mac = ?
	`

	var cp Checkpoint
	var macBlob []byte

	err := r.db.QueryRowContext(ctx, query, key.InterfaceName, key.Ifindex, key.MAC[:]).Scan(
		&cp.InterfaceName,
		&cp.Ifindex,
		&macBlob,
		&cp.DateUTC,
		&cp.LastRawBytes,
		&cp.LastRawPackets,
		&cp.LastIngressBytes,
		&cp.LastIngressPackets,
		&cp.LastEgressBytes,
		&cp.LastEgressPackets,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("querying checkpoint: %w", err)
	}

	copy(cp.MAC[:], macBlob)
	return &cp, nil
}

// GetDaily retrieves a daily record for a key and date.
func (r *Repository) GetDaily(ctx context.Context, key *RecordKey, dateUTC string) (*DailyRecord, error) {
	query := `
		SELECT interface_name, ifindex, mac, date_utc, bytes, packets, ingress_bytes, ingress_packets, egress_bytes, egress_packets
		FROM mac_traffic_daily
		WHERE interface_name = ? AND ifindex = ? AND mac = ? AND date_utc = ?
	`

	var dr DailyRecord
	var macBlob []byte

	err := r.db.QueryRowContext(ctx, query, key.InterfaceName, key.Ifindex, key.MAC[:], dateUTC).Scan(
		&dr.InterfaceName,
		&dr.Ifindex,
		&macBlob,
		&dr.DateUTC,
		&dr.Bytes,
		&dr.Packets,
		&dr.IngressBytes,
		&dr.IngressPackets,
		&dr.EgressBytes,
		&dr.EgressPackets,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("querying daily record: %w", err)
	}

	copy(dr.MAC[:], macBlob)
	return &dr, nil
}

// GetTotal retrieves an all-time total for a key.
func (r *Repository) GetTotal(ctx context.Context, key *RecordKey) (*TotalRecord, error) {
	query := `
		SELECT interface_name, ifindex, mac, bytes, packets, ingress_bytes, ingress_packets, egress_bytes, egress_packets
		FROM mac_traffic_totals
		WHERE interface_name = ? AND ifindex = ? AND mac = ?
	`

	var tr TotalRecord
	var macBlob []byte

	err := r.db.QueryRowContext(ctx, query, key.InterfaceName, key.Ifindex, key.MAC[:]).Scan(
		&tr.InterfaceName,
		&tr.Ifindex,
		&macBlob,
		&tr.Bytes,
		&tr.Packets,
		&tr.IngressBytes,
		&tr.IngressPackets,
		&tr.EgressBytes,
		&tr.EgressPackets,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("querying total record: %w", err)
	}

	copy(tr.MAC[:], macBlob)
	return &tr, nil
}

// ListDaily records for a date range (inclusive) with optional interface filter.
func (r *Repository) ListDaily(ctx context.Context, dateStart, dateEnd, interfaceFilter string) ([]DailyRecord, error) {
	var query string
	var args []interface{}

	if interfaceFilter != "" {
		query = `
			SELECT interface_name, ifindex, mac, date_utc, bytes, packets, ingress_bytes, ingress_packets, egress_bytes, egress_packets
			FROM mac_traffic_daily
			WHERE interface_name = ? AND date_utc >= ? AND date_utc <= ?
			ORDER BY interface_name, ifindex, mac, date_utc
		`
		args = []interface{}{interfaceFilter, dateStart, dateEnd}
	} else {
		query = `
			SELECT interface_name, ifindex, mac, date_utc, bytes, packets, ingress_bytes, ingress_packets, egress_bytes, egress_packets
			FROM mac_traffic_daily
			WHERE date_utc >= ? AND date_utc <= ?
			ORDER BY interface_name, ifindex, mac, date_utc
		`
		args = []interface{}{dateStart, dateEnd}
	}

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing daily records: %w", err)
	}
	defer rows.Close()

	var records []DailyRecord
	for rows.Next() {
		var dr DailyRecord
		var macBlob []byte

		if err := rows.Scan(
			&dr.InterfaceName,
			&dr.Ifindex,
			&macBlob,
			&dr.DateUTC,
			&dr.Bytes,
			&dr.Packets,
			&dr.IngressBytes,
			&dr.IngressPackets,
			&dr.EgressBytes,
			&dr.EgressPackets,
		); err != nil {
			return nil, fmt.Errorf("scanning daily record: %w", err)
		}

		copy(dr.MAC[:], macBlob)
		records = append(records, dr)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating daily rows: %w", err)
	}

	return records, nil
}

// ListTotals returns all all-time totals with optional interface filter.
func (r *Repository) ListTotals(ctx context.Context, interfaceFilter string) ([]TotalRecord, error) {
	var query string
	var args []interface{}

	if interfaceFilter != "" {
		query = `
			SELECT interface_name, ifindex, mac, bytes, packets, ingress_bytes, ingress_packets, egress_bytes, egress_packets
			FROM mac_traffic_totals
			WHERE interface_name = ?
			ORDER BY interface_name, ifindex, mac
		`
		args = []interface{}{interfaceFilter}
	} else {
		query = `
			SELECT interface_name, ifindex, mac, bytes, packets, ingress_bytes, ingress_packets, egress_bytes, egress_packets
			FROM mac_traffic_totals
			ORDER BY interface_name, ifindex, mac
		`
	}

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing totals: %w", err)
	}
	defer rows.Close()

	var records []TotalRecord
	for rows.Next() {
		var tr TotalRecord
		var macBlob []byte

		if err := rows.Scan(
			&tr.InterfaceName,
			&tr.Ifindex,
			&macBlob,
			&tr.Bytes,
			&tr.Packets,
			&tr.IngressBytes,
			&tr.IngressPackets,
			&tr.EgressBytes,
			&tr.EgressPackets,
		); err != nil {
			return nil, fmt.Errorf("scanning total record: %w", err)
		}

		copy(tr.MAC[:], macBlob)
		records = append(records, tr)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating total rows: %w", err)
	}

	return records, nil
}

// PruneDaily removes daily records older than the cutoff date (UTC).
func (r *Repository) PruneDaily(ctx context.Context, cutoffDateUTC string) (int64, error) {
	result, err := r.db.ExecContext(ctx,
		`DELETE FROM mac_traffic_daily WHERE date_utc < ?`,
		cutoffDateUTC,
	)
	if err != nil {
		return 0, fmt.Errorf("pruning daily records: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("getting rows affected: %w", err)
	}

	return rowsAffected, nil
}

// ListCheckpoints returns all checkpoints with optional interface filter.
func (r *Repository) ListCheckpoints(ctx context.Context, interfaceFilter string) ([]Checkpoint, error) {
	var query string
	var args []interface{}

	if interfaceFilter != "" {
		query = `
			SELECT interface_name, ifindex, mac, date_utc, last_raw_bytes, last_raw_packets,
			       last_ingress_bytes, last_ingress_packets, last_egress_bytes, last_egress_packets
			FROM collector_checkpoints
			WHERE interface_name = ?
			ORDER BY interface_name, ifindex, mac
		`
		args = []interface{}{interfaceFilter}
	} else {
		query = `
			SELECT interface_name, ifindex, mac, date_utc, last_raw_bytes, last_raw_packets,
			       last_ingress_bytes, last_ingress_packets, last_egress_bytes, last_egress_packets
			FROM collector_checkpoints
			ORDER BY interface_name, ifindex, mac
		`
	}

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing checkpoints: %w", err)
	}
	defer rows.Close()

	var checkpoints []Checkpoint
	for rows.Next() {
		var cp Checkpoint
		var macBlob []byte

		if err := rows.Scan(
			&cp.InterfaceName,
			&cp.Ifindex,
			&macBlob,
			&cp.DateUTC,
			&cp.LastRawBytes,
			&cp.LastRawPackets,
			&cp.LastIngressBytes,
			&cp.LastIngressPackets,
			&cp.LastEgressBytes,
			&cp.LastEgressPackets,
		); err != nil {
			return nil, fmt.Errorf("scanning checkpoint: %w", err)
		}

		copy(cp.MAC[:], macBlob)
		checkpoints = append(checkpoints, cp)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating checkpoint rows: %w", err)
	}

	return checkpoints, nil
}

// CurrentDateUTC returns the current date in UTC as YYYY-MM-DD.
func CurrentDateUTC() string {
	return time.Now().UTC().Format("2006-01-02")
}

// FormatDateUTC formats a time.Time as YYYY-MM-DD in UTC.
func FormatDateUTC(t time.Time) string {
	return t.UTC().Format("2006-01-02")
}

// PruneCutoffDate returns the cutoff date for retention pruning (400 days ago UTC).
func PruneCutoffDate() string {
	return time.Now().UTC().AddDate(0, 0, -400).Format("2006-01-02")
}
