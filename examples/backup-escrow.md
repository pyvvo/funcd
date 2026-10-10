# Backup encryption and the escrow set

funcd seals every platform backup generation (ADR-0204) to age recipients the operator holds: the box writes what
it cannot read. Each store file (`events`, `metastore`, `runs`) is one age stream cut into 8 MiB parts, so
`cat part-*` is one age file that the `age` CLI opens without funcd. The manifest stays plain and names, by
fingerprint, the keys a restore needs.

## Recipients

Create two identities and keep them apart: the operator's, and a recovery one kept offline.

```sh
age-keygen -o operator.key        # X25519; the public key is printed and is in the file's comment
age-keygen -o recovery.key
age-keygen -y operator.key > backup-recipients.txt
age-keygen -y recovery.key >> backup-recipients.txt
```

`age-keygen -pq` makes a post-quantum hybrid (ML-KEM-768 + X25519) identity whose recipient starts with `age1pq1`.
age refuses to seal to a hybrid recipient beside an X25519 one, so use one kind for every recipient. SSH and plugin
recipients are refused. Point funcd at the recipients files:

```yaml
backup:
  target: s3://<bucket>?region=<region>&prefix=<p>/
  encryption:
    recipients:
      - /etc/funcd/backup-recipients.txt
```

At start funcd refuses fewer than 2 distinct recipients across the files, and recipients that do not seal together.
`encryption.none: true` stores the files unsealed; it needs no recipients and a set `secrets.encryptionKeyFile`, so
Secret values never leave in plaintext. To open a generation by hand:

```sh
cat gen/<class>/<n>-<timeline>/metastore/part-* | age -d -i recovery.key > metastore
```

An identity file encrypted with `age -p` is not accepted by a restore: decrypt it first.

## The escrow set

The operator keeps it outside the box, every target and every generation; funcd never reads or writes it while it
serves.

```
<escrow dir>/
  secrets/*          every secrets key a retained generation names; any file name
  master/*           every master secret a retained generation names; any file name
  tls/               a copy of <storage.dataDir>/tls, or the provided certFile and keyFile
  funcdconfig.yaml   the operator config: auth.token, auth.credentials, key file paths
```

A restore finds a key by its fingerprint, the first 16 hex characters of SHA-256 over a fixed label and the file's
exact bytes. Copy the key files byte for byte (`cp`, never an editor): an added newline changes the fingerprint.
The lookup follows symlinks, so a key file or the `secrets/` or `master/` directory may be a link; a link that does
not resolve is named in the refusal.
The node master secret is `s3gateway.masterSecretFile` when set, else `<storage.dataDir>/s3gateway/master.key`,
with the S3 gateway on or off. A restore (ADR-0206) without the master a generation names refuses, unless
`--new-master-secret`, which prints every S3 keypair and catalog token that changes.

Keep a key or an identity until no retained generation names it: an ADR-0203 class or pin, an ADR-0208 blob mirror
generation, or an ADR-0209 KV chain.

## Rotation

| Key | Procedure | Generations |
|---|---|---|
| recipients | add the new identity, edit the files, restart | the next run uses the new set; older ones keep theirs, since the box cannot read to re-seal; a removed identity stays while a generation lists it |
| secrets key | no live rotation exists; any future one puts the new key in escrow before first use | each manifest names its key |
| master secret | none; a change is the `--new-master-secret` path | each manifest names it |
| TLS, config | refresh the escrow copy after a change | not in generations |
