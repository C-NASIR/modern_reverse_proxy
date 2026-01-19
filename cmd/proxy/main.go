package main

import (
	"log"
	"net/http"
	"os"

	"modern_reverse_proxy/internal/config"
	"modern_reverse_proxy/internal/proxy"
	"modern_reverse_proxy/internal/runtime"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatalf("usage: %s <config.json>", os.Args[0])
	}
	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		log.Fatalf("read config: %v", err)
	}
	cfg, err := config.ParseJSON(data)
	if err != nil {
		log.Fatalf("parse config: %v", err)
	}
	snap, err := runtime.BuildSnapshot(cfg)
	if err != nil {
		log.Fatalf("build snapshot: %v", err)
	}

	store := runtime.NewStore(snap)
	handler := &proxy.Handler{
		Store:  store,
		Engine: proxy.NewEngine(),
	}

	listenAddr := cfg.ListenAddr
	if listenAddr == "" {
		listenAddr = "127.0.0.1:8080"
	}

	log.Printf("listening on %s", listenAddr)
	log.Fatal(http.ListenAndServe(listenAddr, handler))
}
