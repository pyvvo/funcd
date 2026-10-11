# Restore runbook

A restore is offline: `funcd restore` loads one backup generation (ADR-0203) into empty data directories, and the
next `funcd` start boots held (ADR-0206). While held the API, the controllers and the data plane serve, but no
timer, blob source, Sensor, WorkflowRun, App rollout, retention sweep, data reclaim or backup acts until an
operator reads the evidence and releases the hold.

## Recovery order

1. **Escrow keys.** Fetch the age identity that opens the generation, and the escrow set holding the secrets key
   and the master secret (`examples/backup-escrow.md`). Set `secrets.encryptionKeyFile` in `funcdconfig.yaml` to
   the escrowed secrets key.
2. **Pick a point.** `funcd restore list --from <target> --credentials-file <restore credential>` prints the
   generations as a tree: each timeline under the generation its restore loaded, `abandoned` on a dead branch.
   `funcd restore inspect <point>` shows the manifest, the keys it needs, the version verdict, the counts per kind
   and the evidence; `--diff <other point>` lists what changed between two points.
3. **Restore the platform.** With funcd stopped and the metastore, workflow and dead-letter directories empty:

   ```sh
   funcd restore run latest --from <target> --credentials-file <restore credential> \
     --identity recovery.key --escrow /mnt/escrow
   ```

   A point is `latest`, an RFC 3339 time, `<timeline>/<n>`, a resourceVersion `<timeline>-<n>` (the state before
   that write), `pre-upgrade` or `verified`. Run it as root or as the data directory's owner: what it creates gets
   that owner. Without the escrowed master secret, `--new-master-secret` restores anyway and prints the
   credentials that change.
4. **Restore KV and blob data** with `funcd restore kv` and `funcd restore blob` where the node runs them
   (ADR-0209, ADR-0208).
5. **Start held.** Start funcd. A restore killed midway leaves `restore.inprogress`, and funcd refuses to start
   until the directories it wrote are emptied and the restore is run again.
6. **Read the evidence.** `funcdctl hold status` prints the marker, `restore.json`, the counts now, the paused runs,
   the dead letters, the blob keys a release would replay per source, the KV and Bucket data no object names, and
   the Functions not Ready (a Function whose image is not in the registry stays not Ready: push it).
7. **Release.**

   ```sh
   funcdctl hold release                          # blob sources replay what their seen lists lack
   funcdctl hold release --advance team/uploads   # mark the listed objects of team/uploads seen instead
   ```

   Each restored run that was not finished stays paused: `funcdctl workflow resume` runs it from its record,
   `funcdctl workflow cancel` ends it. Dead letters stay parked until replayed. The boot reclaims of KV and Bucket
   data with no object run at the next start after the release: apply, while held, the objects whose data stays.

To bring back one object, `funcd restore inspect <point> --object Secret/<ns>/<name> --secrets-key <key>
--reveal-secrets > s.yaml`, then `funcdctl apply -f s.yaml`. Without `--reveal-secrets` Secret values print as
`REDACTED`.

## Roll back an upgrade

`funcd upgrade` (ADR-0207) wrote a `pre-upgrade` generation with the old version and printed it as
`<timeline>/<n>`; `upgrade.generation` in `<dataDir>/safemode.json` keeps it. Restore that generation, not the point
`pre-upgrade`, whose newest may be an older upgrade's. Every write after the pin is lost, and KV and blob data are
outside it: a release that migrates them says how it rolls back.

1. `sudo systemctl stop funcd`.
2. Move the metastore, workflow and dead-letter directories aside (`<dataDir>/store`, `workflow`, `deadletter`).
3. Restore with the old binary, which reads what it wrote (a newer minor's generation it refuses, ADR-0206):

   ```sh
   sudo <path>.previous restore run <timeline>/<n> --credentials-file <restore credential> \
     --identity recovery.key --escrow /mnt/escrow
   ```

4. Install the old binary: `sudo mv <path>.previous <path>`.
5. After a stopped start (exit status 70, below), `sudo funcd safe-mode reset`.
6. Start funcd: it boots held. Verify with `funcdctl hold status`, then `funcdctl hold release`.

## Crash loop (safe mode)

funcd counts its unclean starts in `<dataDir>/safemode.json` (ADR-0207): a start is clean once it ran
`recovery.safeMode.stableAfter` (10 minutes) or stopped on a signal. A fault outside a runner, such as a taken port,
counts like a crash.

- After `recovery.safeMode.afterCrashes` (3) unclean starts in a row, funcd starts held, marker reason `safe-mode`, and
  logs an error naming the count and the last error. A loop born in a timer, a Sensor, a run or a backup stops there.
  Fix the cause, then `funcdctl hold release`; the count clears after `stableAfter`.
- After twice as many, funcd exits with status 70 before it opens a store, and the unit does not restart it
  (`RestartPreventExitStatus=70`). The error names the next step: the rollback above to `upgrade.generation` when the
  loop follows `funcd upgrade` to this version; else `funcd restore run verified` into directories moved aside ("no
  way back" after a `--no-snapshot` upgrade). Then `funcd safe-mode reset` clears the count and keeps the marker, so
  the next start is held.

## The first drill

Run the drill on a scratch node before relying on a backup:

1. Take a generation of a seeded platform (or the newest one of production).
2. On a scratch node with `server.network.egress: true` (on Linux, outbound traffic stays off), restore it with the
   steps above. The integrity checks run during the restore: each store's sha256 against the manifest, and every
   Secret decoded with the configured key.
3. Start funcd held and never release it: compare `funcdctl hold status` counts with the source's, read one object of
   each kind, and note the time from the restore to the first answer as a recovery-time input.
