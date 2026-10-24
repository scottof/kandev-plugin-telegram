# kandev-plugin-telegram

A [kandev](https://github.com/kdlbs/kandev) plugin that connects a Telegram bot to kandev:

- **Alerts with task links.** The bot messages you when an agent waits for a permission or an answer, and when a task
  is completed or fails to start. Each alert has a button that opens the task in kandev. You answer in kandev, not in
  Telegram.
- **`/new <text>`** files a task. A triage agent picks the workspace, workflow and column, and the bot replies with a
  link and the column it used. It reacts with 👀 to your message while the agent works. Reply to an earlier message
  with `/new` to pass that message along as context. A new task never lands in a column for finished work: a pick such
  as Done goes to the workflow's start column, and a task kandev still reports as completed is moved back there.
- **`/status`** lists running tasks and agents waiting on you, with a link to the needs-you inbox.
- **`notify_user` for agents.** An agent working on a task can message you on Telegram, for example when a deploy
  finishes or it is blocked. See [Agent notifications](#agent-notifications).

The bot uses long polling (`getUpdates`), so kandev can stay on localhost. No public URL or webhook is needed.

Requires kandev 0.95.0 or newer.

## Setup

1. **Create the bot.** In Telegram, open [@BotFather](https://t.me/BotFather), send `/newbot` and follow the prompts.
   Copy the token it gives you.
2. **Find your user id.** Message a bot such as [@userinfobot](https://t.me/userinfobot); it replies with your numeric
   id. Do this for every person who should use the bot.
3. **Start a chat with your bot.** Open your new bot and send `/start`. Telegram does not let a bot message someone who
   has not started a chat with it, so every allowed user must do this once.
4. **Install the plugin** in kandev under Settings > Plugins (see [Build and install](#build-and-install)).
5. **Fill in the settings** on Settings > Plugins > Telegram:

   | Setting | What to enter |
   |---|---|
   | Bot token | The token from BotFather. It is stored in kandev's encrypted vault. |
   | Allowed Telegram user ids | Comma-separated ids from step 2. Messages from anyone else are ignored without a reply. Empty means the bot answers nobody. |
   | kandev URL | The base URL your phone uses to open kandev, for example `https://my-laptop.tailnet-name.ts.net:38429` over Tailscale. It is only used to build links, and must not contain a user name, password, query string or fragment. A `localhost` address works for the settings check but not on a phone, and the status card warns about it. |
   | Triage agent profile | The agent profile `/new` uses. Not set means the default from Settings > Utility Agents; when that is not set either and kandev has a single workspace, the workspace's default agent is used. The profile must be enabled, global and not a CLI passthrough. |
   | Start an agent on tasks filed with /new | Off by default. When on, the text of a message you reply to also reaches the triage prompt, so do not reply to forwarded text you do not trust. |
   | Alert toggles | Waiting agents, completed tasks and failed tasks are on by default. Review alerts and column alerts are off by default (see below). |

   kandev restarts the plugin when you save.
6. **Approve capabilities if asked.** If the plugin page shows a capability approval section, approve the requested
   reads (workspaces, workflows, repositories, tasks, interactions) and the task write.
7. **Check the connection.** The status card at the top of the plugin page shows whether the bot is connected. Press
   **Send test message**; each allowed user should get a message.

## How alerts work

- **Waiting agents.** The plugin reads kandev's durable list of pending permission prompts and questions every 15
  seconds, and sooner when kandev reports a change. Each prompt is alerted once; the sent set is kept in plugin state,
  so a restart does not repeat alerts.
- **Task alerts** come from `task.state_changed`:
  - *Completed*: the task moved to `COMPLETED`.
  - *Failed*: the task moved to `FAILED`. kandev uses this when a task fails to start. An agent that crashes mid-run
    moves the task to `REVIEW` instead.
  - *Waiting for review*: the task moved to `REVIEW`. This happens after **every** agent turn, so it is off by default.
  - Quick chat tasks never alert.
  - kandev can drop events under load, so task alerts are best-effort. Waiting-agent alerts are not, because they are
    read from the durable list.
- **Column alerts** (off by default): "Moved: <task>" with the old and new column, for board moves and for the moves
  kandev's workflows make, for example to Review after an agent turn. kandev reports the first kind with `task.moved`
  and the second only with `task.updated`, so the plugin keeps each task's last seen column (up to 500 tasks) and
  alerts once per change, whichever event arrives first. A task the plugin has never seen alerts from its second
  column change on. A move that completes the task is left to the *Completed* alert when that one is on. Without
  this setting, a card move only alerts when it changes the task's state, as moving it to a column that completes
  tasks does.
- A burst of alerts is sent as one message with up to ten task buttons.
- During a Telegram outage, alerts wait in plugin state and go out on the next 15 second cycle, so a restart does not
  lose them. New kandev events do not make the bot retry sooner. When Telegram asks the bot to slow down (HTTP 429),
  nothing is sent to Telegram until the wait it names has passed, capped at 5 minutes. During that wait **Send test
  message** fails at once and says how long to wait. Up to 50 task alerts wait; older ones are dropped. If Telegram refuses an alert for good (for example a
  link button it will not accept), the bot sends it once without buttons, and the status card shows the last refused
  alert.
- Delivery is tracked per allowed user. A user Telegram cannot reach (for example one who never sent `/start`) does not
  hold alerts back from the others, and a user who already got an alert is not sent it again while another user's
  delivery is retried.

## Agent notifications

The plugin gives agents in kanban and office tasks a `notify_user` tool. Kandev shows it to the agent under a
server-qualified name. A call takes:

| Argument | Rule |
| --- | --- |
| `message` | Plain text, 1 to 3500 characters. No formatting is applied. |
| `idempotency_key` | 1 to 128 characters: a letter or digit, then letters, digits, `.`, `_`, `:`, `/`, `-`. Use one stable key per event. |
| `recipient` | Optional. One Telegram user id from the allowed list. Without it, every allowed user gets the message. |

The task the agent works on comes from kandev, not from the arguments. The message gets a "From task" line and an
**Open task** button (plain text when Telegram refuses the link, as with `localhost`).

The result's `status` is `delivered`, `partial`, `failed`, `uncertain` or `rejected`. Repeating a key returns the
stored result with `duplicate: true` and sends nothing. The same key with a different message is rejected. When
Telegram did not confirm a send, the result is `uncertain` and the key stays blocked, so an agent cannot send the
same message twice by retrying. While Telegram asks the bot to wait, the call fails at once with
`retry_after_seconds` and the key stays free.

## Troubleshooting

| Problem | What to do |
| --- | --- |
| The bot never answers | Check that your numeric id is in *Allowed Telegram user ids* and that you sent `/start`. The status card's *Last update* shows whether the bot received anything. |
| "entry N is not a numeric Telegram user id" | The field takes numeric ids, not usernames. [@userinfobot](https://t.me/userinfobot) tells you yours. |
| Alerts and replies arrive without buttons, and the card warns about the kandev URL | Telegram refuses links to `localhost` and IP addresses. Use a name your phone can reach, such as a Tailscale address. |
| "Triage failed: no agent profile for /new" | Pick a *Triage agent profile* on the plugin page, or a default in Settings > Utility Agents. With a single workspace, its default agent is also used. |
| "Triage failed: the Triage agent profile ... cannot answer one-shot requests" | Pick an enabled, global profile that is not a CLI passthrough. |
| `/status`, alerts or `/new` cannot read or create tasks | Approve the plugin under *Workspace access* on the plugin page. Kandev may ask again after an upgrade that changes what the plugin declares. |
| The test message says users must send `/start` | Each allowed user opens the bot and sends `/start` once. |
| "Telegram asked the bot to wait N s" | Telegram is rate limiting the bot. Wait and try again. |
| The status card shows a 409 error | Another program polls the same bot token. Stop it, or create a separate bot. |
| Moving a card does not alert | Alerts follow the task's state. Turn on *Alert when a task changes column*. |

## Limits

- The bot cannot approve, reject or answer prompts. kandev only accepts an answer that carries a receipt issued to a
  signed-in kandev user, so every alert links into kandev instead.
- Private chats only. Group chats, media and voice notes are ignored.
- One bot per kandev install. Telegram allows only one `getUpdates` caller per bot, so do not point a second program at
  the same token.
- If the plugin restarts while it is handling a command (saving its settings restarts it), that command is dropped
  rather than replayed, so `/new` never files a task twice. After the restart the bot replies to the command to say
  so, and for `/new` whether the task had already been created.

## Build and install

The kandev SDK is not a published module. This repo expects a kandev checkout next to it, pinned to
`.kandev-sdk-ref`:

```sh
git clone https://github.com/kdlbs/kandev.git ../kandev
git -C ../kandev checkout "$(cat .kandev-sdk-ref)"
export GOTOOLCHAIN=auto   # fetches Go 1.26 if the local Go is older
```

Build every platform and verify the package:

```sh
make verify-package
```

Install the resulting `kandev-plugin-telegram-<version>.tar.gz` from Settings > Plugins, or:

```sh
curl -F package=@kandev-plugin-telegram-0.1.4.tar.gz "$KANDEV_URL/api/plugins/install"
```

## Development

```sh
npm ci --ignore-scripts
make check-format
make vet
make test-backend     # Go tests against a fake Host and a fake Telegram client, no network
npm run test:ui       # node --test for ui/bundle.js
make verify-package-host   # host platform only; make verify-package builds all five
```

`docs/SMOKE.md` is the manual check against a real kandev and a real bot.

## Layout

```text
manifest.yaml        plugin contract: capabilities, settings, webhooks
server/              Go plugin binary
  plugin.go          lifecycle and event handling
  poller.go          getUpdates loop and command dispatch
  alerts.go          pending-interaction reconcile and task-state alerts
  triage.go          /new
  status.go          /status
  webhooks.go        settings card backend
  telegram.go        Bot API client (the token never reaches logs or errors)
  notify.go          notify_user agent tool
  moves.go           column alerts and the last seen column per task
  inflight.go        notice for a command a restart cut short
ui/bundle.js         settings status card (no build step)
tests/               UI tests
docs/PITCH.md        v1 scope
docs/SMOKE.md        manual smoke test
```

## About

Built with Claude Code agents running in Kandev.

Licensed under the MIT License, see [`LICENSE`](LICENSE).
