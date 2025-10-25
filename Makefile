.PHONY: info utest lint
.DEFAULT_GOAL := info

# Show available targets.
info:
	@echo "Targets:"
	@echo "  lint     Run linters"
	@echo "  utest    Run unit tests"

# Run unit tests.
utest: _require_go
	go test -cover ./...

# Run linters.
# See https://golangci-lint.run/usage/linters/ for details on specific linters.
lint: _require_golangci_lint
	golangci-lint run

#
# dependencies
#

_require_go:
ifneq (, $(shell which go))
	@true
else
	@echo "This target reqires Go: https://golang.org/doc/install"
	@false
endif

_require_golangci_lint:
ifneq (, $(shell which golangci-lint))
	@true
else
	@echo "This target reqires golangci-lint: https://golangci-lint.run/usage/install/"
	@false
endif
