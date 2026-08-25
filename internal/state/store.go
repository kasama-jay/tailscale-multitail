// Package state persists non-secret runtime metadata in SQLite.
package state

import (
	"database/sql"
	"fmt"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"time"
)

type Store struct{ db *sql.DB }

func OpenRecovering(root, cidr string) (*Store, bool, error) {
	s, ch, e := Open(root, cidr)
	if e == nil {
		return s, ch, nil
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	for _, x := range []string{"runtime.db", "runtime.db-wal", "runtime.db-shm"} {
		p := filepath.Join(root, x)
		if _, xerr := os.Stat(p); xerr == nil {
			_ = os.Rename(p, p+".corrupt-"+stamp)
		}
	}
	s, _, e2 := Open(root, cidr)
	if e2 != nil {
		return nil, false, fmt.Errorf("runtime database corrupt (%v) and recreation failed: %w", e, e2)
	}
	return s, true, nil
}

func Open(root, cidr string) (*Store, bool, error) {
	if e := os.MkdirAll(root, 0700); e != nil {
		return nil, false, e
	}
	p := filepath.Join(root, "runtime.db")
	db, e := sql.Open("sqlite", p)
	if e != nil {
		return nil, false, e
	}
	s := &Store{db}
	if _, e = db.Exec(`PRAGMA journal_mode=WAL; CREATE TABLE IF NOT EXISTS meta(k TEXT PRIMARY KEY,v TEXT NOT NULL); CREATE TABLE IF NOT EXISTS leases(k TEXT PRIMARY KEY,ip TEXT NOT NULL);`); e != nil {
		db.Close()
		return nil, false, e
	}
	var old string
	e = db.QueryRow(`SELECT v FROM meta WHERE k='cidr'`).Scan(&old)
	changed := e == nil && old != cidr
	if e != nil && e != sql.ErrNoRows {
		db.Close()
		return nil, false, e
	}
	if changed {
		if _, e = db.Exec(`DELETE FROM leases`); e != nil {
			db.Close()
			return nil, false, e
		}
	}
	if _, e = db.Exec(`INSERT INTO meta(k,v) VALUES('cidr',?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`, cidr); e != nil {
		db.Close()
		return nil, false, e
	}
	return s, changed, nil
}
func (s *Store) Leases() (map[string]string, error) {
	r, e := s.db.Query(`SELECT k,ip FROM leases`)
	if e != nil {
		return nil, e
	}
	defer r.Close()
	m := map[string]string{}
	for r.Next() {
		var k, v string
		if e = r.Scan(&k, &v); e != nil {
			return nil, e
		}
		m[k] = v
	}
	return m, r.Err()
}
func (s *Store) Put(k, ip string) error {
	_, e := s.db.Exec(`INSERT INTO leases(k,ip) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET ip=excluded.ip`, k, ip)
	return e
}
func (s *Store) Close() error   { return s.db.Close() }
func (s *Store) String() string { return fmt.Sprintf("%v", s.db != nil) }
