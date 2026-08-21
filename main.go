// dreamproject backend — a personality chatbot with temporary in-memory memory.
package main

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"dreamproject/api"
	"dreamproject/database"
)

//go:embed public
var publicFS embed.FS

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}
	snapshot := os.Getenv("SNAPSHOT") // e.g. data/sessions.json; empty = pure RAM

	api.Store = database.NewStore(snapshot)

	// auto-forget sessions every 5 min (time-limited memory)
	go func() {
		t := time.NewTicker(5 * time.Minute)
		for range t.C {
			api.Store.Evict()
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/chat", api.Chat)
	mux.HandleFunc("/api/history", api.History)
	mux.HandleFunc("/api/characters", api.Characters)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})
	// serve the embedded single-page frontend at "/"
	sub, err := fs.Sub(publicFS, "public")
	if err != nil {
		log.Fatal(err)
	}
	mux.Handle("/", http.FileServer(http.FS(sub)))

	srv := &http.Server{Addr: "0.0.0.0:" + port, Handler: mux}
	go func() {
		log.Printf("dreamproject listening on :%s", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	// graceful shutdown → snapshot memory so it survives restart
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	<-stop
	api.Store.Save()
	log.Println("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
}