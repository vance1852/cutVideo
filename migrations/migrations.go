// Package migrations embeds the versioned SQL schema of the cutVideo service so
// a server binary can build the database from empty without extra files.
package migrations

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

//go:embed *.sql
var files embed.FS

// Migration is one forward-only schema step.
type Migration struct {
	Version int
	Name    string
	SQL     string
}

// All returns every embedded migration ordered by version.
func All() ([]Migration, error) {
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		return nil, fmt.Errorf("migrations: read embedded directory: %w", err)
	}
	out := make([]Migration, 0, len(entries))
	seen := map[int]string{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version, name, err := parseName(entry.Name())
		if err != nil {
			return nil, err
		}
		if previous, exists := seen[version]; exists {
			return nil, fmt.Errorf("migrations: version %d is declared by both %s and %s", version, previous, entry.Name())
		}
		seen[version] = entry.Name()
		body, err := files.ReadFile(entry.Name())
		if err != nil {
			return nil, fmt.Errorf("migrations: read %s: %w", entry.Name(), err)
		}
		out = append(out, Migration{Version: version, Name: name, SQL: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	if len(out) == 0 {
		return nil, fmt.Errorf("migrations: no embedded migration found")
	}
	return out, nil
}

// Latest reports the highest embedded schema version.
func Latest() (int, error) {
	all, err := All()
	if err != nil {
		return 0, err
	}
	return all[len(all)-1].Version, nil
}

func parseName(filename string) (int, string, error) {
	trimmed := strings.TrimSuffix(filename, ".sql")
	parts := strings.SplitN(trimmed, "_", 2)
	if len(parts) != 2 {
		return 0, "", fmt.Errorf("migrations: %s must be named <version>_<name>.sql", filename)
	}
	version, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, "", fmt.Errorf("migrations: %s has a non numeric version: %w", filename, err)
	}
	if version < 1 {
		return 0, "", fmt.Errorf("migrations: %s must use a positive version", filename)
	}
	return version, parts[1], nil
}
