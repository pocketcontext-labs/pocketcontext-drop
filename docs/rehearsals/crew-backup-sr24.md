# Crew backup and restore on SR24

On 28 November 2026 the Crew guest backup hooks restored a note and its binary attachment into a different Crew app on SR24. The development node reported zero managed instances and volumes before and after the drill.

The fixture booted three isolated Firecracker microVMs using the NibRunner kernel and root filesystem. The source and destination had distinct app IDs, hostnames, hook tokens and ext4 data volumes. Each VM had a vsock connection and no network adapter. Crew sign-in used its normal OTP routes with a synthetic vsock mailbox.

The test created the note and attached 8,192 random bytes through the authenticated Crew API. It downloaded an archive through the source hook, powered off the source, verified that the destination started empty, then uploaded the archive to the destination hook. It compared the restored note and attachment using Crew's authenticated read and download routes. It then powered off the destination and repeated both reads after booting a new VM from its volume.

| Measurement | Result |
| --- | --- |
| Source and destination | `d-894cda5820002cf` → `d-82f413a799ef59a` |
| Crew binary SHA-256 | `ec229602d38342b016e8a6a78da99e5a80bf70b7e1fd7346070a5fcb4a75fd94` |
| Note ID | `ztp1vdsoggrg27m` |
| Note body SHA-256 before and after cold boot | `6b01207a3e0abe31f9fdba8f00b5f7e72c17efe409e79dffe6d5bab3abe5e630` |
| Attachment SHA-256 before and after cold boot | `b02855d5e1da7efd79030e3ef133dbc3919005b38cd356946413f2478c32c6d4` |
| Archive size and SHA-256 | 61,754 bytes · `ccc95e7c6043740be15deb52b672fc38c1924a7b5eb917cbcd89afaf7f26c501` |
| Guest capture / restore to ready / whole drill | 0.01 s / 2.31 s / 37.57 s |

The drill covers the guest contract. The archive travelled through a temporary local file, independently of the platform job queue, S3 billing and dashboard.

## Reproduce

Build the Crew Drop executable from its repository with `bash drop/build.sh`. On an available Linux amd64 KVM host, provide a NibRunner guest image directory, Firecracker, static BusyBox at `/bin/busybox`, and e2fsprogs. From the Drop repository run the following commands with access to `/dev/kvm`. All fixture data is synthetic and removed at exit.

```sh
fixture_dir=$(mktemp -d)
CGO_ENABLED=0 go build -o "$fixture_dir/relay" ./tests/fixtures/archive-relay
python3 tests/crew_backup_kvm.py --crew "$CREW_BINARY" --firecracker "$FIRECRACKER_BINARY" --nib "$NIB_GUEST_DIR" --relay "$fixture_dir/relay"
rm -f "$fixture_dir/relay"
rmdir "$fixture_dir"
```
