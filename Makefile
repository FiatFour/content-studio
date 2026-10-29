export PATH := $(HOME)/.local/go/bin:$(PATH)

.PHONY: dev db test web-test build smoke fmt vet lora-train lora-train-check backup restore-test restore-test-fixture

dev:
	./scripts/dev.sh

db:
	docker start studio-pg >/dev/null 2>&1 || docker run -d --name studio-pg -e POSTGRES_USER=studio -e POSTGRES_PASSWORD=studio \
		-e POSTGRES_DB=studio -p 127.0.0.1:5433:5432 -v studio-pgdata:/var/lib/postgresql/data postgres:17

fmt:
	gofmt -l $$(git ls-files '*.go' 2>/dev/null || find . -name '*.go' -not -path './spikes/*')

vet:
	go vet ./...

test: db
	go test ./...
	cd web && npm test

build:
	go build -o bin/ ./cmd/...
	cd web && npm run build

# Runs one real episode against the live local models. Never part of CI. Needs `make dev` running.
smoke:
	./scripts/smoke.sh

# Manual per-character LoRA training (D8). Never part of test/CI. Exclusive GPU, ~15 min.
# Usage: make lora-train CHAR=<slug> [TRIGGER_TOKEN=<token>]. See docs/lora-training.md.
# CHAR and TRIGGER_TOKEN reach the script as environment variables (no shell interpolation).
lora-train:
	./scripts/lora/train.sh

# Validates inputs only; does not train.
lora-train-check:
	./scripts/lora/train.sh --check

# Backup (pg_dump + storage sync + secret-free config, with retention) and the restore test. Never part of `make test`.
# Environment: STUDIO_BACKUP_ROOT (default ~/studio-backups, must be outside the storage root). See docs/backup-restore.md.
backup: db
	./scripts/backup/backup.sh

# Restores the newest backup set into a temporary database and storage dir, starts the server on a spare port,
# and verifies the approved video hash. Touches nothing live.
restore-test: db
	./scripts/backup/restore-test.sh

# Same, from a throwaway fixture source (approved episode with a 1 s test video); needs no existing backup.
restore-test-fixture: db
	./scripts/backup/restore-test.sh --fixture
