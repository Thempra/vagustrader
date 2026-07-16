.PHONY: build run paper status dashboard deploy stop

build:
	docker compose build

run:
	docker compose up -d bot

paper:
	docker compose run --rm bot vagustrader run --paper

status:
	docker compose exec bot vagustrader status

dashboard:
	docker compose up -d dashboard

deploy:
	docker compose up -d --build

stop:
	docker compose down

derive-creds:
	docker compose run --rm bot vagustrader derive-creds

simulate-settlement:
	docker compose exec bot vagustrader simulate-settlement
