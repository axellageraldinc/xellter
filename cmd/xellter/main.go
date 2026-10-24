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
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/axellageraldinc/xellter/internal/admin"
	"github.com/axellageraldinc/xellter/internal/app"
	"github.com/axellageraldinc/xellter/internal/auth"
	"github.com/axellageraldinc/xellter/internal/config"
	"github.com/axellageraldinc/xellter/internal/proxy"
	"github.com/axellageraldinc/xellter/internal/store"
)

const usage = `xellter - LLM gateway for kenari.id

Usage:
  xellter serve        [-config xellter.yaml]
  xellter check        [-config xellter.yaml]        validate the config
  xellter explain      [-config f] [-shadow name]    print the current ranking and why (fetches the catalog, probes keys)
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
	case "explain":
		err = explain(os.Args[2:])
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

func explain(args []string) error {
	fs := flag.NewFlagSet("explain", flag.ExitOnError)
	path := cfgPath(fs)
	only := fs.String("shadow", "", "only this shadow model")
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
	a.Rt.MarkSeen(a.Cat.Snapshot())
	a.ProbeKeys(ctx)
	names := make([]string, 0)
	for n := range a.Rt.Rankings() {
		if *only == "" || *only == n {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		r := a.Rt.Rankings()[n]
		fmt.Printf("\n== %s (%s, %s)  round-robin group: %d\n", n, r.Modality, r.Mode, r.Front)
		for i, c := range r.Candidates {
			fmt.Printf("%2d. %-34s %s\n", i+1, c.Model, c.Reason)
		}
		for _, c := range r.Trial {
			fmt.Printf(" T  %-34s %s\n", c.Model, c.Reason)
		}
		if len(r.Candidates)+len(r.Trial) == 0 {
			fmt.Println("    (no eligible model)")
		}
		for _, x := range r.Rejected {
			fmt.Printf("    x %-32s %s\n", x.Model, x.Why)
		}
		if len(r.Unavailable) > 0 {
			fmt.Printf("    (%d catalog models are not callable with the configured keys)\n", len(r.Unavailable))
		}
	}
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
