// Package store keeps the tunnel definitions in a SQLite database at the
// project root, so tunnels can be added, edited and removed from the UI
// instead of being edited into internal/config and recompiled.
//
// Only what identifies a tunnel lives here: the SSM target, the host and
// ports, and the *name* of the AWS CLI profile it runs as. The profiles
// themselves stay in ~/.aws/config, where the `aws` CLI reads them — and no
// credential or token is ever written to this file.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"

	"scriptstui/internal/config"
)

// FileName is the database's name; it sits at the project root.
const FileName = "scriptstui.db"

// schemaVersion is written to PRAGMA user_version. Bump it and add a case to
// migrate() when the schema changes.
const schemaVersion = 1

const schema = `
CREATE TABLE IF NOT EXISTS tunnels (
  id                        TEXT PRIMARY KEY,
  proyecto                  TEXT NOT NULL DEFAULT '',
  title                     TEXT NOT NULL,
  target                    TEXT NOT NULL,
  host                      TEXT NOT NULL,
  remote_port               INTEGER NOT NULL,
  local_port                INTEGER NOT NULL,
  region                    TEXT NOT NULL DEFAULT 'us-east-1',
  document_name             TEXT NOT NULL DEFAULT 'AWS-StartPortForwardingSessionToRemoteHost',
  profile                   TEXT NOT NULL DEFAULT '',
  connection_string_mac     TEXT NOT NULL DEFAULT '',
  connection_string_windows TEXT NOT NULL DEFAULT '',
  wait_seconds              INTEGER NOT NULL DEFAULT 0,
  enabled                   INTEGER NOT NULL DEFAULT 1,
  sort_order                INTEGER NOT NULL DEFAULT 0,
  created_at                TEXT NOT NULL DEFAULT (datetime('now')),
  updated_at                TEXT NOT NULL DEFAULT (datetime('now'))
);

-- Two live tunnels on the same local port would silently fight over it, and
-- today that is only catchable by reading the source. Disabled rows are
-- exempt so a port can be freed by turning a tunnel off instead of deleting it.
CREATE UNIQUE INDEX IF NOT EXISTS tunnels_local_port
  ON tunnels(local_port) WHERE enabled = 1;

CREATE TABLE IF NOT EXISTS settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
`

// Tunnel is one row of the tunnels table.
type Tunnel struct {
	ID           string
	Proyecto     string
	Title        string
	Target       string
	Host         string
	RemotePort   int
	LocalPort    int
	Region       string
	DocumentName string
	// Profile is the name of an AWS CLI profile in ~/.aws/config. Empty means
	// "use ambient or pasted credentials".
	Profile       string
	ConnStringMac string
	ConnStringWin string
	WaitSeconds   int
	Enabled       bool
	SortOrder     int
}

// Service turns a row into the config.Service the rest of the app already
// runs, so procman and both front ends stay unaware of where it came from.
func (t Tunnel) Service() config.Service {
	step := config.NewTunnelStep(config.TunnelParams{
		Label:        "AWS",
		Target:       t.Target,
		Host:         t.Host,
		RemotePort:   t.RemotePort,
		LocalPort:    t.LocalPort,
		Region:       t.Region,
		DocumentName: t.DocumentName,
		WaitSeconds:  t.WaitSeconds,
	})
	return config.Service{ID: t.ID, Title: t.Title, Steps: []config.Step{step}}
}

// CommandLine is the tunnel's `aws ssm start-session` line exactly as it
// would run, profile included — the same text the UI copies with 'y'. It is
// what the connection-string columns hold when they are meant to let someone
// bring the tunnel up by hand, outside the app.
func (t Tunnel) CommandLine() string {
	step := t.Service().Steps[0]
	step.Profile = t.Profile
	return step.CommandLine()
}

// syncGeneratedCommand keeps a connection string that is just the tunnel's own
// SSM command line in step with the tunnel it describes: editing a port or
// reassigning a profile would otherwise leave behind a command that brings up
// the wrong tunnel. A string that no longer matches what the stored row
// generates was written by hand, and is left exactly as it is.
func (s *Store) syncGeneratedCommand(next Tunnel) Tunnel {
	prev, err := s.Get(next.ID)
	if err != nil {
		return next
	}
	was := prev.CommandLine()
	now := next.CommandLine()
	if next.ConnStringMac == was {
		next.ConnStringMac = now
	}
	if next.ConnStringWin == was {
		next.ConnStringWin = now
	}
	return next
}

// Store is the open database.
type Store struct {
	db   *sql.DB
	path string
}

// DefaultPath locates the database at the project root: the directory holding
// go.mod, looked up from the working directory and then from the binary's own
// location, so `go run .` and a built ./scriptstui find the same file.
// SCRIPTSTUI_DB overrides all of it.
func DefaultPath() string {
	if p := strings.TrimSpace(os.Getenv("SCRIPTSTUI_DB")); p != "" {
		return p
	}
	if wd, err := os.Getwd(); err == nil {
		if root := findProjectRoot(wd); root != "" {
			return filepath.Join(root, FileName)
		}
	}
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		if root := findProjectRoot(exeDir); root != "" {
			return filepath.Join(root, FileName)
		}
		return filepath.Join(exeDir, FileName)
	}
	return FileName
}

// findProjectRoot walks up from dir looking for go.mod, returning "" when it
// reaches the filesystem root without finding one.
func findProjectRoot(dir string) string {
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// Open opens (creating it if needed) the database at path and applies the
// schema. WAL is on because the TUI and the desktop app can both be running
// against this one file at the same time.
func Open(path string) (*Store, error) {
	dsn := "file:" + path +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(1)"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("abriendo %s: %w", path, err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("abriendo %s: %w", path, err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("creando el esquema: %w", err)
	}
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		db.Close()
		return nil, fmt.Errorf("marcando la versión del esquema: %w", err)
	}
	return &Store{db: db, path: path}, nil
}

// Path is where this store lives on disk, for showing in the UI.
func (s *Store) Path() string { return s.path }

func (s *Store) Close() error { return s.db.Close() }

const selectColumns = `
  id, proyecto, title, target, host, remote_port, local_port, region,
  document_name, profile, connection_string_mac, connection_string_windows,
  wait_seconds, enabled, sort_order`

// List returns every tunnel, disabled ones included, in display order.
func (s *Store) List() ([]Tunnel, error) {
	rows, err := s.db.Query(`SELECT` + selectColumns + `
		FROM tunnels ORDER BY sort_order, proyecto, title, id`)
	if err != nil {
		return nil, fmt.Errorf("leyendo túneles: %w", err)
	}
	defer rows.Close()

	var out []Tunnel
	for rows.Next() {
		var t Tunnel
		if err := rows.Scan(&t.ID, &t.Proyecto, &t.Title, &t.Target, &t.Host,
			&t.RemotePort, &t.LocalPort, &t.Region, &t.DocumentName, &t.Profile,
			&t.ConnStringMac, &t.ConnStringWin, &t.WaitSeconds, &t.Enabled,
			&t.SortOrder); err != nil {
			return nil, fmt.Errorf("leyendo túneles: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Get returns one tunnel by id.
func (s *Store) Get(id string) (Tunnel, error) {
	var t Tunnel
	err := s.db.QueryRow(`SELECT`+selectColumns+` FROM tunnels WHERE id=?`, id).
		Scan(&t.ID, &t.Proyecto, &t.Title, &t.Target, &t.Host, &t.RemotePort,
			&t.LocalPort, &t.Region, &t.DocumentName, &t.Profile,
			&t.ConnStringMac, &t.ConnStringWin, &t.WaitSeconds, &t.Enabled,
			&t.SortOrder)
	if errors.Is(err, sql.ErrNoRows) {
		return Tunnel{}, ErrNotFound
	}
	if err != nil {
		return Tunnel{}, fmt.Errorf("leyendo el túnel %s: %w", id, err)
	}
	return t, nil
}

// Enabled returns only the tunnels that should show up in the list.
func (s *Store) Enabled() ([]Tunnel, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	out := make([]Tunnel, 0, len(all))
	for _, t := range all {
		if t.Enabled {
			out = append(out, t)
		}
	}
	return out, nil
}

// ErrNotFound is returned when an id doesn't match any row.
var ErrNotFound = errors.New("el túnel no existe")

// Create inserts a new tunnel, validating it first.
func (s *Store) Create(t Tunnel) error {
	t = t.normalized()
	if err := t.Validate(); err != nil {
		return err
	}
	if t.SortOrder == 0 {
		if err := s.db.QueryRow(
			`SELECT COALESCE(MAX(sort_order), 0) + 1 FROM tunnels`).Scan(&t.SortOrder); err != nil {
			return fmt.Errorf("calculando el orden: %w", err)
		}
	}
	_, err := s.db.Exec(`
		INSERT INTO tunnels (id, proyecto, title, target, host, remote_port,
			local_port, region, document_name, profile, connection_string_mac,
			connection_string_windows, wait_seconds, enabled, sort_order)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.Proyecto, t.Title, t.Target, t.Host, t.RemotePort, t.LocalPort,
		t.Region, t.DocumentName, t.Profile, t.ConnStringMac, t.ConnStringWin,
		t.WaitSeconds, t.Enabled, t.SortOrder)
	return friendlyErr(err, t)
}

// Update rewrites an existing tunnel in place. created_at is left alone.
func (s *Store) Update(t Tunnel) error {
	t = t.normalized()
	if err := t.Validate(); err != nil {
		return err
	}
	t = s.syncGeneratedCommand(t)
	res, err := s.db.Exec(`
		UPDATE tunnels SET proyecto=?, title=?, target=?, host=?, remote_port=?,
			local_port=?, region=?, document_name=?, profile=?,
			connection_string_mac=?, connection_string_windows=?, wait_seconds=?,
			enabled=?, sort_order=?, updated_at=datetime('now')
		WHERE id=?`,
		t.Proyecto, t.Title, t.Target, t.Host, t.RemotePort, t.LocalPort,
		t.Region, t.DocumentName, t.Profile, t.ConnStringMac, t.ConnStringWin,
		t.WaitSeconds, t.Enabled, t.SortOrder, t.ID)
	if err != nil {
		return friendlyErr(err, t)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes a tunnel for good.
func (s *Store) Delete(id string) error {
	res, err := s.db.Exec(`DELETE FROM tunnels WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("eliminando el túnel: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetProfile records which AWS CLI profile a tunnel runs as. Passing ""
// clears it, falling back to ambient or pasted credentials.
func (s *Store) SetProfile(id, profile string) error {
	row, err := s.Get(id)
	if err != nil {
		return err
	}
	row.Profile = strings.TrimSpace(profile)
	row = s.syncGeneratedCommand(row)

	res, err := s.db.Exec(`
		UPDATE tunnels SET profile=?, connection_string_mac=?,
			connection_string_windows=?, updated_at=datetime('now')
		WHERE id=?`,
		row.Profile, row.ConnStringMac, row.ConnStringWin, id)
	if err != nil {
		return fmt.Errorf("guardando el perfil: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Count reports how many tunnels are stored, so callers can tell an empty
// database from a populated one.
func (s *Store) Count() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM tunnels`).Scan(&n)
	return n, err
}

// Seed fills an empty database from the literals in internal/config, carrying
// over any profile assignments from the old JSON prefs file. It is a no-op on
// a database that already has tunnels, so it can run on every startup.
func (s *Store) Seed(services []config.Service, profiles map[string]string) (int, error) {
	n, err := s.Count()
	if err != nil {
		return 0, err
	}
	if n > 0 {
		return 0, nil
	}

	inserted := 0
	for i, svc := range services {
		for _, step := range svc.Steps {
			params, ok := config.TunnelParamsOf(step)
			if !ok {
				continue
			}
			t := Tunnel{
				ID:           svc.ID,
				Title:        svc.Title,
				Target:       params.Target,
				Host:         params.Host,
				RemotePort:   params.RemotePort,
				LocalPort:    params.LocalPort,
				Region:       params.Region,
				DocumentName: params.DocumentName,
				Profile:      profiles[svc.ID],
				WaitSeconds:  params.WaitSeconds,
				Enabled:      true,
				SortOrder:    i + 1,
			}
			if err := s.Create(t); err != nil {
				return inserted, fmt.Errorf("sembrando %s: %w", svc.ID, err)
			}
			inserted++
			break // one tunnel step per service, which is all the literals have
		}
	}
	return inserted, nil
}

// normalized trims the free-text fields and fills in the defaults, so the
// same value is stored whether it came from a form or from the seed.
func (t Tunnel) normalized() Tunnel {
	t.ID = strings.TrimSpace(t.ID)
	t.Proyecto = strings.TrimSpace(t.Proyecto)
	t.Title = strings.TrimSpace(t.Title)
	t.Target = strings.TrimSpace(t.Target)
	t.Host = strings.TrimSpace(t.Host)
	t.Region = strings.TrimSpace(t.Region)
	t.DocumentName = strings.TrimSpace(t.DocumentName)
	t.Profile = strings.TrimSpace(t.Profile)
	t.ConnStringMac = strings.TrimSpace(t.ConnStringMac)
	t.ConnStringWin = strings.TrimSpace(t.ConnStringWin)
	if t.Region == "" {
		t.Region = "us-east-1"
	}
	if t.DocumentName == "" {
		t.DocumentName = config.SSMPortForwardDocument
	}
	return t
}

// Validate reports the first problem with a tunnel, worded for the UI to show
// as-is.
func (t Tunnel) Validate() error {
	switch {
	case t.ID == "":
		return errors.New("el id no puede estar vacío")
	case strings.ContainsAny(t.ID, " \t"):
		return errors.New("el id no puede tener espacios (ej. db-balu)")
	case t.Title == "":
		return errors.New("el nombre no puede estar vacío")
	case t.Target == "":
		return errors.New("falta el target (la instancia EC2, ej. i-074e88b2ee9d1d67f)")
	case t.Host == "":
		return errors.New("falta el host remoto (ej. mi-db.us-east-1.rds.amazonaws.com)")
	case t.RemotePort < 1 || t.RemotePort > 65535:
		return errors.New("el puerto remoto debe estar entre 1 y 65535")
	case t.LocalPort < 1 || t.LocalPort > 65535:
		return errors.New("el puerto local debe estar entre 1 y 65535")
	}
	return nil
}

// friendlyErr turns SQLite's constraint messages into something a user can
// act on, since the UI shows them verbatim.
func friendlyErr(err error, t Tunnel) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "tunnels.id"), strings.Contains(msg, "PRIMARY KEY"):
		return fmt.Errorf("ya existe un túnel con el id %q", t.ID)
	case strings.Contains(msg, "tunnels.local_port"), strings.Contains(msg, "tunnels_local_port"):
		return fmt.Errorf("el puerto local %d ya lo usa otro túnel activo", t.LocalPort)
	}
	return fmt.Errorf("guardando el túnel: %w", err)
}
