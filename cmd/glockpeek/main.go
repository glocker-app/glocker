// Command glockpeek serves the glocker stats dashboard (the web interface) as a
// standalone process, separate from the glocker daemon. It reads
// /etc/glocker/glockpeek.yaml for its listen address and database, serves the
// dashboard on a per-account login, and exposes a token-authenticated ingest API
// the glocker syncer pushes local records into. Designed to be hosted remotely.
//
// Admin subcommands (run locally, they touch the DB directly):
//
//	glockpeek -adduser <name>        # create a dashboard account (prompts for a password)
//	glockpeek -passwd  <name>        # change a password
//	glockpeek -addtoken <name>       # mint an ingest API token for <name> (printed once)
//	glockpeek -setlimit <email> -devices <n>   # set an account's device cap (<0 = unlimited)
//	glockpeek -backfill-trials [-dry-run]       # one-off: give never-subscribed accounts a 14-day trial
//
// This replaces the old command-line log viewer; run the server as its own
// service (see extras/glockpeek.service).
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"glocker/internal/accountability"
	"glocker/internal/config"
	"glocker/internal/mailer"
	"glocker/internal/stats"
	"glocker/internal/store"
)

const defaultListen = "127.0.0.1:4317"

// version is the build tag, stamped at build time via
// -ldflags "-X main.version=$(git describe ...)" (see the Makefile). "dev" for
// an un-stamped `go build`.
var version = "dev"

func main() {
	showVersion := flag.Bool("version", false, "print the build version and exit")
	listen := flag.String("listen", "", "address to serve on (overrides config; default "+defaultListen+")")
	dbDSN := flag.String("db", "", "database DSN (overrides config; e.g. a sqlite file path for dev)")
	dbDriver := flag.String("driver", "", "database driver override: sqlite|postgres (pairs with -db; lets you run against a throwaway copy for migration validation without touching the config)")
	modeFlag := flag.String("mode", "", "run mode override: local|hosted (for a throwaway test instance without a config file)")
	addUser := flag.String("adduser", "", "create a dashboard account with this email, then exit")
	passwd := flag.String("passwd", "", "change the password for this email, then exit")
	addToken := flag.String("addtoken", "", "mint an ingest API token for this account (email), then exit")
	setLimit := flag.String("setlimit", "", "set the device (API-token) limit for this email (use with -devices), then exit")
	backfillTrials := flag.Bool("backfill-trials", false, "give every verified, never-subscribed account a 14-day no-card trial from now (one-off); pairs with -dry-run to only list who'd be affected")
	dryRun := flag.Bool("dry-run", false, "with -backfill-trials, list the affected accounts without writing")
	importRules := flag.String("import-rules", "", "load Glocker starter rules from a JSON file ({\"rules\":[{tag,program,title}]}) into the default-rules template that new accounts seed from, then exit")
	migrateFlag := flag.Bool("migrate", false, "run DB migrations, then exit — the explicit pre-deploy step for hosted/multi-node (local + admin subcommands still migrate on open)")
	devices := flag.Int("devices", 1, "device limit to apply with -setlimit (negative = unlimited)")
	exportAssets := flag.String("export-assets", "", "write the CDN-eligible static assets (content-hashed names) to this dir, then exit — for `aws s3 sync` to the assets bucket")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	// Emit the content-hashed static assets for uploading to the CDN bucket. No
	// config or DB needed; the names match what the running server points its HTML at.
	if *exportAssets != "" {
		if err := stats.ExportCDNAssets(*exportAssets); err != nil {
			log.Fatalf("glockpeek: export-assets: %v", err)
		}
		log.Printf("glockpeek: exported CDN assets to %s", *exportAssets)
		return
	}

	addr := defaultListen
	driver := config.DefaultDatabaseDriver
	dsn := config.DefaultDatabaseDSN
	dbMaxOpen, dbMaxIdle, dbLifetimeMin := 0, 0, 0
	cdnBase := ""
	secureCookies := false
	mode := config.GlockpeekModeLocal
	var mailCfg config.MailConfig
	appURL := ""
	adminEmail := ""
	termsURL := ""
	captcha := false
	requireEncryption := false
	var billingOpts stats.BillingOptions

	if gp, err := config.LoadGlockpeekConfig(); err != nil {
		log.Printf("glockpeek: could not load %s (%v); using defaults", config.GlockpeekConfigFile, err)
	} else {
		if gp.Listen != "" {
			addr = gp.Listen
		}
		if gp.Database.Driver != "" {
			driver = gp.Database.Driver
		}
		if gp.Database.DSN != "" {
			dsn = gp.Database.DSN
		}
		dbMaxOpen = gp.Database.MaxOpenConns
		dbMaxIdle = gp.Database.MaxIdleConns
		dbLifetimeMin = gp.Database.ConnMaxLifetimeMinutes
		cdnBase = gp.CDNBase
		secureCookies = gp.SecureCookies
		if gp.Mode != "" {
			mode = gp.Mode
		}
		mailCfg = gp.Mail
		appURL = gp.AppURL
		adminEmail = gp.AdminEmail
		termsURL = gp.TermsURL
		captcha = gp.Captcha
		requireEncryption = gp.RequireEncryption
		if gp.Billing.Enabled {
			successURL := gp.Billing.SuccessURL
			if successURL == "" {
				successURL = gp.AppURL // default post-checkout redirect: the dashboard
			}
			billingOpts = stats.BillingOptions{
				Enabled:          true,
				APIToken:         gp.Billing.APIToken,
				WebhookSecret:    gp.Billing.WebhookSecret,
				ProductID:        gp.Billing.ProductID,
				ProductIDMonthly: gp.Billing.ProductIDMonthly,
				APIBase:          gp.Billing.ResolvedAPIBase(),
				SuccessURL:       successURL,
			}
		}
	}
	if *listen != "" {
		addr = *listen
	}
	if *dbDSN != "" {
		dsn = *dbDSN
	}
	if *dbDriver != "" {
		driver = *dbDriver
	}
	if *modeFlag != "" {
		mode = *modeFlag
	}

	if mode != config.GlockpeekModeLocal && mode != config.GlockpeekModeHosted {
		log.Printf("glockpeek: unknown glockpeek_mode %q; treating as %q", mode, config.GlockpeekModeLocal)
		mode = config.GlockpeekModeLocal
	}
	hosted := mode == config.GlockpeekModeHosted
	// Local mode is a personal-desktop guarantee: bind loopback only, so the
	// dashboard (and its open, tokenless ingest endpoint) is unreachable from
	// other hosts regardless of the configured listen address.
	if !hosted {
		addr = forceLoopback(addr)
	}

	// Migrations are an explicit step in hosted/multi-node: N nodes booting together
	// must not race on DDL, so the server boot skips them and a one-shot
	// `glockpeek -migrate` (run once before a deploy) applies them. Local mode,
	// admin subcommands, and `-migrate` itself all migrate on open (single process,
	// no race).
	adminCmd := *addUser != "" || *passwd != "" || *addToken != "" || *setLimit != "" || *importRules != ""
	skipMigrate := hosted && !*migrateFlag && !adminCmd
	db, err := store.Open(store.Options{
		Driver:          driver,
		DSN:             dsn,
		SkipMigrate:     skipMigrate,
		MaxOpenConns:    dbMaxOpen,
		MaxIdleConns:    dbMaxIdle,
		ConnMaxLifetime: time.Duration(dbLifetimeMin) * time.Minute,
	})
	if err != nil {
		log.Fatalf("glockpeek: open database (%s): %v", driver, err)
	}
	if *migrateFlag {
		log.Printf("glockpeek: migrations applied (%s)", driver)
		return
	}

	// Admin subcommands: touch the DB and exit, no server.
	switch {
	case *addUser != "":
		runAddUser(db, *addUser)
		return
	case *passwd != "":
		runPasswd(db, *passwd)
		return
	case *addToken != "":
		runAddToken(db, *addToken)
		return
	case *setLimit != "":
		runSetLimit(db, *setLimit, *devices)
		return
	case *backfillTrials:
		runBackfillTrials(db, *dryRun)
		return
	case *importRules != "":
		runImportRules(db, *importRules)
		return
	}

	mailSvc := mailer.New(mailCfg)
	opts := stats.Options{
		Auth:              hosted,
		SecureCookies:     secureCookies,
		Mailer:            mailSvc,
		AppURL:            appURL,
		AdminEmail:        adminEmail,
		Captcha:           captcha,
		RequireEncryption: requireEncryption,
		Billing:           billingOpts,
		Version:           version,
		CDNBase:           cdnBase,
		TermsURL:          termsURL,
	}
	desc := "hosted: login required"
	if !hosted {
		// Local mode: everything runs as one implicit account, ingest is open.
		du, err := db.EnsureDefaultUser()
		if err != nil {
			log.Fatalf("glockpeek: ensure default user: %v", err)
		}
		opts.DefaultUserID = du.ID
		desc = "local: no login, loopback only"
	}

	mux := http.NewServeMux()
	stats.Register(mux, db, opts)

	// Accountability emails (periodic digest + went-dark alerts) run server-side —
	// only glockpeek can send when the agent is dead. Reads each account's synced
	// notification config; a no-op if mail is unconfigured or nobody opted in.
	go accountability.New(db, mailSvc, appURL, billingOpts.Enabled).Run()

	// Note: the DSN is deliberately NOT logged — it can carry a database password.
	log.Printf("glockpeek %s serving the dashboard at http://%s/ (%s); db driver %s", version, addr, desc, driver)
	// Timeouts so a slow/stuck client can't tie up a connection indefinitely
	// (slowloris) on a small node. WriteTimeout is generous for the larger reads.
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	log.Fatal(srv.ListenAndServe())
}

// forceLoopback rewrites addr's host to 127.0.0.1, preserving the port, so local
// mode can never accidentally bind a public interface.
func forceLoopback(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		port = "4317"
	}
	return net.JoinHostPort("127.0.0.1", port)
}

// runImportRules loads the Glocker starter-rule template from a JSON file into
// the default_rules table, so new accounts seed from it instead of the built-in
// minimal set. The file's rules are the hosted product's curated defaults; the
// open-source build ships none (empty table -> built-in fallback).
func runImportRules(db *store.DB, path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("glockpeek: read %s: %v", path, err)
	}
	var doc struct {
		Rules []store.DefaultRule `json:"rules"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		log.Fatalf("glockpeek: parse %s: %v", path, err)
	}
	imported, skipped, err := db.ImportDefaultRules(doc.Rules)
	if err != nil {
		log.Fatalf("glockpeek: import rules: %v", err)
	}
	fmt.Printf("imported %d default rules from %s", imported, path)
	if skipped > 0 {
		fmt.Printf(" (%d skipped: unknown tag or empty)", skipped)
	}
	fmt.Println()
}

func runAddUser(db *store.DB, email string) {
	pw := readNewPassword()
	if _, err := db.CreateUser(email, pw); err != nil {
		log.Fatalf("glockpeek: create account %q: %v", email, err)
	}
	fmt.Printf("created dashboard account %q\n", email)
}

func runPasswd(db *store.DB, email string) {
	pw := readNewPassword()
	if err := db.SetPassword(email, pw); err != nil {
		log.Fatalf("glockpeek: set password for %q: %v", email, err)
	}
	fmt.Printf("password updated for %q\n", email)
}

func runAddToken(db *store.DB, email string) {
	u, err := db.UserByEmail(email)
	if err != nil {
		log.Fatalf("glockpeek: no such account %q: %v", email, err)
	}
	tok, err := db.CreateAPIToken(u.ID, "cli")
	if err != nil {
		log.Fatalf("glockpeek: create token: %v", err)
	}
	fmt.Printf("ingest token for %q (store it now — it is not recoverable):\n\n  %s\n\n", email, tok)
	fmt.Println("The glocker syncer sends it as:  Authorization: Bearer <token>")
}

// runSetLimit sets an account's device (API-token) cap — the manual lever for a
// paid upgrade until self-serve billing exists. Negative means unlimited.
func runSetLimit(db *store.DB, email string, n int) {
	if err := db.SetDeviceLimit(email, n); err != nil {
		log.Fatalf("glockpeek: set device limit for %q: %v", email, err)
	}
	label := strconv.Itoa(n)
	if n < 0 {
		label = "unlimited"
	}
	fmt.Printf("device limit for %q set to %s\n", email, label)
}

// runBackfillTrials grants a 14-day no-card trial (from now) to every verified,
// never-subscribed account that doesn't have one — the one-off at feature launch.
// -dry-run lists who'd be affected without writing, for a rehearsal on a restored
// prod copy. Re-running is a no-op (accounts with a trial are excluded).
func runBackfillTrials(db *store.DB, dry bool) {
	users, err := db.UsersMissingTrial()
	if err != nil {
		log.Fatalf("glockpeek: backfill-trials: %v", err)
	}
	end := time.Now().Add(store.TrialLength)
	for _, u := range users {
		if dry {
			fmt.Printf("would set trial for %s\n", u.Email)
			continue
		}
		if err := db.SetTrialEnd(u.ID, end); err != nil {
			log.Fatalf("glockpeek: backfill-trials: set %s: %v", u.Email, err)
		}
		fmt.Printf("set trial for %s\n", u.Email)
	}
	verb := "set"
	if dry {
		verb = "would set"
	}
	fmt.Printf("%s a trial for %d account(s) (ends %s)\n", verb, len(users), end.Format("2006-01-02"))
}

// readNewPassword reads a password without echo from a TTY, or a single line
// from stdin when piped (for scripting). It requires confirmation on a TTY.
func readNewPassword() string {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		fmt.Print("New password: ")
		p1, err := term.ReadPassword(fd)
		fmt.Println()
		if err != nil {
			log.Fatalf("glockpeek: reading password: %v", err)
		}
		fmt.Print("Confirm password: ")
		p2, err := term.ReadPassword(fd)
		fmt.Println()
		if err != nil {
			log.Fatalf("glockpeek: reading password: %v", err)
		}
		if string(p1) != string(p2) {
			log.Fatal("glockpeek: passwords do not match")
		}
		if len(p1) == 0 {
			log.Fatal("glockpeek: empty password")
		}
		return string(p1)
	}
	// Piped: read one line.
	sc := bufio.NewScanner(os.Stdin)
	if !sc.Scan() {
		log.Fatal("glockpeek: no password on stdin")
	}
	pw := strings.TrimRight(sc.Text(), "\r\n")
	if pw == "" {
		log.Fatal("glockpeek: empty password")
	}
	return pw
}
