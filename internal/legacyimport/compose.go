package legacyimport

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Discovered is what DiscoverCompose found: how to reach the legacy Postgres from this host.
type Discovered struct {
	// Layout is "installer" (jarvis-admin's ~/.jarvis/compose: docker-compose.yml + .env) or
	// "source" (a ./jarvis source checkout: jarvis-data-services/.env + ~/.jarvis/databases.env).
	Layout string
	// Files are the env files the credentials and database names came from.
	Files []string
	PG    PGConfig
	Notes []string
}

// DiscoverCompose finds the legacy Postgres credentials in a compose directory. It accepts:
//
//   - the installer's compose directory (jarvis-admin, default ~/.jarvis/compose): .env holds
//     POSTGRES_PASSWORD, DB_USER (the role every service's DATABASE_URL uses; the container's
//     POSTGRES_USER defaults to the same "jarvis") and POSTGRES_PORT, published on
//     JARVIS_INFRA_BIND_HOST (default 127.0.0.1);
//   - a ./jarvis source checkout (or its jarvis-data-services directory): jarvis-data-services/.env
//     holds POSTGRES_USER/POSTGRES_PASSWORD/POSTGRES_PORT, published on every interface, and
//     the database names live in ~/.jarvis/databases.env (DB_NAME_*), given as databasesEnv.
//
// A compose-export file (one docker-compose.yml with every value inline, no .env) is not
// parsed: pass the URL instead. Values are never printed.
func DiscoverCompose(dir, databasesEnv string) (Discovered, error) {
	fi, err := os.Stat(dir)
	if err != nil {
		return Discovered{}, fmt.Errorf("compose directory: %w", err)
	}
	if !fi.IsDir() {
		return Discovered{}, fmt.Errorf("%s is not a directory", dir)
	}
	// A source checkout given by its root.
	if ds := filepath.Join(dir, "jarvis-data-services"); isFile(filepath.Join(ds, ".env")) {
		return discoverSource(ds, databasesEnv)
	}
	envPath := filepath.Join(dir, ".env")
	env, err := readDotenv(envPath)
	if errors.Is(err, fs.ErrNotExist) {
		if isFile(filepath.Join(dir, "docker-compose.yml")) || isFile(filepath.Join(dir, "docker-compose.yaml")) {
			return Discovered{}, fmt.Errorf("%s has a docker-compose file but no .env (a compose export keeps its settings inline): "+
				"pass --from postgres://USER:PASSWORD@127.0.0.1:PORT with the values from its postgres service", dir)
		}
		return Discovered{}, fmt.Errorf("%s holds no legacy compose files (.env, docker-compose.yml, jarvis-data-services/.env)", dir)
	}
	if err != nil {
		return Discovered{}, err
	}
	if _, ok := env["DB_USER"]; !ok {
		if _, ok := env["POSTGRES_USER"]; ok {
			return discoverSource(dir, databasesEnv) // jarvis-data-services itself
		}
	}
	if env["POSTGRES_PASSWORD"] == "" {
		return Discovered{}, fmt.Errorf("%s has no POSTGRES_PASSWORD: is this the legacy compose directory?", envPath)
	}
	d := Discovered{Layout: "installer", Files: []string{envPath}}
	user := first(env["DB_USER"], env["POSTGRES_USER"], "jarvis")
	if env["DB_USER"] != "" && env["POSTGRES_USER"] != "" && env["DB_USER"] != env["POSTGRES_USER"] {
		d.Notes = append(d.Notes, "DB_USER and POSTGRES_USER differ; using DB_USER (the role the services connect as)")
	}
	port, err := portOf(env["POSTGRES_PORT"], envPath)
	if err != nil {
		return Discovered{}, err
	}
	host := env["JARVIS_INFRA_BIND_HOST"]
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		host = "127.0.0.1"
	}
	d.PG = PGConfig{Host: host, Port: port, User: user, Password: env["POSTGRES_PASSWORD"], DBNames: dbNames(env)}
	if !isFile(filepath.Join(dir, "docker-compose.yml")) && !isFile(filepath.Join(dir, "docker-compose.yaml")) {
		d.Notes = append(d.Notes, "no docker-compose.yml next to .env")
	}
	return d, nil
}

func discoverSource(ds, databasesEnv string) (Discovered, error) {
	envPath := filepath.Join(ds, ".env")
	env, err := readDotenv(envPath)
	if err != nil {
		return Discovered{}, err
	}
	port, err := portOf(env["POSTGRES_PORT"], envPath)
	if err != nil {
		return Discovered{}, err
	}
	d := Discovered{Layout: "source", Files: []string{envPath}}
	d.PG = PGConfig{Host: "127.0.0.1", Port: port, User: first(env["POSTGRES_USER"], "postgres"),
		Password: env["POSTGRES_PASSWORD"], DBNames: dbNames(env)}
	if databasesEnv != "" {
		names, err := readDotenv(databasesEnv)
		switch {
		case err == nil:
			d.Files = append(d.Files, databasesEnv)
			for k, v := range dbNames(names) {
				d.PG.DBNames[k] = v
			}
		case errors.Is(err, fs.ErrNotExist):
			d.Notes = append(d.Notes, databasesEnv+" not found: default database names")
		default:
			d.Notes = append(d.Notes, fmt.Sprintf("%s unreadable (%v): default database names", databasesEnv, err))
		}
	}
	return d, nil
}

// dbNames picks the DB_NAME_* overrides out of an env file.
func dbNames(env map[string]string) map[string]string {
	m := map[string]string{}
	for _, d := range LegacyDBs {
		if v := env[d.EnvName]; v != "" {
			m[d.Key] = v
		}
	}
	return m
}

func portOf(v, file string) (int, error) {
	if v == "" {
		return 5432, nil
	}
	p, err := strconv.Atoi(v)
	if err != nil || p <= 0 || p > 65535 {
		return 0, fmt.Errorf("%s: POSTGRES_PORT is not a port", file)
	}
	return p, nil
}

func first(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

// readDotenv parses KEY=VALUE lines the way docker compose reads an env file: comments,
// blank lines and an "export " prefix are ignored, quotes are stripped, an unquoted value
// ends at " #". ${…} references are kept literally.
func readDotenv(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	env := map[string]string{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		switch {
		case len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && strings.IndexByte(v[1:], v[0]) >= 0:
			v = v[1 : 1+strings.IndexByte(v[1:], v[0])]
		default:
			if i := strings.Index(v, " #"); i >= 0 {
				v = strings.TrimSpace(v[:i])
			}
		}
		env[k] = v
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return env, nil
}
