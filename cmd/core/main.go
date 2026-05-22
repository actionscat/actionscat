package main

import (
	"actionscat/internal/api"
	"log"
	"net/http"
	"os"

	_ "actionscat/actions/acat_bili_link"
	_ "actionscat/actions/acat_maimai_recorder"
	
)

func main() {
	addr := os.Getenv("ACTIONSCAT_ADDR")
	if addr == "" {
		addr = ":7999"
	}

	router := api.NewRouter()

	log.Printf("ActionsCat core listening on %s", addr)

	if err := http.ListenAndServe(addr, router); err != nil {
		log.Fatalf("server stopped: %v", err)
	}
}
