// Command xellter is a small, fast LLM gateway/router in front of kenari.id.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/axellageraldinc/xellter/internal/admin"
	"github.com/axellageraldinc/xellter/internal/app"
	"github.com/axellageraldinc/xellter/internal/auth"
	"github.com/axellageraldinc/xellter/internal/config"
	"github.com/axellageraldinc/xellter/internal/proxy"
	"github.com/axellageraldinc/xellter/internal/rank"
	"github.com/axellageraldinc/xellter/internal/store"
)

const usage = `xellter - LLM gateway for kenari.id

Usage:
  xellter serve        [-config xellter.yaml]
  xellter check        [-config xellter.yaml]        validate the config
  xellter migrate      [-config f] [-write]           convert a pre-rewrite config to the current shape
  xellter explain      [-config f] [-preset name]    print the current ranking and why (fetches the catalog, probes keys)
  xellter admin init   [-config f] [-username u] [-from-record file] [-credentials-out file]
  xellter client add   [-config f] -name N [-presets a,b] [-passthrough glob,glob] [-upstreams u1,u2] [-budget IDR] [-out file]
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
	case "migrate":
		err = migrateCmd(os.Args[2:])
	case "explain":
		err = explain(os.Args[2:])
	case "admin":
		err = adminCmd(os.Args[2:])
	case "client":
		err = clientCmd(os.Args[2:])
	case "key":
		err = keyCmd(os.Args[2:])
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
	// The panel is the site: it answers everything that is not the API. The API
	// and the proxy are mounted on it, so one service serves the panel and
	// /v1 (ADR 0023).
	panel := admin.New(a)
	mux.Handle("/api/", panel)
	mux.HandleFunc("/api", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/api/", http.StatusFound) })
	mux.Handle("/v1/", a.Px)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { a.Px.ServeHTTP(w, r) })
	mux.Handle("/", panel)
	srv := &http.Server{Addr: a.Cfg().Server.Listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 15 * time.Minute}
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
	keys, presets := 0, 0
	for _, cl := range c.Clients {
		keys += len(cl.Keys)
		presets += len(cl.Presets)
	}
	fmt.Printf("ok: %d clients, %d kenari keys, %d presets\n", len(c.Clients), keys, presets)
	return nil
}

func explain(args []string) error {
	fs := flag.NewFlagSet("explain", flag.ExitOnError)
	path := cfgPath(fs)
	onlyClient := fs.String("client", "", "only this client")
	only := fs.String("preset", "", "only this preset")
	_ = fs.Parse(args)
	abs, _ := filepath.Abs(*path)
	a, err := app.New(abs, nil)
	if err != nil {
		return err
	}
	defer a.Close()
	a.Logf = func(string, ...any) {}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := a.Cat.Refresh(ctx); err != nil {
		return fmt.Errorf("catalog: %w", err)
	}
	if time.Since(a.BenchFetched()) > 24*time.Hour {
		if err := a.SyncBench(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "leaderboard sync failed:", err)
		}
	}
	a.ProbeKeys(ctx)
	names := make([]string, 0)
	for n, r := range a.Rt.Rankings() {
		if (*only == "" || *only == r.Preset) && (*onlyClient == "" || *onlyClient == r.Client) {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		r := a.Rt.Rankings()[n]
		// The pool is everything the preset may route over: the list, the reserve
		// behind it, and the models that were left out with a reason.
		pool := len(r.Candidates) + len(r.Reserve) + len(r.Rejected)
		fmt.Printf("\n== %s / %s (%s, %s, purpose %s)  models in the pool: %d  routed against: %d\n",
			r.Client, r.Preset, r.Category, r.Optimization, r.Purpose, pool, len(r.Candidates))
		for i, c := range r.Candidates {
			fmt.Printf("%2d. %-34s %-8s Rp%-9.2f quality %.2f (%s)  %s\n", i+1, c.Model, c.Billing, c.CostIDR, c.Bench, c.BenchSource, c.Reason)
		}
		if len(r.Candidates) == 0 {
			fmt.Println("    (no usable model)")
		}
		for _, x := range r.Rejected {
			fmt.Printf("    x %-32s %s\n", x.Model, x.Why)
		}
		if len(r.Reserve) > 0 {
			fmt.Printf("    (%d models wait in the reserve: %s)\n", len(r.Reserve), reserveNames(r.Reserve))
		}
		if len(r.Unranked) > 0 {
			fmt.Printf("    (%d models have no quality evidence: %s)\n", len(r.Unranked), strings.Join(r.Unranked, ", "))
		}
	}
	return nil
}

// reserveNames lists the models that wait in the reserve.
func reserveNames(list []rank.Candidate) string {
	out := make([]string, 0, len(list))
	for _, c := range list {
		out = append(out, c.Model)
	}
	return strings.Join(out, ", ")
}

// migrateCmd converts a pre-rewrite configuration file. It prints the result unless -write is given, and it
// never overwrites the original without keeping a copy next to it.
func migrateCmd(args []string) error {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	path := cfgPath(fs)
	write := fs.Bool("write", false, "replace the config file (the old one is kept as <file>.pre-rewrite)")
	_ = fs.Parse(args)
	data, err := os.ReadFile(*path)
	if err != nil {
		return err
	}
	c, notes, err := config.Migrate(data)
	if err != nil {
		return err
	}
	fmt.Println("# migration notes")
	fmt.Println(notes)
	if !*write {
		tmp, err := os.CreateTemp("", "xellter-migrate-*.yaml")
		if err != nil {
			return err
		}
		defer os.Remove(tmp.Name())
		tmp.Close()
		if err := config.Save(tmp.Name(), c); err != nil {
			return err
		}
		out, err := os.ReadFile(tmp.Name())
		if err != nil {
			return err
		}
		fmt.Print(string(out))
		return nil
	}
	if err := os.WriteFile(*path+".pre-rewrite", data, 0o600); err != nil {
		return err
	}
	if err := config.Save(*path, c); err != nil {
		return err
	}
	fmt.Printf("wrote %s (old file kept as %s.pre-rewrite)\n", *path, *path)
	return nil
}

func adminCmd(args []string) error {
	if len(args) == 0 || args[0] != "init" {
		return errors.New("usage: xellter admin init ...")
	}
	fs := flag.NewFlagSet("admin init", flag.ExitOnError)
	path := cfgPath(fs)
	user := fs.String("username", "admin", "admin username")
	from := fs.String("from-record", "", "reuse an existing record (for example, one produced by another app)")
	out := fs.String("credentials-out", "", "write the generated credentials to this file (mode 0600) instead of stdout")
	force := fs.Bool("force", false, "replace an existing login: new password, new authenticator secret, all sessions ended")
	_ = fs.Parse(args[1:])
	c, err := config.Load(*path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.Server.DataDir, 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(c.Server.AuthRecord); err == nil {
		if !*force {
			return fmt.Errorf("%s already exists; pass -force to replace it", c.Server.AuthRecord)
		}
		for _, f := range []string{"login-state.json", "recovery-used.json"} {
			_ = os.Remove(filepath.Join(c.Server.DataDir, f))
		}
		if st, err := store.Open(c.Server.DataDir); err == nil {
			st.DeleteAllSessions() // old logins must not survive a credential reset
			_ = st.Close()
		}
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
	purpose := fs.String("purpose", "general", "client purpose: general, coding, agentic, vision or bulk")
	out := fs.String("out", "", "write the API key to this file (mode 0600) instead of stdout")
	_ = fs.Parse(args[1:])
	c, err := config.Load(*path)
	if err != nil {
		return err
	}
	key := "xl-" + auth.RandomHex(24)
	c.Clients = append(c.Clients, config.Client{Name: *name, Enabled: true, Purpose: *purpose, KeyHash: proxy.HashKey(key), KeyHint: "…" + key[len(key)-4:], Presets: map[string]config.Preset{}})
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

func keyCmd(args []string) error {
	if len(args) == 0 || args[0] != "add" {
		return errors.New("usage: xellter key add -client C -name K  (key on stdin)")
	}
	fs := flag.NewFlagSet("key add", flag.ExitOnError)
	path := cfgPath(fs)
	client := fs.String("client", "", "client name")
	name := fs.String("name", "", "key name")
	_ = fs.Parse(args[1:])
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, 4096))
	if err != nil {
		return err
	}
	value := strings.TrimSpace(string(raw))
	if value == "" || *client == "" || *name == "" {
		return errors.New("need -client, -name and the key on stdin")
	}
	c, err := config.Load(*path)
	if err != nil {
		return err
	}
	for i := range c.Clients {
		if c.Clients[i].Name != *client {
			continue
		}
		env := "KENARI_KEY_" + strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(*client+"_"+*name))
		c.Clients[i].Keys = append(c.Clients[i].Keys, config.Upstream{Name: *name, KeyEnv: env})
		if err := config.SetSecret(c.Server.SecretsFile, env, value); err != nil {
			return err
		}
		if err := config.Save(*path, c); err != nil {
			return err
		}
		fmt.Printf("key %q added to client %q (restart or use the panel to have it probed)\n", *name, *client)
		return nil
	}
	return fmt.Errorf("no such client %q", *client)
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
