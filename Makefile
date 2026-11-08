.PHONY: run up down logs test test-integration lint

up:            ## build and start bot + postgres + redis
	docker compose up -d --build

down:
	docker compose down

logs:
	docker compose logs -f bot

run:           ## run locally (needs .env and running postgres/redis)
	set -a; . ./.env; set +a; go run ./cmd/bot

test:
	go test -race ./...

test-integration:  ## spins up throwaway postgres + redis in docker
	docker run -d --rm --name tb-pg -e POSTGRES_PASSWORD=test -p 55432:5432 postgres:16-alpine
	docker run -d --rm --name tb-redis -p 56379:6379 redis:7-alpine
	sleep 3
	TEST_DATABASE_URL="postgres://postgres:test@localhost:55432/postgres?sslmode=disable" \
	TEST_REDIS_URL="redis://localhost:56379/0" \
	go test -race -count=1 -p 1 -tags integration ./... ; \
	status=$$?; docker rm -f tb-pg tb-redis >/dev/null; exit $$status

lint:
	go vet ./...
