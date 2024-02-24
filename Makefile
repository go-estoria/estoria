.PHONY: start-deps start-example down

start-deps:
	docker-compose up -d mongo mongo-express

start-example:
	docker-compose up --build example-app

down:
	docker-compose down -v
