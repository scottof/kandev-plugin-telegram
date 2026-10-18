# Pitch: Telegram for kandev, v1

This pitch follows the method in the book [Shape Up](https://basecamp.com/shapeup).

## Problem

Kandev runs agents for hours on a laptop, and most of that time nobody is looking at the screen. An agent that stops
for a permission prompt or a question waits until someone happens to open kandev again, so a long run is often lost to
one unanswered prompt. Ideas for new tasks show up away from the desk too, and today they need the web UI. Slack users
have the official Slack plugin for filing tasks, but there is no Telegram equivalent: the catalog has none, the only
public repo (`yustme/kandev-telegram`) is an unmodified template, and the Office channel for Telegram is behind a feature
flag that is off by default, needs a public webhook URL, and stores the raw update JSON as the comment text.

## Appetite

Small batch: about one week of evenings for one developer (or the agent equivalent). The scope below is cut to fit
that, not estimated from it.

## Solution

### 1. Bot connection (plugin backend)

A Go binary plugin that talks to the Telegram Bot API by long polling (`getUpdates` with a timeout), so kandev can stay
on localhost with no public URL and no webhook. The update offset lives in Host state so a restart neither drops nor
replays messages.

Operator settings (`config_schema`):

- `bot_token`: secret, from BotFather.
- `allowed_user_ids`: comma-separated Telegram user ids. Every update from any other sender is dropped without a
  reply. Empty means the bot answers nobody.
- `kandev_url`: the base URL the phone uses to open kandev (for example a Tailscale address). Used only to build links.
- `utility_agent`: the triage agent for `/new`, same field type as the Slack plugin (`format: utility-agent`).
- `start_agent`: whether `/new` starts an agent on the created task, default false.

### 2. Alerts with task links

The bot sends one message to each allowed user when:

- an agent is waiting on a human: a pending permission prompt or clarification (`api_read: interactions`);
- a task finishes, fails or ends up waiting for review.

Each alert is short: task title, what is waiting (for a permission, the tool and its title; for a question, its first
line), and an inline URL button that opens `<kandev_url>/t/<taskId>`. Answering happens in kandev, not in Telegram.

Pending interactions come from the durable record (`ListPending`), not from events alone, because the plugin may start
after a request was made or miss an event. A sent-alert set keyed on the interaction id in Host state makes alerts
idempotent across restarts. Settings let the operator turn each alert kind on or off.

### 3. `/new <text>`

Same flow as the Slack plugin: the text goes to the selected utility agent together with the real workspace, workflow
and column topology (`api_read: workspaces, workflows, repositories`), the agent picks where the task belongs, the
plugin creates it (`api_write: tasks`), and the bot replies with the placement and a link. A reply to an earlier bot
message is included as context. If triage fails, the bot says so and creates nothing.

### 4. `/status`

One message listing what is running and what is waiting on a human, newest first and capped at about ten lines, each
with a link. A footer links to the kandev "needs you" inbox for the full list.

### 5. Settings page (plugin UI)

The standard plugin settings form plus a small status card built from `host.ui`: connected or not, the bot's username,
the last update time, and a Test button that calls `getMe` and sends one message to the allowed users. The UI reaches
the backend through manifest actions or UI-relay webhooks, never a public webhook.

## Rabbit holes

- **Answering from Telegram.** Kandev only accepts a permission or clarification answer that carries a human response
  receipt, which it issues to an authenticated kandev request and which expires after five minutes. A Telegram button
  handled by the plugin binary cannot get one. Do not try to work around it; the alert links into kandev instead.
- **Interaction events.** Check in `../kandev` whether plugins receive the permission and clarification events at all.
  If they do, use them to react quickly and keep a slow `ListPending` reconcile; if not, poll `ListPending` on an
  interval (start at 15 seconds) and diff against the sent-alert set.
- **Task state events.** Map "finished", "failed" and "waiting for review" to the real `task.state_changed` and
  `agent.completed` payloads in v0.97.0 before writing any message text. A completed turn is not the same as a finished
  task.
- **Background work.** The poll loop runs as a long-lived goroutine. Confirm it stops cleanly on plugin unload and
  upgrade, and that only one loop runs per install (Telegram returns 409 if two `getUpdates` calls overlap).
- **Capabilities.** Declare only what is used (`state`, `secrets`, `agent_invoke`, `api_read` for workspaces,
  workflows, repositories, tasks, sessions and interactions, `api_write: tasks`). Host v2 capabilities may need a
  workspace grant in Settings > Plugins; document it in the README.
- **Message limits.** Telegram caps a message at 4096 characters. Send plain text with no `parse_mode`, so task titles
  never need escaping, and truncate titles and prompts on a rune boundary.
- **Rate limits.** Respect `retry_after` on 429 responses. A burst of alerts for many tasks is batched into one message.
- **Secrets in logs.** The bot token is part of every API URL. Never log request URLs or wrap errors that contain them.
- **Template leftovers.** Remove the source-control recipe, the ping webhook, the demo page parts and the demo config
  fields, and keep the Makefile, `package.json` and package verifier consistent with what remains.

## No-gos

- Approving, rejecting or answering agent prompts from Telegram.
- Sending messages into a running task or steering an agent from Telegram.
- Group chats, multiple bots, or per-user routing beyond the allowlist.
- Webhook mode, the Office channel, or any change to kandev itself.
- Voice notes, images or files.
- Translations of bot messages.
- Publishing to the plugin marketplace, release automation changes, pushing to GitHub.

## Done means

- The checks listed in the README are green and `make verify-package-host` produces a tarball.
- Domain logic (allowlist, alert dedupe, message formatting, command parsing, offset handling) is pure Go covered by
  tests against a fake Host and a fake Telegram client. No test calls the real Telegram API.
- README explains creating the bot with BotFather, finding your user id, the workspace grant, and setting `kandev_url`.
- A smoke test plan for a local kandev: install the tarball, run a task that asks for a permission and get the alert on
  the phone, open the link, file a task with `/new`, and check `/status`. Restart kandev midway and confirm no alert is
  sent twice.

## Circuit breaker for this project

If after two weeks of real use the bot has not caught a single waiting agent sooner than opening kandev would have,
the project stops there.
