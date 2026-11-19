## Verdict: pass — 0 blockers, 0 majors (ADR-0336 implementation, round 3, model: claude-opus-5-5)

Round 3 checked 18677c50 at that commit of `feat/addons`, and both Minors of round 2
(`docs/reviews/adr-0336-implementation-claude-opus-5-5-2.md`) are fixed. `callbacks` now skips an add-on object whose
`ingress.enabled` is false, by the rule the add-on reconciler uses for its entry (`internal/edge/oidc/reconcile.go:415`,
`internal/services/addon/reconcile.go:263`). Round 2's probe, an object `wiki` whose ingress is off beside a Route on
`App.Example.com.`, now gives only `https://app.example.com/.auth/callback`. `TestOidcCallbacksSkipAnAddonObjectThatIsOff`
pins it, and a mutant without the check fails it. The comment of `TestOidcCallbacksNameEachServedHostAsTheGateDoes` now
cites Decision 2. d613eaa6's commit message keeps "Decision 5", and the PR's squash replaces that message with its
override line, so no finding remains. `go test -race -count=3 ./internal/edge/oidc`, `go vet`, `gofmt -l`,
`go tool golangci-lint run`, `go build ./...`, `go mod tidy -diff`, `just check-behavior` and `just check-hygiene` all
pass. The reason `Applied` stays for the decider, and the lane case `addon-rauthy-signin` still waits for the
funcd-addons pin and the final gate. DoD 17/18, the miss being that pending lane case. Name issue #1501 in the PR that
ships the work.
