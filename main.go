package main

import (
	"fmt"
	"log"
	"net/http"

	"github.com/supergiant/supergiant/controller"
	"github.com/supergiant/supergiant/core"
)

func main() {

	client := core.NewClient()

	// TODO
	go core.NewWorker(client).Work()

	router := controller.NewRouter(client)

	fmt.Println("Serving API on port :8080")
	log.Fatal(http.ListenAndServe(":8080", router))
}
