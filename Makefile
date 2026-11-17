SHELL := /bin/sh
COMPOSE      := docker compose
COMPOSE_PROD := docker compose -f docker-compose.yml -f docker-compose.prod.yml
-include .env
export

.PHONY: up down logs ps test test-v lint env certs smoke psql prod-up cert-init deploy

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

## Production (run on the VPS)
cert-init:
	./deploy/scripts/cert-init.sh

prod-up:
	$(COMPOSE_PROD) up -d --build

## Deploy from a dev machine: sync the repo to the VPS and restart the stack
deploy:
	@test -n "$(DEPLOY_HOST)" || (echo "set DEPLOY_HOST in .env" && exit 1)
	rsync -az --delete --exclude .git --exclude .env --exclude 'deploy/nginx/certs' \
		./ $(DEPLOY_USER)@$(DEPLOY_HOST):$(DEPLOY_PATH)/
	ssh $(DEPLOY_USER)@$(DEPLOY_HOST) 'cd $(DEPLOY_PATH) && make prod-up && docker image prune -f'
