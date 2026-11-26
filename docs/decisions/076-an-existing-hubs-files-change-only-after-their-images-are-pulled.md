# 076 — An existing Hub's files change only after their images are pulled

**Context.** Run again on an existing Hub, the setup link updates the Hub's stack. It writes `docker-compose.yml` anew on every run, and the helper's image is named there. After a yes, it also moves the `VAULTSYNC_HUB_IMAGE` line of `.env` to the release it ships (#216). Both files were written first, and `docker compose pull --quiet` ran afterwards. A pull that failed (registry unreachable, rate limit) ended the setup under `set -e` without a word, with the files naming images the host never pulled. The Hub ran on its old containers until the next `docker compose up -d`, which then failed (#368). The next helper release would send every existing Hub that runs the link through the compose-file half of this (#266).

**Decision.**
1. On an existing Hub (one with an `.env` and a compose file), the new compose file and, after a yes, the new `.env` are written as staged files beside the files in place. The `.env` is staged from a checked snapshot, as before (#216). The compose file keeps the mode the old one had.
2. The pull reads the staged files: `docker compose -f <staged> --env-file <staged> pull`.
3. Only after a pull that worked do the staged files replace the files in place. `.env` is first compared with the snapshot it was staged from; if it changed, neither file moves. A regular file moves in one rename. A compose file that is a symbolic link is written through, as setup always did.
4. A pull that fails or is interrupted leaves both files and the containers as they were, and removes every staged file and the lock. The setup says so and names the manual step.
5. A new Hub writes its files in place before its first pull, as before.

**Why.** The pull is the step that needs the network, and the one most likely to fail or be interrupted. With the files staged, every way it can end leaves the live files untouched, without a restore that has to run first. The lock of #216 now covers the pull, so a second setup cannot slip its own edit in between.

**Rejected alternatives.** *Restoring the files after a failed pull:* the files would name unpulled images for as long as the pull runs, and an interruption (Ctrl-C, a closed session) before the restore leaves them that way. *Pulling the images by name before writing the files* (`docker pull` for each image): it repeats in a second place what the compose file names, and the two drift apart.

**Not covered.** A new Hub whose first pull fails keeps the files it wrote; running the setup again pulls again. A failed `up -d` after a pull that worked is handled as before (`syncthing_start_hint`).

**Links.** #368, #216, #266; decision 068; `hub/scripts/tests/setup-dry-run-test.sh`.
