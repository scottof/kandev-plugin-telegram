# Changelog

## Unreleased

### Added

- Telegram bot over long polling, configured from Settings > Plugins: bot token, allowed user ids, kandev URL,
  triage agent profile, start agent, and alert toggles.
- Alerts with a task link when an agent waits for a permission or an answer, and when a task is completed, fails to
  start or (opt-in) moves to review. Bursts are batched into one message. Alerts survive a restart
  or a Telegram outage, and a restart only repeats one if it lands between sending it and recording it.
- `/new <text>` files a task through the triage agent, with the replied-to message as context.
- `/status` lists running tasks and agents waiting on you.
- Settings page status card with a test message button, settings warnings and the last alert Telegram refused.

### Removed

- The template's source-control recipe, demo page, composer action, ping webhook and demo settings.

## [0.2.0] - 2026-10-05

Template release this plugin started from (`kdlbs/kandev-plugin-template`).

### Changed

- feat(template): adopt host Action and harden release checks (#8) (b6d5a7c)
- Fix marketplace release archive checksum (#9) (b13b7ee)
- feat: add provider-neutral source-control recipe (#6) (be2f0c5)
- Teach the post-0.86 host surface (#5) (c107c22)
- chore(security): bump x/net & x/text, pin CI actions to commit SHA (#3) (e034b08)
- ci: add manual release workflow (#2) (bd7aed6)
- ci: standardize plugin automation (542f331)
- docs: describe release checksums.txt as advisory, not index-pinned (2c5fd80)
- ci: add release workflow that publishes package + checksums on tag (a25f3d3)
- chore: scaffold kandev plugin template (526ca58)
