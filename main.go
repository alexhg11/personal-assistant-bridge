// personal-assistant-bridge receives WhatsApp Cloud API webhooks, runs each
// message through Claude Code against the Obsidian vault, and replies.
//
// Boundaries: this process holds the Meta secrets and the git deploy key.
// Claude Code runs as a different OS user through one sudo-allowed wrapper
// and never sees them.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)

	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	store, err := openStore(cfg.DBPath)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer store.Close()

	wa := newWhatsApp(cfg)
	runner := newRunner(cfg)
	repo := newRepo(cfg)

	queue := make(chan inbound, 100)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	w := &worker{cfg: cfg, store: store, wa: wa, runner: runner, repo: repo}
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		w.run(ctx, queue)
	}()
	go w.runReminders(ctx, 30*time.Second)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /webhook", verifyHandler(cfg))
	mux.HandleFunc("POST /webhook", webhookHandler(cfg, store, queue))
	mux.HandleFunc("POST /jobs/{name}", jobsHandler(cfg, queue))
	mux.HandleFunc("GET /healthz", func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusOK)
		_, _ = rw.Write([]byte("ok\n"))
	})

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
	}

	go func() {
		log.Printf("listening on %s", cfg.Listen)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("http server: %v", err)
			stop()
		}
	}()

	<-ctx.Done()
	log.Print("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("http shutdown: %v", err)
	}
	close(queue)
	select {
	case <-workerDone:
	case <-time.After(cfg.ClaudeTimeout + 30*time.Second):
		log.Print("worker did not finish in time")
	}
	os.Exit(0)
}
