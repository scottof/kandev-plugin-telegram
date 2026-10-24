package main

import (
	"strings"
	"unicode"
)

// command is a parsed bot command such as "/new Fix the login page".
type command struct {
	Name string
	Args string
}

// parseCommand reads a leading "/name[@bot] args" from text. It reports false
// for plain text and for commands addressed to a different bot.
func parseCommand(text, botUsername string) (command, bool) {
	text = strings.TrimLeftFunc(text, unicode.IsSpace)
	if !strings.HasPrefix(text, "/") {
		return command{}, false
	}
	head, rest := text, ""
	if i := strings.IndexFunc(text, unicode.IsSpace); i >= 0 {
		head, rest = text[:i], text[i:]
	}
	name := strings.TrimPrefix(head, "/")
	if at := strings.IndexByte(name, '@'); at >= 0 {
		if !strings.EqualFold(name[at+1:], botUsername) {
			return command{}, false
		}
		name = name[:at]
	}
	if name == "" {
		return command{}, false
	}
	return command{Name: strings.ToLower(name), Args: strings.TrimSpace(rest)}, true
}

const helpText = "Commands:\n" +
	"/new <text>: file a task in kandev through the triage agent. Reply to a message to add it as context.\n" +
	"/status: list running tasks and agents waiting on you.\n" +
	"/task <words>: find a task by title.\n" +
	"/mute <words>: stop alerts for a task. /unmute lists muted tasks.\n\n" +
	"Alerts for agents waiting on you and finished tasks arrive here on their own. " +
	"An alert about one task has Mute and Done buttons."
