package main

import "github.com/kandev/kandev/pkg/pluginsdk"

// telegramPlugin implements pluginsdk.Plugin. UnimplementedPlugin supplies
// no-op defaults and stores the Host handed over by SetHost.
type telegramPlugin struct {
	pluginsdk.UnimplementedPlugin
}

var _ pluginsdk.Plugin = (*telegramPlugin)(nil)
