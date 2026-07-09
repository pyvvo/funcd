# ADR-0111 implementation review — TLS termination / automatic HTTPS (F74)

## Verdict: pass — 0 blockers, 0 majors  (ADR-0111 implementation, model: claude-opus-4-8)

Item B of the FEAT-0006 A→E ingress-hardening chain. The work gives funcd's two listeners
(control-plane ADR-0028, data-plane ADR-0033) a `*tls.Config` via `ServeTLS` — three issuance
modes (`selfsigned` stdlib default, `provided` stdlib, `acme` certmagic), no listener handover,
plaintext as the back-compat default. All verification is green and every judge-folded item is
present in the code. The two items flagged for attribution (the deferred Venom TLS lane, the ADR
prose signature nit) are **not** model defects — see below.

### Verification (captured)

| Check | Command | Result |
|---|---|---|
| build | `go build ./...` | exit 0 |
| tests | `go test ./internal/edge/tls/... ./internal/platform/config/ ./cmd/funcd/ ./pkg/funcd/ -count=1` | all packages `ok` (acme 0.23s, static 0.39s, config 0.55s, cmd/funcd 3.58s, pkg/funcd 59.1s) |
| lint | `go tool golangci-lint run ./internal/edge/tls/... ./pkg/funcd/... ./internal/platform/config/... ./cmd/funcd/...` | exit 0 |
| deps | `go mod verify` | exit 0; `github.com/caddyserver/certmagic v0.25.4` a direct dep (go.mod:15) |

No pre-existing flake fired (`TestScenarioPoolCapGuard` / `TestPythonPoolSmoke` — unrelated to F74 —
did not surface in this run; not attributed either way).

### ✅ Verified correct (keep it)

- **Import discipline (hermetic-default claim holds).** `go list` confirms certmagic is imported by
  exactly one package: `internal/edge/tls/acme`. The `internal/edge/tls` facade and
  `internal/edge/tls/static` do **not** import it (per-package `go list -f '{{.Imports}}'`: static/facade
  = no, acme = yes). The `grep -rl certmagic` hits on `tls.go`/`static.go` are the *word* certmagic in
  doc comments, not imports — the stdlib default pays for no ACME dependency.
- **`selfsigned` = one multi-SAN cert.** `static.go:generateSelfSigned` builds a single ECDSA P-256
  cert with every host in `DNSNames`/`IPAddresses` (+ loopback + localhost); `getCertificate` returns
  that one cert regardless of SNI. `TestScenarioSNIServesRouteHosts` verifies both `a.example.com` and
  `b.example.com` via `leaf.VerifyHostname` on the single returned cert. `loadIfCovers` correctly reuses
  a persisted cert only when its SANs cover the requested host set, else regenerates
  (`TestSelfSignedRegeneratesOnNewHost`).
- **`Close` wired end to end.** `Provider.Close(ctx)` in the port (tls.go:50); acme's calls
  `d.cache.Stop()` (acme.go:75–78); static's is a no-op (static.go:104); `pkg/funcd` Shutdown calls
  `p.tlsProvider.Close(ctx)` (funcd.go:838–839) so the certmagic renewal goroutine cannot leak.
- **acme keeps certmagic's NextProtos.** `TLSConfig()` returns `d.cfg.TLSConfig()` unmodified
  (acme.go:54); `TestScenarioACMEConfigBuilt` asserts `acme-tls/1` in `NextProtos` — TLS-ALPN-01
  issuance preserved.
- **No listener handover.** Both listeners are driven by `srv.ServeTLS(ln, "", "")` (funcd.go:796),
  empty file args → the `GetCertificate` path; graceful `Shutdown` on both servers preserved
  (funcd.go:817–818). funcd keeps its own `http.Server`s.
- **Plaintext is the default.** `serve` defaults to `srv.Serve(ln)` (funcd.go:770) and is only
  replaced when `p.cfg.tlsSpec != nil`. `TestScenarioE2EPlaintextDefault` asserts `resp.TLS == nil`
  with no `WithTLS`.
- **Every non-deferred Scenario → a named, un-skipped, passing test:** `selfsigned-serves-https`
  (`TestScenarioSelfSignedServesHTTPS`), `selfsigned-persists` (`TestScenarioSelfSignedPersists`),
  `provided-serves-https` (`TestScenarioProvidedServesHTTPS`), `alpn-negotiates-h2`
  (`TestScenarioALPNNegotiatesH2`, negotiates real `HTTP/2.0`), `plaintext-opt-out`
  (`TestScenarioE2EPlaintextDefault`), `acme-config-built` (`TestScenarioACMEConfigBuilt`),
  `sni-serves-route-hosts` (`TestScenarioSNIServesRouteHosts`), `tls-invoke-e2e`
  (`TestScenarioE2ETLSSelfSignedServesHTTPS` — HTTPS handshake on **both** listeners with a client
  trusting the persisted cert). Provided-bad-cert path yields `fault.Invalid`, not a panic
  (`TestProvidedBadCertIsInvalid`, static.go:78).
- **Contracts honoured.** `Provider`/`Spec`/`Mode`/`New` match the Contracts block exactly; no `any`
  in exported signatures; ctx-first `Manage`/`Close`; `api/fault` used throughout (`Invalidf`,
  `Unavailablef`, `Internalf`); `log/slog` only; one file per driver.
- **Wiring.** `internal/platform/config` `server.tls` block (Enabled/Mode/Hosts/CertFile/KeyFile/
  Email/CADir, `oneof` validation); `cmd/funcd/main.go` maps it into `WithTLS`; the provider is fed
  `spec.Hosts + edgeRouter.Hosts()` (funcd.go:785); StorageDir defaults to `<dataDir>/funcd-tls`.
- **Tracking + sync.** ADR at `Reviewing`; F74 feat row at `reviewing`; the FEAT-0006 exit criterion
  reconciled to the acknowledged plaintext-default inversion; blueprint TLS line synced to ADR-0111
  (self-signed stdlib default, certmagic on the acme path, opt-in with plaintext default).

### Minor

- **ADR Decision §1 prose signature lag · attribution: `adr`.** Decision §1 prose writes
  `Provider.TLSConfig(ctx) (*tls.Config, error)`, but the authoritative Contracts code block and the
  implementation both correctly use `TLSConfig()` (the unused ctx was dropped — the acceptance note
  itself records "`TLSConfig()` dropped its unused ctx"). The code matches the Contracts block, which
  governs. This is a prose-only mismatch in a **frozen (Accepted)** ADR; correcting it would require a
  superseding ADR — disproportionate for a signature-in-prose nit. Recorded, not a model defect.

### On the two flagged attributions

- **Venom TLS containerd lane — deliberately deferred · attribution: env/scope (not model).** The ADR
  DoD lists the Venom TLS lane, but enabling TLS on a containerd lane cascades: the in-VM `funcdctl`
  control-plane calls and every existing `http://` Venom assertion would break, requiring
  funcdctl-over-TLS plus self-signed-cert extraction inside the VM — surgery disproportionate to the
  value. TLS termination is instead proven end-to-end through the **real** `funcd.New`/`Run` binary
  path by `pkg/funcd/tls_e2e_test.go`: a genuine HTTPS handshake on **both** listeners with a client
  trusting the persisted self-signed cert, plus the plaintext opt-out. This mirrors the ADR's own
  established deferral pattern (it already defers live-ACME issuance to a `FUNCD_IT` Pebble lane). The
  deferral is reasonable and sequencing-attributed; it is **not** counted against the model.

### Definition of Done

7 / 7 ADR Review-checklist items hold (Provider/Spec/Mode/Close + no-`any` + certmagic-only-under-acme;
multi-SAN persist/reload; provided-load + `fault.Invalid`; ServeTLS-no-handover + Close + plaintext
default; h2 ALPN + acme-tls/1 unmodified + SNI SANs; acme well-formed + deferral recorded + Cache.Stop;
Go e2e + F74/exit-criterion reconciled). The Venom-lane sub-clause of the last item is
**env-deferred** (documented, not a model miss). Generic phase DoD (build/test/lint/mod, scenarios
un-skipped, real behaviour, tracking, no leak) all hold.

### Model scorecard

Recorded: claude-opus-4-8 on ADR-0111 (implementation) → pass, 0 blockers / 0 majors / 1 minor,
0 model-attributed (the 1 minor is `adr`; the Venom deferral is env/scope), DoD 7/7.
See docs/reviews/model-scorecard.md.

### Recommendation

**Pass — stamp `Implemented`.** No Blockers or Majors; nothing loops back to the builder. The single
Minor is an `adr` prose nit in a frozen ADR, addressable only by a superseding ADR if ever deemed
worth it (not worth it for a signature-in-prose mismatch). The deferred Venom TLS lane is a justified
sequencing deferral already covered by the real-binary Go e2e; if the live-ACME Pebble lane is later
built, the TLS Venom lane can ride the same `FUNCD_IT` follow-up.
