// Command kandev-plugin-telegram is the backend half of the Telegram plugin.
// kandev spawns it as a gRPC subprocess; pluginsdk.Serve owns the transport.
package main

import "github.com/kandev/kandev/pkg/pluginsdk"

func main() {
	pluginsdk.Serve(&telegramPlugin{})
}
