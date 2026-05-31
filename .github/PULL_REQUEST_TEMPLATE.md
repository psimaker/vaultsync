<!-- PR titles follow Conventional Commits (feat:, fix:, docs:, chore:, ci: …);
     PRs are squash-merged, so the title becomes the commit subject on main. -->

## What & why
<!-- One or two sentences: what changes and why. Link the issue if there is one. -->

## Component(s)
- [ ] go (bridge / Syncthing)
- [ ] ios (app / widget)
- [ ] notify (relay)
- [ ] docs / CI

## Testing
- [ ] `cd go && make patch && go test -tags noassets ./bridge`
- [ ] `cd notify && go test ./...`
- [ ] iOS build / `xcodebuild test`
- [ ] Not applicable
