.PHONY: install test run paper status logs build deploy stop

install:
	pip install -e ".[dev]"

test:
	pytest --cov=polywin --cov-report=term-missing

run:
	polywin run

paper:
	polywin run --paper

status:
	polywin status

logs:
	tail -f logs/polywin.log

build:
	docker-compose build

deploy:
	docker-compose up -d

stop:
	docker-compose down

derive-creds:
	polywin derive-creds
