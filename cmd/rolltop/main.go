// File overview: rolltop process entrypoint and startup coordinator. The
// binary starts an HTTP listener first, serves a temporary startup page while
// schema migrations and service initialization run, then swaps in the real web
// handler. After readiness it owns background loops for sync polling, IMAP IDLE,
// blob retention, thread-header backfills, and graceful shutdown cleanup.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"rolltop/backend/blob"
	"rolltop/backend/config"
	"rolltop/backend/imapclient"
	"rolltop/backend/search"
	"rolltop/backend/store"
	"rolltop/backend/syncer"
	"rolltop/backend/web"
)

type mailboxWatcher interface {
	WatchMailbox(ctx context.Context, account store.MailAccount, mailbox string, onChange func()) error
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

// Startup state is intentionally process-local: it exists before the normal
// web server is ready, so the browser and API clients can see migration and
// initialization progress instead of a dead connection.
type startupSnapshot struct {
	Ready     bool   `json:"ready"`
	Failed    bool   `json:"failed"`
	Error     string `json:"error"`
	Phase     string `json:"phase"`
	Detail    string `json:"detail"`
	Done      int    `json:"done"`
	Total     int    `json:"total"`
	StartedAt string `json:"started_at"`
}

type startupState struct {
	mu       sync.RWMutex
	snapshot startupSnapshot
}

func newStartupState() *startupState {
	return &startupState{snapshot: startupSnapshot{Phase: "Starting", Detail: "Preparing rolltop", Total: 1, StartedAt: time.Now().UTC().Format(time.RFC3339)}}
}

func (s *startupState) update(phase, detail string, done, total int) {
	if total <= 0 {
		total = 1
	}
	if done < 0 {
		done = 0
	}
	if done > total {
		done = total
	}
	s.mu.Lock()
	s.snapshot.Phase = phase
	s.snapshot.Detail = detail
	s.snapshot.Done = done
	s.snapshot.Total = total
	s.mu.Unlock()
}

func (s *startupState) ready() {
	s.mu.Lock()
	s.snapshot.Ready = true
	s.snapshot.Phase = "Ready"
	s.snapshot.Detail = "rolltop is ready"
	s.snapshot.Done = 1
	s.snapshot.Total = 1
	s.mu.Unlock()
}

func (s *startupState) fail(err error) {
	s.mu.Lock()
	s.snapshot.Failed = true
	s.snapshot.Error = err.Error()
	s.snapshot.Phase = "Startup failed"
	s.snapshot.Detail = err.Error()
	s.mu.Unlock()
}

func (s *startupState) snapshotCopy() startupSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshot
}

type startupGate struct {
	state *startupState
	mu    sync.RWMutex
	ready http.Handler
}

func (g *startupGate) setHandler(handler http.Handler) {
	g.mu.Lock()
	g.ready = handler
	g.mu.Unlock()
}

// startupGate is the temporary root handler. It serves startup status until
// startApp has built the real application handler, then delegates all normal
// traffic while keeping /api/startup available for diagnostics.
func (g *startupGate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/startup" {
		writeStartupJSON(w, http.StatusOK, g.state.snapshotCopy())
		return
	}
	g.mu.RLock()
	ready := g.ready
	g.mu.RUnlock()
	if ready != nil {
		ready.ServeHTTP(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeStartupJSON(w, http.StatusServiceUnavailable, g.state.snapshotCopy())
		return
	}
	writeStartupHTML(w, g.state.snapshotCopy())
}

func writeStartupJSON(w http.ResponseWriter, status int, snapshot startupSnapshot) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(snapshot)
}

func writeStartupHTML(w http.ResponseWriter, snapshot startupSnapshot) {
	percent := 0
	if snapshot.Total > 0 {
		percent = snapshot.Done * 100 / snapshot.Total
	}
	if percent < 4 && !snapshot.Failed {
		percent = 4
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprintf(w, `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>rolltop starting</title>
<style>
:root{color-scheme:light dark;font-family:Inter,ui-sans-serif,system-ui,sans-serif;background:#f2f0eb;color:#151f2e}body{margin:0;min-height:100vh;display:grid;place-items:center;background:linear-gradient(180deg,#faf8f4,#e4ded7)}.panel{width:min(520px,calc(100vw - 40px));border:1px solid #ded8d1;border-radius:10px;background:#fff;box-shadow:0 18px 60px rgba(21,31,46,.18);padding:28px}.brand{font-weight:800;font-size:28px;letter-spacing:0}.phase{margin-top:18px;font-size:15px;font-weight:700}.detail{margin-top:6px;color:#665f59;line-height:1.45}.bar{height:8px;background:#e6ded6;border-radius:999px;overflow:hidden;margin-top:22px}.fill{height:100%%;width:%d%%;background:#c46b44;transition:width .25s ease}.error{margin-top:18px;color:#8f472b;font-weight:700}@media (prefers-color-scheme:dark){:root{background:#151f2e;color:#f2f0eb}body{background:#151f2e}.panel{background:#182331;border-color:#4a403a}.detail{color:#b7b3aa}.bar{background:#273241}}</style>
<script>
async function poll(){try{const r=await fetch('/api/startup',{cache:'no-store'});const s=await r.json();if(s.ready){location.reload();return}document.querySelector('.phase').textContent=s.phase||'Starting';document.querySelector('.detail').textContent=s.detail||'';const pct=s.total?Math.max(4,Math.min(100,Math.round((s.done/s.total)*100))):4;document.querySelector('.fill').style.width=pct+'%%';if(s.failed){document.querySelector('.error').textContent=s.error||'Startup failed';return}}catch(e){}setTimeout(poll,700)}setTimeout(poll,700)
</script>
</head>
<body>
<main class="panel">
<div class="brand">rolltop</div>
<div class="phase">%s</div>
<div class="detail">%s</div>
<div class="bar"><div class="fill"></div></div>
<div class="error">%s</div>
</main>
</body>
</html>`, percent, html.EscapeString(snapshot.Phase), html.EscapeString(snapshot.Detail), html.EscapeString(snapshot.Error))
}

type appRuntime struct {
	db      *store.Store
	search  *search.Service
	handler http.Handler
}

func (a *appRuntime) close() {
	if a == nil {
		return
	}
	if a.search != nil {
		_ = a.search.Close()
	}
	if a.db != nil {
		_ = a.db.Close()
	}
}

// run starts the HTTP listener before backend initialization. That lets slow
// database migrations or index opens show progress in the browser rather than
// making the app look down.
func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	startup := newStartupState()
	gate := &startupGate{state: startup}
	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           gate,
		ReadHeaderTimeout: 10 * time.Second,
	}
	serverErr := make(chan error, 1)
	go func() {
		log.Printf("rolltop starting on %s", cfg.Addr)
		err := server.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			serverErr <- nil
			return
		}
		serverErr <- err
	}()

	app, err := startApp(ctx, cfg, startup)
	if err != nil {
		startup.fail(err)
		log.Printf("rolltop startup failed: %v", err)
		select {
		case <-ctx.Done():
		case listenErr := <-serverErr:
			return listenErr
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		return err
	}
	defer app.close()
	gate.setHandler(app.handler)
	startup.ready()
	log.Printf("rolltop ready on %s", cfg.Addr)

	select {
	case <-ctx.Done():
	case err := <-serverErr:
		if err != nil {
			return err
		}
		return nil
	}

	if app.db != nil {
		if n, err := app.db.MarkRunningSyncRunsInterrupted(context.Background()); err != nil {
			log.Printf("mark interrupted sync runs during shutdown: %v", err)
		} else if n > 0 {
			log.Printf("marked interrupted sync runs during shutdown: %d", n)
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownCtx)
	if err := <-serverErr; err != nil {
		return err
	}
	return nil
}

// startApp performs the blocking startup work in dependency order: schema,
// user stores, interrupted sync cleanup, search indexes, then web/sync services.
func startApp(ctx context.Context, cfg config.Config, startup *startupState) (*appRuntime, error) {
	startup.update("System database", "opening", 0, 1)
	reporter := func(p store.MigrationProgress) {
		phase := "System database"
		if p.Scope == "user" {
			phase = "User databases"
		}
		detail := strings.TrimSpace(p.Migration + " - " + p.Step)
		startup.update(phase, detail, p.Done, p.Total)
	}
	db, err := store.OpenServerWithProgress(cfg.DatabasePath, cfg.DataDir, reporter)
	if err != nil {
		return nil, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = db.Close()
		}
	}()

	startup.update("User databases", "opening per-user stores", 0, 1)
	if err := db.PrepareUserStores(ctx, reporter); err != nil {
		return nil, err
	}

	startup.update("Sync state", "marking interrupted sync runs", 0, 1)
	if n, err := db.MarkRunningSyncRunsInterrupted(context.Background()); err != nil {
		log.Printf("mark interrupted sync runs: %v", err)
	} else if n > 0 {
		log.Printf("marked interrupted sync runs: %d", n)
	}

	startup.update("Messages", "backfilling thread keys", 0, 1)
	for {
		n, err := db.BackfillThreadKeys(context.Background(), 10000)
		if err != nil {
			log.Printf("backfill thread keys: %v", err)
			break
		}
		if n < 10000 {
			break
		}
	}

	startup.update("Search", "opening indexes", 0, 1)
	searchSvc, err := search.OpenPerUser(filepath.Join(cfg.DataDir, "users"))
	if err != nil {
		return nil, err
	}
	defer func() {
		if cleanup {
			_ = searchSvc.Close()
		}
	}()

	startup.update("Services", "initializing sync and web services", 0, 1)
	blobStore := blob.New(cfg.DataDir)
	imapFetcher := &imapclient.Fetcher{MasterKey: cfg.MasterKey}
	syncSvc := &syncer.Service{
		Store:         db,
		Blobs:         blobStore,
		Search:        searchSvc,
		Fetcher:       imapFetcher,
		BlobRetention: cfg.BlobRetention,
	}
	syncRunner := syncer.NewRunnerWithContext(ctx, syncSvc)
	webServer, err := web.New(web.Options{
		Store:        db,
		Blobs:        blobStore,
		Search:       searchSvc,
		Syncer:       syncSvc,
		SyncRunner:   syncRunner,
		MasterKey:    cfg.MasterKey,
		DataDir:      cfg.DataDir,
		DatabasePath: cfg.DatabasePath,
		IndexPath:    cfg.IndexPath,
		SessionTTL:   cfg.SessionTTL,
		CookieSecure: cfg.CookieSecure,
		WebhookToken: cfg.WebhookToken,
	})
	if err != nil {
		return nil, err
	}

	go backfillThreadHeaders(ctx, db, cfg.DataDir)
	if cfg.SyncInterval > 0 {
		go scheduledSync(ctx, db, syncRunner, cfg.SyncInterval)
	}
	if cfg.InboxPollInterval > 0 {
		go inboxPoll(ctx, db, syncRunner, cfg.InboxPollInterval)
		go inboxIdle(ctx, db, syncRunner, imapFetcher, cfg.InboxPollInterval)
	}
	if cfg.BlobRetention > 0 {
		go storageRetention(ctx, db, syncSvc, cfg.BlobRetention)
	}

	cleanup = false
	return &appRuntime{db: db, search: searchSvc, handler: webServer.Handler()}, nil
}

func storageRetention(ctx context.Context, db *store.Store, svc *syncer.Service, retention time.Duration) {
	run := func() {
		total := syncer.RetentionStats{}
		for {
			stats, err := svc.ApplyStorageRetention(ctx, retention, 500)
			if err != nil {
				if ctx.Err() == nil {
					log.Printf("storage retention: %v", err)
				}
				return
			}
			total.CompactedMessages += stats.CompactedMessages
			total.PrunedBlobs += stats.PrunedBlobs
			if stats.CompactedMessages < 500 && stats.PrunedBlobs < 500 {
				break
			}
			select {
			case <-ctx.Done():
				return
			default:
			}
		}
		if total.CompactedMessages > 0 || total.PrunedBlobs > 0 {
			log.Printf("storage retention compacted_messages=%d pruned_blobs=%d", total.CompactedMessages, total.PrunedBlobs)
			vacuumCtx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			defer cancel()
			if err := db.Vacuum(vacuumCtx); err != nil {
				log.Printf("storage retention vacuum: %v", err)
			}
		}
	}
	run()
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

func backfillThreadHeaders(ctx context.Context, db *store.Store, dataDir string) {
	for {
		checked, updated, err := db.BackfillThreadHeadersFromBlobs(ctx, dataDir, 500)
		if err != nil {
			log.Printf("backfill thread headers: %v", err)
			return
		}
		if updated > 0 {
			log.Printf("backfilled thread headers: checked=%d updated=%d", checked, updated)
		}
		if checked < 500 {
			return
		}
		select {
		case <-ctx.Done():
			return
		default:
		}
	}
}

func inboxPoll(ctx context.Context, db *store.Store, runner *syncer.Runner, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			userIDs, err := db.ListUserIDsWithAccounts(ctx)
			if err != nil {
				log.Printf("inbox poll list accounts: %v", err)
				continue
			}
			for _, userID := range userIDs {
				account, err := db.GetMailAccount(ctx, userID)
				if err != nil {
					log.Printf("inbox poll account user_id=%d: %v", userID, err)
					continue
				}
				mb, err := inboxMailbox(ctx, db, userID, account)
				if err != nil {
					log.Printf("inbox poll mailbox user_id=%d: %v", userID, err)
					continue
				}
				mode, err := db.EffectiveMailboxSyncMode(ctx, userID, account.ID, mb)
				if err != nil || mode != "auto" {
					continue
				}
				if !runner.StartPriorityMailboxes(userID, []string{mb.Name}) {
					log.Printf("inbox poll user_id=%d queued: inbox already running", userID)
				}
			}
		}
	}
}

func inboxIdle(ctx context.Context, db *store.Store, runner *syncer.Runner, watcher mailboxWatcher, retryEvery time.Duration) {
	if watcher == nil {
		return
	}
	if retryEvery <= 0 {
		retryEvery = time.Minute
	}
	active := map[int64]context.CancelFunc{}
	var mu sync.Mutex
	startMissing := func() {
		userIDs, err := db.ListUserIDsWithAccounts(ctx)
		if err != nil {
			log.Printf("inbox idle list accounts: %v", err)
			return
		}
		for _, userID := range userIDs {
			mu.Lock()
			if _, ok := active[userID]; ok {
				mu.Unlock()
				continue
			}
			mu.Unlock()
			account, err := db.GetMailAccount(ctx, userID)
			if err != nil {
				log.Printf("inbox idle account user_id=%d: %v", userID, err)
				continue
			}
			mb, err := inboxMailbox(ctx, db, userID, account)
			if err != nil {
				log.Printf("inbox idle mailbox user_id=%d: %v", userID, err)
				continue
			}
			mode, err := db.EffectiveMailboxSyncMode(ctx, userID, account.ID, mb)
			if err != nil || mode != "auto" {
				continue
			}
			watchCtx, cancel := context.WithCancel(ctx)
			mu.Lock()
			active[userID] = cancel
			mu.Unlock()
			go func(account store.MailAccount, userID int64, mailboxName string) {
				defer func() {
					cancel()
					mu.Lock()
					delete(active, userID)
					mu.Unlock()
				}()
				for watchCtx.Err() == nil {
					err := watcher.WatchMailbox(watchCtx, account, mailboxName, func() {
						log.Printf("inbox idle user_id=%d event: queue priority inbox sync", userID)
						if !runner.StartPriorityMailboxes(userID, []string{mailboxName}) {
							log.Printf("inbox idle user_id=%d queued: inbox already running", userID)
						}
					})
					if watchCtx.Err() != nil {
						return
					}
					if err != nil {
						log.Printf("inbox idle user_id=%d: %v", userID, err)
					}
					timer := time.NewTimer(retryEvery)
					select {
					case <-watchCtx.Done():
						timer.Stop()
						return
					case <-timer.C:
					}
				}
			}(account, userID, mb.Name)
		}
	}
	startMissing()
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			mu.Lock()
			for _, cancel := range active {
				cancel()
			}
			mu.Unlock()
			return
		case <-ticker.C:
			startMissing()
		}
	}
}

func inboxMailbox(ctx context.Context, db *store.Store, userID int64, account store.MailAccount) (store.Mailbox, error) {
	boxes, err := db.ListMailboxesForUser(ctx, userID)
	if err == nil {
		for _, box := range boxes {
			if box.AccountID == account.ID && box.Role == "inbox" {
				return box.Mailbox, nil
			}
		}
	}
	return db.GetOrCreateMailbox(ctx, userID, account.ID, "INBOX")
}

func scheduledSync(ctx context.Context, db *store.Store, runner *syncer.Runner, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			userIDs, err := db.ListUserIDsWithAccounts(ctx)
			if err != nil {
				log.Printf("scheduled sync list accounts: %v", err)
				continue
			}
			for _, userID := range userIDs {
				if !runner.Start(userID) {
					log.Printf("scheduled sync user_id=%d skipped: already running", userID)
				}
			}
		}
	}
}
