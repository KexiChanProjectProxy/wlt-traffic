// Package storage persists per-device traffic in SQLite.
//
// Two tables are kept:
//
//	device_traffic_daily  one row per (local day, interface, MAC)
//	device_traffic_total  one row per (interface, MAC), never pruned
//
// Counters are "up" (sent by the device) and "down" (received by the device).
// Tables from schema v1 (mac_traffic_*, collector_checkpoints) are left in
// place untouched; their direction/attribution was wrong, so they are not
// migrated.
package storage

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// Counters are byte/packet counts from the device's point of view.
type Counters struct {
	UpBytes     uint64 `json:"upload_bytes"`
	UpPackets   uint64 `json:"upload_packets"`
	DownBytes   uint64 `json:"download_bytes"`
	DownPackets uint64 `json:"download_packets"`
}

func (c Counters) Add(o Counters) Counters {
	return Counters{c.UpBytes + o.UpBytes, c.UpPackets + o.UpPackets, c.DownBytes + o.DownBytes, c.DownPackets + o.DownPackets}
}

func (c Counters) IsZero() bool { return c == Counters{} }

// Delta is traffic to add for one device on one interface on one local day.
type Delta struct {
	Day      string // YYYY-MM-DD in the configured time zone
	Iface    string
	MAC      string // aa:bb:cc:dd:ee:ff
	Counters Counters
}

// Repository wraps the SQLite database.
type Repository struct {
	db *sql.DB
}

// New opens (and if needed creates) the database.
func New(path string) (*Repository, error) {
	db, err := sql.Open("sqlite3", "file:"+path+"?_journal_mode=WAL&_busy_timeout=5000&_synchronous=NORMAL")
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(1) // SQLite: one writer; avoids SQLITE_BUSY between our own goroutines
	r := &Repository{db: db}
	if err := r.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return r, nil
}

func (r *Repository) Close() error { return r.db.Close() }

func (r *Repository) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS device_traffic_daily (
			day          TEXT    NOT NULL,
			iface        TEXT    NOT NULL,
			mac          TEXT    NOT NULL,
			up_bytes     INTEGER NOT NULL DEFAULT 0,
			up_packets   INTEGER NOT NULL DEFAULT 0,
			down_bytes   INTEGER NOT NULL DEFAULT 0,
			down_packets INTEGER NOT NULL DEFAULT 0,
			updated_at   INTEGER NOT NULL,
			PRIMARY KEY (day, iface, mac)
		) WITHOUT ROWID`,
		`CREATE INDEX IF NOT EXISTS idx_daily_mac_day ON device_traffic_daily(mac, day)`,
		`CREATE TABLE IF NOT EXISTS device_traffic_total (
			iface        TEXT    NOT NULL,
			mac          TEXT    NOT NULL,
			up_bytes     INTEGER NOT NULL DEFAULT 0,
			up_packets   INTEGER NOT NULL DEFAULT 0,
			down_bytes   INTEGER NOT NULL DEFAULT 0,
			down_packets INTEGER NOT NULL DEFAULT 0,
			first_seen   INTEGER NOT NULL,
			last_seen    INTEGER NOT NULL,
			PRIMARY KEY (iface, mac)
		) WITHOUT ROWID`,
	}
	for _, s := range stmts {
		if _, err := r.db.Exec(s); err != nil {
			return fmt.Errorf("migrate schema: %w", err)
		}
	}
	return nil
}

// sqlite cannot bind uint64 values above MaxInt64.
func i64(v uint64) int64 {
	if v > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(v)
}

// AddDeltas adds all deltas to the daily and total tables in one transaction.
func (r *Repository) AddDeltas(ctx context.Context, deltas []Delta, now time.Time) error {
	if len(deltas) == 0 {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	daily, err := tx.PrepareContext(ctx, `
		INSERT INTO device_traffic_daily (day, iface, mac, up_bytes, up_packets, down_bytes, down_packets, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(day, iface, mac) DO UPDATE SET
			up_bytes     = up_bytes     + excluded.up_bytes,
			up_packets   = up_packets   + excluded.up_packets,
			down_bytes   = down_bytes   + excluded.down_bytes,
			down_packets = down_packets + excluded.down_packets,
			updated_at   = excluded.updated_at`)
	if err != nil {
		return err
	}
	defer daily.Close()
	total, err := tx.PrepareContext(ctx, `
		INSERT INTO device_traffic_total (iface, mac, up_bytes, up_packets, down_bytes, down_packets, first_seen, last_seen)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(iface, mac) DO UPDATE SET
			up_bytes     = up_bytes     + excluded.up_bytes,
			up_packets   = up_packets   + excluded.up_packets,
			down_bytes   = down_bytes   + excluded.down_bytes,
			down_packets = down_packets + excluded.down_packets,
			last_seen    = excluded.last_seen`)
	if err != nil {
		return err
	}
	defer total.Close()

	ts := now.Unix()
	for _, d := range deltas {
		c := d.Counters
		if _, err := daily.ExecContext(ctx, d.Day, d.Iface, d.MAC, i64(c.UpBytes), i64(c.UpPackets), i64(c.DownBytes), i64(c.DownPackets), ts); err != nil {
			return fmt.Errorf("upsert daily: %w", err)
		}
		if _, err := total.ExecContext(ctx, d.Iface, d.MAC, i64(c.UpBytes), i64(c.UpPackets), i64(c.DownBytes), i64(c.DownPackets), ts, ts); err != nil {
			return fmt.Errorf("upsert total: %w", err)
		}
	}
	return tx.Commit()
}

// Query selects traffic for a day range, or all-time when From is empty.
type Query struct {
	From, To   string // inclusive YYYY-MM-DD; both empty = all time
	MAC        string
	Iface      string
	GroupByMAC bool   // sum a device's traffic over all interfaces
	Sort       string // "total" (default), "upload", "download", "mac"
	Limit      int
	Offset     int
}

// Row is one result of a Query.
type Row struct {
	Iface    string   `json:"interface"`
	Ifaces   []string `json:"interfaces,omitempty"`
	MAC      string   `json:"mac"`
	Counters Counters `json:"-"`
	LastSeen int64    `json:"last_seen"`
}

// QueryTraffic returns matching rows sorted as requested plus the number of
// rows before LIMIT/OFFSET.
func (r *Repository) QueryTraffic(ctx context.Context, q Query) ([]Row, int, error) {
	var (
		table, lastSeen string
		where           []string
		args            []any
	)
	if q.From == "" && q.To == "" {
		table, lastSeen = "device_traffic_total", "last_seen"
	} else {
		table, lastSeen = "device_traffic_daily", "updated_at"
		where = append(where, "day >= ?", "day <= ?")
		args = append(args, q.From, q.To)
	}
	if q.MAC != "" {
		where = append(where, "mac = ?")
		args = append(args, strings.ToLower(q.MAC))
	}
	if q.Iface != "" {
		where = append(where, "iface = ?")
		args = append(args, q.Iface)
	}
	group, ifaceCol := "iface, mac", "iface"
	if q.GroupByMAC {
		group, ifaceCol = "mac", "group_concat(DISTINCT iface)"
	}
	order := "total DESC, mac"
	switch q.Sort {
	case "upload":
		order = "up DESC, mac"
	case "download":
		order = "down DESC, mac"
	case "mac":
		order = "mac"
	}
	if !q.GroupByMAC {
		order += ", iface"
	}
	sqlText := fmt.Sprintf(`
		SELECT %s, mac,
		       SUM(up_bytes) AS up, SUM(up_packets), SUM(down_bytes) AS down, SUM(down_packets),
		       SUM(up_bytes) + SUM(down_bytes) AS total, MAX(%s),
		       COUNT(*) OVER ()
		FROM %s`, ifaceCol, lastSeen, table)
	if len(where) > 0 {
		sqlText += " WHERE " + strings.Join(where, " AND ")
	}
	sqlText += " GROUP BY " + group + " ORDER BY " + order
	if q.Limit > 0 {
		sqlText += " LIMIT ? OFFSET ?"
		args = append(args, q.Limit, q.Offset)
	}

	rows, err := r.db.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("query traffic: %w", err)
	}
	defer rows.Close()
	var (
		out   []Row
		count int
	)
	for rows.Next() {
		var (
			row             Row
			ifaces          sql.NullString
			up, upP, dn, dP int64
			tot             int64
		)
		if err := rows.Scan(&ifaces, &row.MAC, &up, &upP, &dn, &dP, &tot, &row.LastSeen, &count); err != nil {
			return nil, 0, err
		}
		row.Counters = Counters{uint64(up), uint64(upP), uint64(dn), uint64(dP)}
		if q.GroupByMAC {
			row.Ifaces = strings.Split(ifaces.String, ",")
		} else {
			row.Iface = ifaces.String
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if len(out) == 0 && q.Offset > 0 && q.Limit > 0 {
		// COUNT(*) OVER () is only visible when a row comes back.
		if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM (SELECT 1 FROM "+table+
			whereClause(where)+" GROUP BY "+group+")", args[:len(args)-2]...).Scan(&count); err != nil {
			return nil, 0, err
		}
	}
	return out, count, nil
}

func whereClause(where []string) string {
	if len(where) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(where, " AND ")
}

// DayPoint is one day of a series.
type DayPoint struct {
	Day      string   `json:"date"`
	Counters Counters `json:"-"`
}

// Series returns per-day sums between from and to (inclusive), with a zero
// entry for days without traffic. An empty mac sums all devices.
func (r *Repository) Series(ctx context.Context, mac, iface, from, to string) ([]DayPoint, error) {
	where := []string{"day >= ?", "day <= ?"}
	args := []any{from, to}
	if mac != "" {
		where = append(where, "mac = ?")
		args = append(args, strings.ToLower(mac))
	}
	if iface != "" {
		where = append(where, "iface = ?")
		args = append(args, iface)
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT day, SUM(up_bytes), SUM(up_packets), SUM(down_bytes), SUM(down_packets)
		FROM device_traffic_daily`+whereClause(where)+` GROUP BY day ORDER BY day`, args...)
	if err != nil {
		return nil, fmt.Errorf("query series: %w", err)
	}
	defer rows.Close()
	got := map[string]Counters{}
	for rows.Next() {
		var day string
		var a, b, c, d int64
		if err := rows.Scan(&day, &a, &b, &c, &d); err != nil {
			return nil, err
		}
		got[day] = Counters{uint64(a), uint64(b), uint64(c), uint64(d)}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	start, err := time.Parse(time.DateOnly, from)
	if err != nil {
		return nil, err
	}
	end, err := time.Parse(time.DateOnly, to)
	if err != nil {
		return nil, err
	}
	var out []DayPoint
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		day := d.Format(time.DateOnly)
		out = append(out, DayPoint{Day: day, Counters: got[day]})
	}
	return out, nil
}

// PruneDaily deletes daily rows before the given day.
func (r *Repository) PruneDaily(ctx context.Context, before string) (int64, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM device_traffic_daily WHERE day < ?`, before)
	if err != nil {
		return 0, fmt.Errorf("prune daily: %w", err)
	}
	return res.RowsAffected()
}
