// Command xellter is a small, fast LLM gateway/router in front of kenari.id.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/axellageraldinc/xellter/internal/admin"
	"github.com/axellageraldinc/xellter/internal/app"
	"github.com/axellageraldinc/xellter/internal/auth"
	"github.com/axellageraldinc/xellter/internal/config"
	"github.com/axellageraldinc/xellter/internal/proxy"
)

const usage = `xellter - LLM gateway for kenari.id

Usage:
  xellter serve        [-config xellter.yaml]
  xellter check        [-config xellter.yaml]        validate the config
  xellter admin init   [-config f] [-username u] [-from-record file] [-credentials-out file]
  xellter client add   [-config f] -name N [-shadows a,b] [-passthrough glob,glob] [-upstreams u1,u2] [-budget IDR] [-out file]
  xellter version
`

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = serve(os.Args[2:])
	case "check":
		err = check(os.Args[2:])
	case "admin":
		err = adminCmd(os.Args[2:])
	case "client":
		err = clientCmd(os.Args[2:])
	case "version":
		fmt.Println("xellter", app.Version)
	default:
		fmt.Print(usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func cfgPath(fs *flag.FlagSet) *string {
	def := os.Getenv("XELLTER_CONFIG")
	if def == "" {
		def = "xellter.yaml"
	}
	return fs.String("config", def, "path to the YAML config")
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	path := cfgPath(fs)
	_ = fs.Parse(args)
	abs, _ := filepath.Abs(*path)
	a, err := app.New(abs, nil)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	a.Start(ctx)

	mux := http.NewServeMux()
	mux.Handle("/admin/", admin.New(a))
	mux.HandleFunc("/admin", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/admin/", http.StatusFound) })
	mux.Handle("/", a.Px)
	srv := &http.Server{Addr: a.Cfg().Server.Listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second}
	log.Println(a.Describe())
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	sd, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_ = srv.Shutdown(sd)
	return a.Close()
}

func check(args []string) error {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	path := cfgPath(fs)
	_ = fs.Parse(args)
	c, err := config.Load(*path)
	if err != nil {
		return err
	}
	fmt.Printf("ok: %d upstreams, %d clients, %d shadows\n", len(c.Upstreams), len(c.Clients), len(c.Shadows))
	return nil
}

func adminCmd(args []string) error {
	if len(args) == 0 || args[0] != "init" {
		return errors.New("usage: xellter admin init ...")
	}
	fs := flag.NewFlagSet("admin init", flag.ExitOnError)
	path := cfgPath(fs)
	user := fs.String("username", "admin", "admin username")
	from := fs.String("from-record", "", "reuse an existing record (for example aixell's auth-record.json)")
	out := fs.String("credentials-out", "", "write the generated credentials to this file (mode 0600) instead of stdout")
	_ = fs.Parse(args[1:])
	c, err := config.Load(*path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.Server.DataDir, 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(c.Server.AuthRecord); err == nil {
		return fmt.Errorf("%s already exists; remove it first to re-initialise", c.Server.AuthRecord)
	}
	if *from != "" {
		rec, err := auth.ReadRecord(*from)
		if err != nil {
			return err
		}
		if err := auth.WriteRecord(c.Server.AuthRecord, *rec); err != nil {
			return err
		}
		fmt.Printf("imported login for user %q (same password and authenticator as the source)\n", rec.Username)
		return nil
	}
	pw := auth.RandomPassword()
	rec, recovery, err := auth.NewRecord(*user, pw)
	if err != nil {
		return err
	}
	if err := auth.WriteRecord(c.Server.AuthRecord, rec); err != nil {
		return err
	}
	msg := fmt.Sprintf("xellter admin credentials (shown once)\nusername: %s\npassword: %s\nTOTP secret (base32): %s\notpauth URI: %s\nrecovery codes (single use):\n  %s\n",
		rec.Username, pw, rec.TotpSecret, auth.OTPAuthURI(rec.Username, rec.TotpSecret), strings.Join(recovery, "\n  "))
	if *out != "" {
		if err := os.WriteFile(*out, []byte(msg), 0o600); err != nil {
			return err
		}
		fmt.Println("credentials written to", *out, "(read it once, then delete it)")
		return nil
	}
	fmt.Print(msg)
	return nil
}

func clientCmd(args []string) error {
	if len(args) == 0 || args[0] != "add" {
		return errors.New("usage: xellter client add -name N ...")
	}
	fs := flag.NewFlagSet("client add", flag.ExitOnError)
	path := cfgPath(fs)
	name := fs.String("name", "", "client name")
	shadows := fs.String("shadows", "*", "comma separated shadow models (or *)")
	pass := fs.String("passthrough", "", "comma separated real-model globs the client may call directly")
	ups := fs.String("upstreams", "", "comma separated upstream names (empty = all)")
	budget := fs.Float64("budget", 0, "daily budget in IDR (0 = unlimited)")
	out := fs.String("out", "", "write the API key to this file (mode 0600) instead of stdout")
	_ = fs.Parse(args[1:])
	c, err := config.Load(*path)
	if err != nil {
		return err
	}
	key := "xl-" + auth.RandomHex(24)
	cl := config.Client{Name: *name, Enabled: true, KeyHash: proxy.HashKey(key), KeyHint: "…" + key[len(key)-4:], Shadows: split(*shadows), Passthrough: split(*pass), Upstreams: split(*ups), DailyBudget: *budget}
	c.Clients = append(c.Clients, cl)
	if err := config.Save(*path, c); err != nil {
		return err
	}
	if *out != "" {
		if err := os.WriteFile(*out, []byte(key+"\n"), 0o600); err != nil {
			return err
		}
		fmt.Printf("client %q added; key written to %s\n", *name, *out)
		return nil
	}
	fmt.Printf("client %q added; API key (shown once): %s\n", *name, key)
	return nil
}

func split(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
