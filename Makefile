BINARY  := gohttp
CMD     := ./cmd/gohttp
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "0.1.0")
LDFLAGS := -ldflags "-X main.Version=$(VERSION)"

.PHONY: all build termux run test clean deps

all: build

build:
	@echo "→ Compilando $(BINARY)..."
	go build $(LDFLAGS) -o $(BINARY) $(CMD)
	@echo "✓ ./$(BINARY)"

termux: build
	cp $(BINARY) $(PREFIX)/bin/$(BINARY)
	@echo "✓ Instalado no Termux"

run: build
	./$(BINARY) :8080

test:
	go test ./... -v

clean:
	rm -f $(BINARY)
	go clean

deps:
	go mod tidy
	go mod download

help:
	@echo "make build    - compila"
	@echo "make termux   - instala no Termux"
	@echo "make run      - compila e executa em :8080"
	@echo "make test     - roda testes"
