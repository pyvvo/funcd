# Backup lifecycle, credentials and pruning

funcd's platform backup (ADR-0203) never deletes, reads or overwrites a backup object: every generation is a new
set of keys, and its class sits in the key. Expiry is the target's job. Set one rule per prefix; funcd logs the
same table at start (`backup target: set one lifecycle expiry per prefix`), computed from `backup.retention`.

| Prefix | Expiry at the defaults | From |
|---|---|---|
| `gen/hourly/` | 2 days | ⌈`retention.hourly` / 24⌉ (48 hours) |
| `gen/daily/` | 30 days | `retention.daily`; no rule when 0 |
| `gen/weekly/` | 84 days | 7 × `retention.weekly` (12 weeks); no rule when 0 |
| `gen/verified/` | 2 days | `retention.verified` |
| `probe/` | 1 day | fixed |

`gen/pre-upgrade/` pins have no rule here (ADR-0207). With a `prefix=<p>/` in `backup.target`, every prefix below
starts with `<p>/`. Object Lock is optional; funcd sets none.

## S3 lifecycle

`aws s3api put-bucket-lifecycle-configuration --bucket <bucket> --lifecycle-configuration file://lifecycle.json`:

```json
{
  "Rules": [
    {"ID": "funcd-hourly", "Status": "Enabled", "Filter": {"Prefix": "<p>/gen/hourly/"}, "Expiration": {"Days": 2}, "NoncurrentVersionExpiration": {"NoncurrentDays": 1}},
    {"ID": "funcd-daily", "Status": "Enabled", "Filter": {"Prefix": "<p>/gen/daily/"}, "Expiration": {"Days": 30}, "NoncurrentVersionExpiration": {"NoncurrentDays": 1}},
    {"ID": "funcd-weekly", "Status": "Enabled", "Filter": {"Prefix": "<p>/gen/weekly/"}, "Expiration": {"Days": 84}, "NoncurrentVersionExpiration": {"NoncurrentDays": 1}},
    {"ID": "funcd-verified", "Status": "Enabled", "Filter": {"Prefix": "<p>/gen/verified/"}, "Expiration": {"Days": 2}, "NoncurrentVersionExpiration": {"NoncurrentDays": 1}},
    {"ID": "funcd-probe", "Status": "Enabled", "Filter": {"Prefix": "<p>/probe/"}, "Expiration": {"Days": 1}, "NoncurrentVersionExpiration": {"NoncurrentDays": 1}}
  ]
}
```

S3 expiry rounds up to midnight UTC and acts per object, so a manifest can outlive a part written the day before;
ADR-0206 lists such a generation as broken.

## The box credential (`backup.credentialsFile`)

The credential on the funcd host lists and puts. It cannot read (a leaked key reads no Secrets ciphertext), delete
(it erases no history) or put under `gen/verified/` (it forges no verified pin). `blob/` and `kv/` are the sibling
backups' prefixes (ADR-0208, ADR-0209).

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {"Sid": "List", "Effect": "Allow", "Action": "s3:ListBucket", "Resource": "arn:aws:s3:::<bucket>", "Condition": {"StringLike": {"s3:prefix": ["<p>/*"]}}},
    {"Sid": "Put", "Effect": "Allow", "Action": "s3:PutObject", "Resource": [
      "arn:aws:s3:::<bucket>/<p>/gen/hourly/*",
      "arn:aws:s3:::<bucket>/<p>/gen/daily/*",
      "arn:aws:s3:::<bucket>/<p>/gen/weekly/*",
      "arn:aws:s3:::<bucket>/<p>/gen/pre-upgrade/*",
      "arn:aws:s3:::<bucket>/<p>/probe/*",
      "arn:aws:s3:::<bucket>/<p>/blob/*",
      "arn:aws:s3:::<bucket>/<p>/kv/*"
    ]},
    {"Sid": "NoVerifiedPin", "Effect": "Deny", "Action": "s3:PutObject", "Resource": "arn:aws:s3:::<bucket>/<p>/gen/verified/*"}
  ]
}
```

The credentials file holds this key alone, in its `[default]` profile:

```ini
[default]
aws_access_key_id = <box key id>
aws_secret_access_key = <box secret>
```

## The restore and verify credential

The operator keeps a separate credential for restore (ADR-0206) and verification (ADR-0205). It reads, lists, and is
the only one that puts under `gen/verified/`. It deletes nothing either.

Run `funcdctl backup verify` off the box with it every `(backup.objectives.rpo − backup.interval) / 2` (30 minutes at
the defaults), for example from a systemd timer: `rpoRisk` follows the newest verified generation, and that slack
lets a generation verified at the age of one interval stay inside the rpo while the next verify runs.

```sh
funcdctl backup verify --target <backup.target> --credentials-file <verify credentials> \
  --identity <operator identity file> --escrow <escrow dir>
```

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {"Sid": "List", "Effect": "Allow", "Action": "s3:ListBucket", "Resource": "arn:aws:s3:::<bucket>", "Condition": {"StringLike": {"s3:prefix": ["<p>/*"]}}},
    {"Sid": "Read", "Effect": "Allow", "Action": "s3:GetObject", "Resource": "arn:aws:s3:::<bucket>/<p>/*"},
    {"Sid": "VerifiedPin", "Effect": "Allow", "Action": "s3:PutObject", "Resource": "arn:aws:s3:::<bucket>/<p>/gen/verified/*"}
  ]
}
```

## A directory target

funcd holds `<dir>/lock` while it runs, creates directories 0700 and files 0600, and writes every object through a
temp file and `link(2)`. A directory on the data directory's device is no independent copy (funcd warns). Prune it
on the same prefixes from a daily cron job or systemd timer, as the directory's owner or root:

```sh
DIR=<dir>
find "$DIR/gen/hourly" -type f -mmin +2880 -delete
find "$DIR/gen/daily" -type f -mmin +43200 -delete
find "$DIR/gen/weekly" -type f -mmin +120960 -delete
find "$DIR/gen/verified" -type f -mmin +2880 -delete
find "$DIR/probe" -type f -mmin +1440 -delete
find "$DIR/gen" -mindepth 2 -type d -empty -delete
```

A directory cannot stop its owner deleting: POSIX `unlink` needs only write access to the directory.
