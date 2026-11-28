# Upgrades

## From 0.2.0 to 0.3.0

0.3.0 adds S3 usage metering and the Crew replica sync interval ([release notes](releases/0.3.0.md)).

1. Back up the Drop project and keep the 0.2.0 server and agent binaries. Migration `1820001700_usage.js` cannot be rolled back: a rollback restores the pre-upgrade backup (follow the hosted restore procedure in [Hosting](hosting.md)).
2. **Upgrade the agents first.** A 0.2.0 agent refuses a whole desired document that carries the new `replicaSyncSeconds` field and keeps running what it has. Existing Crew apps carry no interval until one is set, and new Crew apps get `10s` at creation, so the field appears only after the server upgrade; upgrading every node's agent before the server avoids any refused document. On drop-ax41, where server and agent share the host, install both binaries, then restart the agent before the server.
3. Grant the server's AWS identity the metering permissions (on drop-ax41 the stack `access-drop-ax41`, `deploy/drop-ax41/access.yml`): `s3:ListBucket` and `s3:GetObject` on the log bucket, and `s3:PutMetricsConfiguration`, `s3:GetMetricsConfiguration` and `s3:ListBucketMetricsConfigurations` on the replica bucket. Check that the replica bucket delivers partitioned server access logs (and, for stored bytes, a daily CSV inventory of all versions with the Size field) to the log bucket.
4. Set `DROP_USAGE_LOG_BUCKET`, `DROP_USAGE_LOG_PREFIX` and, optionally, `DROP_USAGE_INVENTORY_PREFIX` on the server ([Hosting](hosting.md#usage-metering)), restart it and run `drop usage collect` as the owner. Without the variables nothing is metered and the Usage tab says so.
5. The first run reads the last three days of logs and starts counting from them; the month to date of the upgrade month is therefore partial. Existing apps keep the default limits. Existing Crew apps keep the guest's own sync interval until their owner sets one; the first collector run creates their metrics filters (one `app.metrics_filter` event each).

## From 0.3.0 to 0.4.0

0.4.0 adds the node's S3 proxy for managed Crew apps ([release notes](releases/0.4.0.md)).

1. Back up the Drop project and keep the 0.3.0 binary. Migration `1820001900_host_usage.js` cannot be rolled back.
2. Upgrade the agents before the server. Older agents refuse a desired document carrying the new `limits` field. On drop-ax41, where server and agent share the host, install the binary, then restart the agent before the server.
3. To turn on host services on a node:
   - Install the NibRunner release with `hostServices`. Add `[host_services] socket = "/run/drop-agent/host-services.sock"` to its config and restart nibrunnerd.
   - Install `drop-agent-host-services.service` and start it. It reads the agent's environment and needs `DROP_IDENTITY_*` and `DROP_AWS_REGION`.
   - Deploy a Crew for Drop guest that supports the endpoint (0.45.4-drop.3 or later) to every managed Crew app on the node.
   - Last, set `DROP_AGENT_HOST_SERVICES=s3` in the agent environment and restart the agent.
4. Check that the Crew apps' nibrunner entries carry `hostServices.s3` and no `AWS_WORKLOAD_IDENTITY`, that their token files are gone, and that their writes still reach the replica bucket.

## From 0.4.0 to 0.5.0

1. Back up the hosted project before applying the Beta catalog and terms migrations. Keep the old binary and follow the hosted restore procedure if reverting the database.
2. Upgrade the node agents. Install the NibRunner release with the manifest probe on the inspection host and its matching guest image. Nodes offering fractional sizes must report support for quarter-core CPU quotas.
3. Publish and configure the verified terms and privacy notice before admitting Beta customers. Set `DROP_BETA_NODES` to eligible nodes with tenant isolation, managed Crew storage and sign-in email configured. The operator-only drop-ax41 host is ineligible while SMT is active.
4. Admit the independently verified Crew release digest and run the [release inspection](ops.md#beta-release-admission-and-deployment). Check an invited user's account activation, upload and HTTPS deployment on an eligible host.

## From 0.5.0 to 0.5.1

Back up the hosted project, replace the Drop executable with the verified release, and restart the services that use it. This patch uses the 0.5.0 database schema and NibRunner resource contract. Existing app allocations carry over. The new size selection applies when preparing a new Crew deployment.

## From 0.5.1 to 0.6.0

Stop the control plane cleanly and back up the project and its previous executable before upgrading. This release adds waitlist and mail records. To roll back, use the pre-upgrade database with the previous executable and follow the hosted recovery instructions in [Hosting](hosting.md).

Keep the existing SMTP and service terms configuration. Verify that the public waitlist terms and privacy notice open before sign-in and that the host has working SMTP delivery. The approved waitlist documents are versioned separately from service terms. SMTP acceptance means that the provider accepted the message for delivery.

Existing owner-admin apps continue serving. Deployment and app changes must come from a user account. This release does not transfer ownership or alter the app's own sign-in identity. No node or guest restart is required for these control-plane changes.

## From 0.6.2 to 0.7.0

1. Stop the server and take a consistent backup of its project, including attachments. Record the original hosted owner ID and binary versions. Migration `1820002400_credits.js` adds private accounting tables. Restore the complete pre-upgrade backup with the previous binary to roll back.
2. Existing accounts enter the same zero-credit, 3,600-awake-second trial as new accounts. Existing archived apps and their volumes count toward the 10 GiB allowance. The migration advances the deployment sequence once for each managed app with an active release so its guest restarts with the local-only environment. Ordinary deployments that previously selected a guest platform identity also restart once. The agent clears their legacy role and token files before connecting to the server. Existing STS sessions expire under the AWS session policy. Each app keeps its volume ID, replica prefix, release and ownership. Accounts already over the allowance can keep their data and reduce usage. The historical `app_quota` field remains readable for old clients and has no effect on placement.
3. Upgrade deployed Crew binaries to a release that can run on local disk without S3 **before** changing the control plane's storage policy. An older binary requiring replication cannot start on a trial deployment. Verify the upgraded guest serves its existing local database. To keep a workload replicated, fund its owner account and redeploy it on a node with the S3 proxy enabled.
4. For a mixed-version upgrade, keep the new optional `awakeCounter` out of reports to old servers. Upgrade the server and all Drop agents together before enabling the new runner counter. Check the new agent reads the desired document and the server accepts its reports. A funded managed app receives only the host S3 proxy. A trial managed app runs on its retained disk.
5. Configure the exchange rate and prices via the admin `credits.configure` operation, then verify a simulated purchase, its ledger entry and each app's storage state. After a full day on the new runner, compare matching sample and host counter intervals as described in the agent API.

## From 0.7.0 to 0.8.0

Back up the hosted project before starting the new server. The archive provenance and private setup migrations change the database schema. Keep the old binaries and the pre-upgrade backup for rollback. Upgrade the server first, then all Drop agents so the status page can receive the current deployment’s first-run details. Use an invited test account to open a pinned catalog link, verify HTTP readiness on the assigned hostname, and check its private setup action.

## From 0.8.0 to 0.9.0

1. Inventory the existing apps, placements, release digests and volume IDs. Keep the previous executable and a verified, stopped backup of the project and its matching private cache. Downgrading a migrated database requires that backup.
2. Upgrade the control plane, then the agents. Install the host-services unit with the node credential on storage-capable nodes. Check that each node has pulled the current desired document and reported successfully. Enable official apps only on eligible nodes with the required guest services.
3. Stage the exact published Crew and Atlas Linux executables in Releases. To adopt an existing supported managed Crew app, register and publish its verified installed version first. Record the actual fixture hashes and immutable evidence for each candidate and its baseline. Publish the two current versions and enable the official catalog. Existing apps keep their binaries, addresses and volumes until their owners upgrade.
4. Verify a fresh app, an existing app, funding transitions, recovery, per-account visibility and the current release offered to owners. Confirm control-plane replication with a scratch restore before removing obsolete release staging.

## From 0.9.0 to 0.10.0

1. Inventory the existing apps, placements, release digests and volume IDs. Keep the previous executables and a verified, stopped backup of the project and its matching private cache. The nineteen new migrations change the database schema, so a downgrade requires that backup.
2. Upgrade the control plane, then the agents and their host services. To deliver guest sign-in codes, give `drop-agent-host-services.service` the SMTP credential and switch NibRunner's host-services socket as described in [the agent manual](agent.md). Upgrade the agent on every node serving custom apps before using direct links to them ([Opening custom apps](private-ingress.md#node-upgrades)). Set `DROP_BACKUP_BUCKET` on the control plane to offer Crew backups.
3. Check that each node has pulled the current desired document and reported successfully, and that every app kept its ID, owner, volume, placement and release. Confirm control-plane replication with a scratch restore.
