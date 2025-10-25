package main

import (
	"fmt"
	"hole-punching-client/udp"
	"os"
	"sync"
)

func main() {
	if len(os.Args) < 1 {
		fmt.Println("Missing args:  <host:port>")
		os.Exit(1)
	}

	serverAddr := os.Args[1]

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
