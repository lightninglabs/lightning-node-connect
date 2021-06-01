package main

import (
	"log"
	"net/http"
	"os"
	"strings"
)

func main() {
	if len(os.Args) != 3 {
		log.Fatal("Not enough arguments provided. Please provide the " +
			"file serving directory and the port.")
	}
	dir := os.Args[1]
	port := os.Args[2]
	fs := http.FileServer(http.Dir(dir))
	log.Print("Serving " + dir + " on http://localhost:" + port)
	err := http.ListenAndServe(":"+port, http.HandlerFunc(
		func(resp http.ResponseWriter, req *http.Request) {
			log.Println(req.URL)
			resp.Header().Add("Cache-Control", "no-cache")
			if strings.HasSuffix(req.URL.Path, ".wasm") {
				resp.Header().Set("content-type", "application/wasm")
			}
			resp.Header().Set("Access-Control-Allow-Origin", "*")
			fs.ServeHTTP(resp, req)
		},
	))
	if err != nil {
		log.Fatalf("Error running server: %v", err)
	}
}
