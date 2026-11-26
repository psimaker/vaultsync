# 076 — An existing Hub's files change only after their images are pulled

**Context.** Run again on an existing Hub, the setup link updates the Hub's stack. It writes `docker-compose.yml` anew on every run, and the helper's image is named there. After a yes, it also moves the `VAULTSYNC_HUB_IMAGE` line of `.env` to the release it ships (#216). Both files were written first, and `docker compose pull --quiet` ran afterwards. A pull that failed (registry unreachable, rate limit) ended the setup under `set -e` without a word, with the files naming images the host never pulled. The Hub ran on its old containers, and a later `docker compose up -d` failed for as long as the registry could not be reached (#368). The next helper release would send every existing Hub that runs the link through the compose-file half of this (#266).

**Decision.**
1. On an existing Hub (one with an `.env` and a compose file), the new compose file and, after a yes, the new `.env` are written as staged files beside the files in place. The `.env` is staged from a checked snapshot, as before (#216). The compose file keeps the mode the old one had. It is staged only when setup can name the files Compose reads: `docker-compose.yml` and at most one override file, with no `COMPOSE_FILE`, `COMPOSE_ENV_FILES` or `COMPOSE_DISABLE_ENV_FILE` and no other base file beside it. An override that is a link to nothing is skipped, as Compose skips it.
2. The pull reads the staged compose file and that override (`-f <staged> -f <override>`), and the staged `.env` as its env file (`--env-file <staged>`).
3. Only after a pull that worked do the staged files replace the files in place. `.env` is first compared with the snapshot it was staged from; if it changed, neither file moves. A regular file moves in one rename. A compose file that is a symbolic link is written through, as setup always did.
4. A pull that fails or is interrupted leaves both files and the containers as they were, and removes every staged file and the lock. The setup says so, and says that running it again brings the whole stack. The manual step in `docs/hub.md` moves only the Hub's own image.
5. A new Hub writes its files in place before its first pull, as before.

**Why.** The pull is the step that needs the network, and the one most likely to fail or be interrupted. With the files staged, every way it can end leaves the live files untouched, without a restore that has to run first. The lock of #216 now covers the pull, so a second setup cannot slip its own edit in between.

**Rejected alternatives.** *Restoring the files after a failed pull:* the files would name unpulled images for as long as the pull runs, and an interruption (Ctrl-C, a closed session) before the restore leaves them that way. *Pulling the images by name before writing the files* (`docker pull` for each image): it repeats in a second place what the compose file names, and the two drift apart. *`-f` with the staged file alone:* Compose then skips the override it loads beside `docker-compose.yml`, and a `COMPOSE_FILE` list, so the pull would miss images the later `up -d` needs.

**Not covered.**
- **Files setup cannot name.** The compose file is written in place before the pull, as before, and setup says so, in these cases:
  - `COMPOSE_FILE` is set in the environment or in `.env`, or `.env` cannot be read;
  - `COMPOSE_ENV_FILES` or `COMPOSE_DISABLE_ENV_FILE` is set. Compose then does not read the Hub's `.env` as setup writes it, so setup does not offer the image move either; it says so and where to set the image instead;
  - another base file such as `compose.yaml` lies beside the compose file;
  - more than one override lies beside it.

  A failed pull then says the file may name images the host has not downloaded. A staged `.env` still waits for the pull; without `-f`, Compose reads `COMPOSE_FILE` from the env file it is given.
- **A run that no trap catches.** A setup ended during the pull by power loss or `kill -9` leaves the lock of the `.env` edit behind. The next run says so and names the directory to remove.
- **A new Hub.** One whose first pull fails keeps the files it wrote; running the setup again pulls again.
- **A failed `up -d` after a pull that worked** is handled as before (`syncthing_start_hint`).

**Links.** #368, #216, #266; decision 068; `hub/scripts/tests/setup-dry-run-test.sh`.
