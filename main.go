package main

import (
	"fmt"
	"hole-punching-client/udp"
	"log"
	"os"
	"sync"
)

func main() {
	if len(os.Args) < 1 {
		fmt.Println("Missing args:  <host:port>")
		os.Exit(1)
	}

	serverAddr := os.Args[1]

	//     // Open (or create) a log file
	f, err := os.OpenFile("client.log", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0666)
	if err != nil {
		log.Fatalf("error opening log file: %v", err)
	}
	defer f.Close()

	// Redirect standard logger output to the file
	log.SetOutput(f)

	// Optional: add timestamp + file info
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds | log.Lshortfile)

	// Example usage
	log.Println("Client started")

	// This code will handle tcp communcation

	var wg sync.WaitGroup

	for i := 0; i < 1; i += 1 {

		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			client := udp.Client{ID: 0}
			client.ConnectToServer(serverAddr)

		}(i)
	}
	wg.Wait()

}
