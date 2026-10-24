# kandev-plugin-telegram

A [kandev](https://github.com/kdlbs/kandev) plugin that connects a Telegram bot to kandev:

- **Alerts with task links.** The bot messages you when an agent waits for a permission or an answer, and when a task
  is completed or fails to start. Each alert has a button that opens the task in kandev. You answer in kandev, not in
  Telegram.
- **`/new <text>`** files a task. A triage agent picks the workspace, workflow and column, and the bot replies with a
  link. Reply to an earlier message with `/new` to pass that message along as context.
- **`/status`** lists running tasks and agents waiting on you, with a link to the needs-you inbox.

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
   | Triage agent profile | The agent profile `/new` uses. Not set means the default from Settings > Utility Agents. |
   | Start an agent on tasks filed with /new | Off by default. When on, the text of a message you reply to also reaches the triage prompt, so do not reply to forwarded text you do not trust. |
   | Alert toggles | Waiting agents, completed tasks and failed tasks are on by default. Review alerts are off by default (see below). |

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

## Limits

- The bot cannot approve, reject or answer prompts. kandev only accepts an answer that carries a receipt issued to a
  signed-in kandev user, so every alert links into kandev instead.
- Private chats only. Group chats, media and voice notes are ignored.
- One bot per kandev install. Telegram allows only one `getUpdates` caller per bot, so do not point a second program at
  the same token.
- If the plugin restarts while it is handling a command, that command is dropped rather than replayed, so `/new`
  never files a task twice.

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
curl -F package=@kandev-plugin-telegram-0.1.1.tar.gz "$KANDEV_URL/api/plugins/install"
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
ui/bundle.js         settings status card (no build step)
tests/               UI tests
docs/PITCH.md        v1 scope
docs/SMOKE.md        manual smoke test
```

## About

Built with Claude Code agents running in Kandev.

Licensed under the MIT License, see [`LICENSE`](LICENSE).
