# Crew backup and restore on SR24

On 28 November 2026 the Crew 0.45.4-drop.7 guest backup hooks restored a note and its binary attachment into a different Crew app on SR24. The development node reported zero managed instances and volumes before and after the drill.

The fixture booted three isolated Firecracker microVMs using the NibRunner kernel and root filesystem. The source and destination had distinct app IDs, hostnames, hook tokens and ext4 data volumes. Each VM had a vsock connection and no network adapter. Crew sign-in used its normal OTP routes with a synthetic vsock mailbox.

The test created the note and attached 8,192 random bytes through the authenticated Crew API. It downloaded an archive through the source hook, powered off the source, verified that the destination started empty, then uploaded the archive to the destination hook. It compared the restored note and attachment using Crew's authenticated read and download routes. It then powered off the destination and repeated both reads after booting a new VM from its volume.

| Measurement | Result |
| --- | --- |
| Source and destination | `d-e467fe8c4a68e76` → `d-f46624bb7ed96e9` |
| Crew binary SHA-256 | `f3f23dc08d6ef63cc3c2d575e428e9847ae09d448e111d34efba17d62084da6d` |
| Note ID | `p5sfhxzhwf01fft` |
| Note body SHA-256 before and after cold boot | `0673700153b06de46560357eee6282e6eebeee526cab29e736269c641c8f97fe` |
| Attachment SHA-256 before and after cold boot | `c4ade15ed70a14afcf691997af3e522aff0f2bc855d26075b7aa93e9da07c759` |
| Archive size and SHA-256 | 61,754 bytes · `6f536d96409050808ef615f78a7800d97f19805f6b8b2df90ce19aa63b99de94` |
| Guest capture / restore to ready / whole drill | 0.01 s / 2.32 s / 37.58 s |

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
