SHELL := /bin/sh
COMPOSE      := docker compose
TAG          := $(shell date +%Y%m%d-%H%M)-$(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
IMAGE         = $(DOCKERHUB_USER)/finadvisor-api
SSH           = ssh $(DEPLOY_USER)@$(DEPLOY_HOST)
-include .env
export

.PHONY: up down logs ps test test-v lint env certs smoke psql publish deploy vps-logs vps-ps vps-host

## Local: one command to run everything (creates .env and a self-signed cert if missing)
up: env certs
	$(COMPOSE) up -d --build
	@echo "API: https://localhost/api/docs  (self-signed certificate)"

down:
	$(COMPOSE) down

logs:
	$(COMPOSE) logs -f --tail=100 api

ps:
	$(COMPOSE) ps

test:
	cd backend && go test ./...

test-v:
	cd backend && go test -v ./internal/calculator/...

lint:
	cd backend && go vet ./... && test -z "$$(gofmt -l .)"

env:
	@./deploy/scripts/gen-env.sh

certs:
	@./deploy/scripts/selfsigned.sh

smoke:
	./scripts/smoke.sh $${URL:-https://localhost}

psql:
	$(COMPOSE) exec postgres psql -U $(POSTGRES_USER) $(POSTGRES_DB)

## Production: build & push the API image to Docker Hub (needs `docker login`)
publish: lint test
	@test -n "$(DOCKERHUB_USER)" || (echo "set DOCKERHUB_USER in .env" && exit 1)
	docker buildx build --platform linux/amd64 -t $(IMAGE):$(TAG) -t $(IMAGE):latest --push backend
	@echo "$(TAG)" > .last-tag
	@echo "published $(IMAGE):$(TAG)"

## Production: ship ONLY config files to the VPS and roll out the published image.
## Usage: make publish deploy   (or `make deploy API_TAG=<tag>` to roll back)
deploy:
	@test -n "$(DEPLOY_HOST)" -a -n "$(DOCKERHUB_USER)" || (echo "set DEPLOY_HOST and DOCKERHUB_USER in .env" && exit 1)
	$(eval DEPLOY_TAG := $(if $(filter-out latest,$(API_TAG)),$(API_TAG),$(shell cat .last-tag 2>/dev/null || echo latest)))
	$(SSH) 'mkdir -p $(DEPLOY_PATH)'
	tar -cz -C deploy/vps docker-compose.yml init-env.sh selfsigned.sh letsencrypt.sh \
	        -C ../.. .env.example \
	        -C deploy nginx/templates \
	  | $(SSH) 'tar -xz -C $(DEPLOY_PATH)'
	$(SSH) 'set -e; cd $(DEPLOY_PATH); \
	  ./init-env.sh $(DEPLOY_HOST) $(DOCKERHUB_USER); ./selfsigned.sh; \
	  sed -i "s|^API_TAG=.*|API_TAG=$(DEPLOY_TAG)|" .env; \
	  docker compose pull -q && docker compose up -d --remove-orphans && docker image prune -f >/dev/null; \
	  docker compose ps --format "table {{.Service}}\t{{.Status}}"'
	@echo "deployed $(IMAGE):$(DEPLOY_TAG) → https://$(DEPLOY_HOST)"

vps-ps:
	$(SSH) 'cd $(DEPLOY_PATH) && docker compose ps'

vps-logs:
	$(SSH) 'cd $(DEPLOY_PATH) && docker compose logs -f --tail=100 api'

## Host hardening (sshd, nftables rate limits, fail2ban, journald cap). Safe to re-run:
## the firewall auto-rolls back in 3 min unless a new SSH session succeeds.
vps-host:
	tar -cz -C deploy/vps/host . | $(SSH) 'rm -rf /tmp/host && mkdir -p /tmp/host && tar -xz -C /tmp/host && /tmp/host/apply.sh'
	$(SSH) -o ConnectTimeout=10 'sudo systemctl stop fw-rollback.timer; rm -rf /tmp/host; sudo fail2ban-client status | tail -1; echo "new SSH session OK, rollback cancelled"'
