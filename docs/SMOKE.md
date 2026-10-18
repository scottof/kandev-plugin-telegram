# Smoke test: local kandev and a real bot

Run this before calling a release usable. It needs a kandev 0.95.0 or newer instance, a Telegram bot token, and a
phone with Telegram that can reach `kandev_url`.

## Setup

1. `make verify-package` and note the tarball name.
2. In kandev, Settings > Plugins: install the tarball. Approve capabilities if the page asks.
3. Fill in the bot token, your user id, and `kandev_url`. Leave the triage profile unset unless you want a specific one.
4. On your phone, send `/start` to the bot. Expect the help text.
5. On the plugin page, check that the status card says **Connected** and shows the bot's username. Press **Send test
   message** and expect it on the phone.

## Alerts

6. Start a task whose agent will ask for a permission (for example, ask it to run a shell command with approvals on).
   Within about 15 seconds, expect "Needs permission: <task>" with an **Open task** button.
7. Tap the button. Expect kandev to open on that task. Answer the prompt in kandev.
8. Ask the agent something that makes it ask you a question. Expect "Has a question: <task>" with the first line of
   the question.
9. Complete a task in kandev. Expect "Finished: <task>".
10. Turn on review alerts, save, finish an agent turn, and expect "Waiting for review: <task>". Turn them off again.

## Commands

11. Send `/status`. Expect running tasks and waiting agents, newest first, and the needs-you inbox link.
12. Send `/new write a README section about smoke tests`. Expect a reply with the identifier, title, placement and a
    link, and the task in kandev with "Filed from Telegram by ..." at the end of its description.
13. Reply to one of the alerts with `/new follow up on this`. Check that the new task's description reflects the
    alert you replied to.
14. Pick a broken triage profile (or stop the default utility agent) and send `/new x`. Expect "Triage failed: ...
    No task was created." and no new task.

## Restart without duplicates

15. Leave a permission prompt pending and confirm its alert arrived.
16. Restart kandev (or disable and re-enable the plugin).
17. Wait at least 30 seconds. Expect **no** second alert for the same prompt.
18. Send `/status` and expect a normal reply, which shows the poll loop resumed from the stored offset.

## Allowlist

19. From a Telegram account that is not in the allowlist, send `/status`. Expect no reply at all.

Record the kandev version, the tarball name, and which steps passed.
