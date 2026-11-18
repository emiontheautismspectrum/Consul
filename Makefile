SHELL := /bin/sh
COMPOSE      := docker compose
TAG          := $(shell date +%Y%m%d-%H%M)-$(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
IMAGE         = $(DOCKERHUB_USER)/consul-api
WEB_IMAGE_HUB = $(DOCKERHUB_USER)/consul-web
SSH           = ssh $(DEPLOY_USER)@$(DEPLOY_HOST)
-include .env
export


.PHONY: publish-web up down logs ps test test-v lint env certs smoke psql publish deploy vps-logs vps-ps vps-host demo-reset demo-reset-vps palata-seed palata-seed-vps palata-refresh palata-refresh-vps eval-chat-vps

## Local: one command to run everything (creates .env and a self-signed cert if missing)
up: env certs
	$(COMPOSE) up -d --build
	@echo "API: https://localhost/api/v1/docs  (self-signed certificate)"

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

## Reset Dilnoza's demo account to the reference ledger ("can spend" 4 200 000)
demo-reset:
	$(COMPOSE) exec -T api /api demo-reset

demo-reset-vps:
	$(SSH) 'cd $(DEPLOY_PATH) && docker compose exec -T api /api demo-reset'

## Chamber report: synthetic population (812, flagged synthetic) and recomputation
palata-seed:
	$(COMPOSE) exec -T api /api palata-seed

palata-seed-vps:
	$(SSH) 'cd $(DEPLOY_PATH) && docker compose exec -T api /api palata-seed'

palata-refresh:
	$(COMPOSE) exec -T api /api palata-refresh

palata-refresh-vps:
	$(SSH) 'cd $(DEPLOY_PATH) && docker compose exec -T api /api palata-refresh'

## Assistant quality check on the real model (50 questions). Pick a model: make eval-chat-vps MODEL=deepseek-chat
eval-chat-vps:
	$(SSH) 'cd $(DEPLOY_PATH) && docker compose exec -T $(if $(MODEL),-e AI_CHAT_MODEL=$(MODEL)) -e AI_CHAT_DEADLINE=$(or $(DEADLINE),20s) api /api eval-chat'

psql:
	$(COMPOSE) exec postgres psql -U $(POSTGRES_USER) $(POSTGRES_DB)

## Production: build & push the API image to Docker Hub (needs `docker login`)
publish: lint test
	@test -n "$(DOCKERHUB_USER)" || (echo "set DOCKERHUB_USER in .env" && exit 1)
	docker buildx build --platform linux/amd64 -t $(IMAGE):$(TAG) -t $(IMAGE):latest --push backend
	@echo "$(TAG)" > .last-tag
	@echo "published $(IMAGE):$(TAG)"

## Frontend: tests, typecheck, then the SSR image (Next.js standalone)
publish-web:
	@test -n "$(DOCKERHUB_USER)" || (echo "set DOCKERHUB_USER in .env" && exit 1)
	cd frontend && npm ci --no-audit --no-fund && npm test && npm run typecheck
	docker buildx build --platform linux/amd64 -t $(WEB_IMAGE_HUB):$(TAG) -t $(WEB_IMAGE_HUB):latest --push frontend
	@echo "$(TAG)" > .last-web-tag
	@echo "published $(WEB_IMAGE_HUB):$(TAG)"

## Production: ship ONLY config files to the VPS and roll out the published image.
## Usage: make publish deploy   (or `make deploy API_TAG=<tag>` to roll back)
deploy:
	@test -n "$(DEPLOY_HOST)" -a -n "$(DOCKERHUB_USER)" || (echo "set DEPLOY_HOST and DOCKERHUB_USER in .env" && exit 1)
	$(eval DEPLOY_TAG := $(if $(filter-out latest,$(API_TAG)),$(API_TAG),$(shell cat .last-tag 2>/dev/null || echo latest)))
	$(eval WEB_DEPLOY_TAG := $(shell cat .last-web-tag 2>/dev/null))
	$(SSH) 'mkdir -p $(DEPLOY_PATH)'
	tar -cz -C deploy/vps docker-compose.yml init-env.sh selfsigned.sh letsencrypt.sh \
	        -C ../.. .env.example \
	        -C deploy nginx/templates \
	  | $(SSH) 'tar -xz -C $(DEPLOY_PATH)'
	$(SSH) 'set -e; cd $(DEPLOY_PATH); \
	  ./init-env.sh $(if $(DEPLOY_DOMAIN),$(DEPLOY_DOMAIN),$(DEPLOY_HOST)) $(DOCKERHUB_USER); ./selfsigned.sh; \
	  sed -i "s|^API_TAG=.*|API_TAG=$(DEPLOY_TAG)|" .env; \
	  [ -n "$(WEB_DEPLOY_TAG)" ] && { grep -q "^WEB_TAG=" .env && sed -i "s|^WEB_TAG=.*|WEB_TAG=$(WEB_DEPLOY_TAG)|" .env || echo "WEB_TAG=$(WEB_DEPLOY_TAG)" >> .env; }; \
	  docker compose pull -q && docker compose up -d --remove-orphans && docker image prune -f >/dev/null; \
	  docker compose exec -T nginx sh -c "/docker-entrypoint.d/20-envsubst-on-templates.sh >/dev/null && nginx -t -q && nginx -s reload"; \
	  docker compose ps --format "table {{.Service}}\t{{.Status}}"'
	@echo "deployed $(IMAGE):$(DEPLOY_TAG) → https://$(if $(DEPLOY_DOMAIN),$(DEPLOY_DOMAIN),$(DEPLOY_HOST))"

vps-ps:
	$(SSH) 'cd $(DEPLOY_PATH) && docker compose ps'

vps-logs:
	$(SSH) 'cd $(DEPLOY_PATH) && docker compose logs -f --tail=100 api'

## Host hardening (sshd, nftables rate limits, fail2ban, journald cap). Safe to re-run:
## the firewall auto-rolls back in 3 min unless a new SSH session succeeds.
vps-host:
	tar -cz -C deploy/vps/host . | $(SSH) 'rm -rf /tmp/host && mkdir -p /tmp/host && tar -xz -C /tmp/host && /tmp/host/apply.sh'
	$(SSH) -o ConnectTimeout=10 'sudo systemctl stop fw-rollback.timer; rm -rf /tmp/host; sudo fail2ban-client status | tail -1; echo "new SSH session OK, rollback cancelled"'

## Market prices: fetch narx.stat.uz + recompute anonymous medians / insert the flagged sample bazaar prices
prices-refresh-vps:
	$(SSH) 'cd $(DEPLOY_PATH) && docker compose exec -T api /api prices-refresh'

prices-seed-vps:
	$(SSH) 'cd $(DEPLOY_PATH) && docker compose exec -T api /api prices-seed'

## Same, for when the VPS cannot reach narx.stat.uz: fetch from this machine, import on the server
prices-push-vps:
	cd backend && go run ./cmd/api prices-fetch | $(SSH) 'cd $(DEPLOY_PATH) && docker compose exec -T api /api prices-import'
