package db

import (
	"net/url"
	"testing"

	"arena-server/internal/config"
)

func TestConnectionStringEscapesCredentials(t *testing.T) {
	cfg := config.Config{
		DBHost: "db.internal", DBPort: 5432, DBName: "arena",
		DBUser: "arena", DBPassword: "pa/ss w#rd?x@y:z", DBSSLMode: "require",
	}
	dsn := connectionString(cfg)
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("connection string does not parse: %v (%q)", err, dsn)
	}
	if got := parsed.User.Username(); got != "arena" {
		t.Fatalf("user = %q", got)
	}
	if got, _ := parsed.User.Password(); got != cfg.DBPassword {
		t.Fatalf("password round-trip = %q", got)
	}
	if parsed.Hostname() != "db.internal" || parsed.Port() != "5432" || parsed.Path != "/arena" {
		t.Fatalf("host/path = %q %q %q", parsed.Hostname(), parsed.Port(), parsed.Path)
	}
	if parsed.Query().Get("sslmode") != "require" {
		t.Fatalf("sslmode = %q", parsed.Query().Get("sslmode"))
	}
}
