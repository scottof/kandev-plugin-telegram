// Command kandev-plugin-telegram is the backend half of the Telegram plugin.
// kandev spawns it as a gRPC subprocess; pluginsdk.Serve owns the transport.
package main

import (
	"context"
	"time"

	"github.com/kandev/kandev/pkg/pluginsdk"
)

// shutdownGrace stays under the roughly two seconds go-plugin allows between
// its shutdown RPC and a hard kill.
const shutdownGrace = time.Second

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	p := newTelegramPlugin(ctx)
	pluginsdk.Serve(p)
	cancel()
	p.wait(shutdownGrace)
}
