package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"business-brew/internal/auth"
	"business-brew/internal/server"
	"business-brew/internal/store"
)

func main() {
	if err := run(os.Args[1:], os.Stdin); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, passwordInput io.Reader) error {
	if len(args) == 0 {
		return errors.New("usage: business-brew-server <serve|bootstrap|add-admin|reset-password> --organization ID [--database PATH]")
	}
	command := args[0]
	if command != "serve" && command != "bootstrap" && command != "add-admin" && command != "reset-password" {
		return errors.New("expected serve, bootstrap, add-admin, or reset-password")
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	organization := flags.String("organization", "", "Company organization ID (required)")
	database := flags.String("database", "business-brew.db", "SQLite database path")
	username := flags.String("username", "", "Administrator username for operator commands")
	listen := flags.String("listen", "127.0.0.1:8080", "HTTP listen address; use a TLS reverse proxy")
	insecure := flags.Bool("allow-insecure-http", false, "Allow insecure cookies for loopback development only")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if strings.TrimSpace(*organization) == "" {
		return errors.New("--organization is required")
	}
	if command == "serve" && *insecure && !loopback(*listen) {
		return errors.New("--allow-insecure-http requires a loopback listen address")
	}
	db, err := store.Open(*database, *organization)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	if command != "serve" {
		if strings.TrimSpace(*username) == "" {
			return errors.New("--username is required")
		}
		if command == "bootstrap" {
			count, err := db.AdminCount()
			if err != nil {
				return err
			}
			if count != 0 {
				return errors.New("an administrator already exists; use reset-password to recover access")
			}
		}
		passwordBytes, err := io.ReadAll(io.LimitReader(passwordInput, 1026))
		if err != nil {
			return errors.New("unable to read password from stdin")
		}
		password := strings.TrimSuffix(strings.TrimSuffix(string(passwordBytes), "\n"), "\r")
		if len(password) < 12 || len(password) > 1024 {
			return errors.New("password must be 12–1024 bytes; supply it through stdin")
		}
		hash, err := auth.Hash(password)
		if err != nil {
			return errors.New("unable to hash administrator password")
		}
		if command == "bootstrap" || command == "add-admin" {
			err = db.CreateAdmin(*username, hash)
		} else {
			err = db.SetAdminPassword(*username, hash)
		}
		if err != nil {
			return fmt.Errorf("administrator operation failed: %w", err)
		}
		fmt.Fprintln(os.Stdout, "Administrator account updated.")
		return nil
	}
	count, err := db.AdminCount()
	if err != nil {
		return err
	}
	if count == 0 {
		return errors.New("bootstrap a local administrator before starting the service")
	}
	app, err := server.New(db, server.Options{AllowInsecureHTTP: *insecure})
	if err != nil {
		return err
	}
	httpServer := &http.Server{Addr: *listen, Handler: app.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 35 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	log.Printf("Business Brew listening on %s for organization %s", *listen, *organization)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func loopback(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
