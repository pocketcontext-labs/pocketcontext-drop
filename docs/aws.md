# AWS

Drop keeps two things in AWS: release executables in one S3 bucket and app secrets in SSM Parameter Store. Everything else (accounts, apps, release and secret metadata, nodes) is in Drop's database. The code is `internal/awsx`; nothing else in the repository talks to AWS.

## Credentials

Drop never reads, stores or accepts access keys in its own configuration. The clients use the AWS SDK for Go v2 default credential chain: environment variables, then the shared config files, then the EC2 instance profile through IMDSv2. On walter-aws that is the instance profile of the role `drop-walter-aws`. Building the clients makes no network call; the first request does.

| Variable | Meaning |
| --- | --- |
| `DROP_AWS_REGION` | Region of the bucket and parameters, for example `eu-north-1` (required). |
| `DROP_S3_BUCKET` | The release bucket (required). |
| `DROP_SSM_PREFIX` | Parameter prefix, default `/drop`. The IAM policy below assumes `/drop`. |

## Releases in S3

- A release is stored once under its content address, `releases/<sha256>` (64 lower-case hex characters). Two apps uploading the same executable share one object.
- The server uploads with `PutObject` sending `If-None-Match: *` and the `x-amz-checksum-sha256` checksum. S3 rejects a body that does not match the checksum, and answers 412 when the key exists, which Drop treats as "already stored". The server checks the declared size before sending.
- The agent downloads with `GetObject` (checksum validation on) into a temporary file, verifies size and SHA-256 itself and renames it into nibrunner's artifact store.
- Releases are never deleted in v0, so the policy grants no `s3:DeleteObject`.
- Bucket settings (made by the operator, M11): Block Public Access on, SSE-S3 default encryption, versioning on.

## Secrets in SSM

- An app secret is the parameter `<prefix>/apps/<record id>/<NAME>`, where the app id is `d-<record id>` and `NAME` matches `[A-Z][A-Z0-9_]{0,63}` (`awsx.SecretParameter`). The agent refuses a desired-state parameter that differs from this value.
- Values are standard-tier `SecureString` parameters (1 to 4096 bytes) encrypted with the account's AWS managed key `alias/aws/ssm`, so no KMS statement is needed.
- The server only writes: `PutParameter` with overwrite (each write is a new version, starting at 1) and `DeleteParameter` (all versions; a later write starts at 1 again). It records the name and the version, never the value. The server holds an `awsx.SecretWriter`, which has no read method.
- The agent only reads one exact version: `GetParameter` of `name:version` with decryption. SSM keeps the last 100 versions of a parameter.
- Errors and logs name parameters, never values.

## IAM

`deploy/walter-aws/iam-policy.json` is the policy of the one v0 role, with `BUCKET` and `ACCOUNT` to fill in. The role trusts `ec2.amazonaws.com` and is attached to walter-aws as an instance profile.

- `s3:PutObject` and `s3:GetObject` (which also covers `HeadObject`) on `BUCKET/releases/*`.
- `s3:ListBucket` on the bucket. Without it S3 answers 403 instead of 404 for a release that does not exist, and the agent could not tell a missing object from a denied one. It is not limited by `s3:prefix`: that condition key exists only on list requests, so a missing-object check would not match it and the 403 would come back (confirm with the live test in M11). The bucket holds only releases, whose names are digests.
- `ssm:PutParameter`, `ssm:DeleteParameter` and `ssm:GetParameter` on `parameter/drop/apps/*` in `eu-north-1`.

Later the role splits into `drop-control-plane` (S3 put, SSM put and delete) and `drop-node` (S3 get, SSM get).

Before attaching the instance profile, require IMDSv2 with hop limit 1 and block guests from `169.254.169.254` (nibrunner's egress denial does not cover it), then check from a guest that IMDS cannot be reached.

## Fakes

Tests and local runs never touch AWS:

- `awsx.NewMemoryStores()` gives in-memory stores for unit tests.
- `--fake-aws DIR` (`awsx.OpenFakeDir`) gives stores in plain files, so a server and an agent on one machine share them in the end-to-end test. It is refused unless the server listens on a loopback address (`awsx.CheckFakeAllowed`), or for the agent, unless its server URL is on loopback (`awsx.CheckFakeAllowedURL`), and its use is logged.

The directory layout:

```
DIR/blobs/releases/<sha256>          a release, 0600
DIR/secrets/<sha256 of name>/name    the parameter name, for people reading the tree
DIR/secrets/<sha256 of name>/<n>     version n of the value, 0600
```

Directories are 0700. Keys are validated (no `.`, `..` or empty segments) and every access goes through `os.Root`, so a symlink inside `DIR` cannot lead outside it. Writes go to a temporary file that is hard-linked into place, so a blob or a secret version is either complete or absent, and two processes never both create the same one.

## Tests

`internal/awsx/conformance_test.go` defines the contract and runs against both fakes in `make test`. The same suites run against real S3 and SSM only when asked:

```sh
DROP_LIVE_AWS=1 DROP_AWS_REGION=eu-north-1 DROP_S3_BUCKET=<bucket> \
  go test -tags "$(cat build-tags.txt)" -run TestLiveAWS -v ./internal/awsx
```

The live test uses the default credential chain, leaves a few small random objects under `releases/` (the policy grants no delete) and deletes the parameters it creates under a random `/drop/apps/<id>/`. Without `DROP_LIVE_AWS=1` it is skipped.
