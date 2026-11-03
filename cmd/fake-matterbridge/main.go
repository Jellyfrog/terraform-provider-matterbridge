// Command fake-matterbridge runs the fake Matterbridge that the tests use. Use it to try the provider manually.
// It serves the WebSocket API of the frontend on 127.0.0.1, with the zigbee2mqtt and hass plugins.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/jellyfrog/terraform-provider-matterbridge/internal/fakematterbridge"
)

func main() {
	password := flag.String("password", "", "the frontend password (default: no password)")
	flag.Parse()

	server, err := fakematterbridge.Start(fakematterbridge.Options{Password: *password})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer server.Close()
	fmt.Printf("url = %q\n", server.URL)
	for name, versions := range fakematterbridge.Registry {
		fmt.Printf("  npm %s %v\n", name, versions)
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	<-stop
}
