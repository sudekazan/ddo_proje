.PHONY: up down logs verify reset-demo load explain test test-integration lint

COMPOSE := docker compose

.env:
	cp .env.example .env

up: .env ## Sistemi derleyip ayağa kaldırır
	$(COMPOSE) up --build -d
	@echo "Arayüz: http://localhost:3000  API: http://localhost:8080  Swagger: http://localhost:8080/docs"

down:
	$(COMPOSE) down

logs:
	$(COMPOSE) logs -f --tail=100

verify: .env ## Tüm özellikleri uçtan uca dener, PASS/FAIL tablosu basar
	$(COMPOSE) up --build -d
	$(COMPOSE) --profile verify run --rm verify

reset-demo: .env ## Verileri ilk seed haline döndürür
	$(COMPOSE) --profile tools run --rm reset
	$(COMPOSE) restart simulator

load: .env ## k6 yük testi (randevu motoru)
	$(COMPOSE) --profile load run --rm k6

explain: .env ## Kritik sorguların dizinli/dizinsiz EXPLAIN (ANALYZE) çıktısı
	$(COMPOSE) --profile tools run --rm reset /app/explain

test: ## Birim testleri (Docker gerekmez; entegrasyon testleri atlanır)
	cd backend && go test ./...

test-integration: ## Gerçek PostgreSQL 16 ile entegrasyon testleri
	$(COMPOSE) --profile test up -d --wait testdb
	cd backend && TEST_DATABASE_URL="postgres://postgres:postgres@localhost:55433/postgres?sslmode=disable" go test -count=1 -race ./...
	$(COMPOSE) --profile test rm -sf testdb

lint:
	cd backend && golangci-lint run ./...
