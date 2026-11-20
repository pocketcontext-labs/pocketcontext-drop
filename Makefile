# build-tags.txt is copied unchanged from pocketcontext-core: the SQLite features, plus tailscale.com tags Drop does not use.
TAGS := $(shell cat build-tags.txt)
VERSION ?= $(shell git describe --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/pocketcontext-labs/pocketcontext-drop/internal/buildinfo.Version=$(VERSION)
GOBUILD := go build -trimpath -tags $(TAGS) -ldflags "$(LDFLAGS)"

.PHONY: ui dev build test names identity e2e
ui:
	cd ui && pnpm install --frozen-lockfile && pnpm build
dev:
	node scripts/dev.mjs --db "$(DB)"
build:
	CGO_ENABLED=1 $(GOBUILD) -o bin/drop ./cmd/drop
test:
	CGO_ENABLED=1 go test -tags $(TAGS) ./...
	node --test tests/dev_launcher.test.mjs
	python3 -B -m unittest discover -s tests -p test_release.py
# The repository and the binary must not contain the retired product name.
names: build
	python3 tests/name_check.py --binary bin/drop
# No template identity may remain in the repository or the binary.
identity: build
	python3 tests/identity_check.py --binary bin/drop
e2e: names identity
	python3 tests/public_routes.py --binary bin/drop
	python3 tests/e2e.py --binary bin/drop
	python3 tests/drop_e2e.py --binary bin/drop
