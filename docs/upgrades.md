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
