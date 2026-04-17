GO_MK_URL   := https://raw.githubusercontent.com/agoodkind/go-makefile/main/go.mk
GO_MK       := .make/go.mk
GO_MK_CACHE := $(HOME)/.cache/go-makefile/go.mk

$(GO_MK):
	@mkdir -p $(dir $@)
	@if curl -fsSL --connect-timeout 5 --max-time 10 "$(GO_MK_URL)" -o "$@"; then \
		mkdir -p "$(dir $(GO_MK_CACHE))" && cp "$@" "$(GO_MK_CACHE)"; \
	elif [ -f "$(GO_MK_CACHE)" ]; then \
		echo "warning: go.mk fetch failed, using cached version" >&2; \
		cp "$(GO_MK_CACHE)" "$@"; \
	else \
		echo "error: go.mk fetch failed and no cache available" >&2; \
		exit 1; \
	fi

-include $(GO_MK)

.PHONY: update-go-mk
update-go-mk:
	@mkdir -p "$(dir $(GO_MK))"
	@if curl -fsSL --connect-timeout 5 --max-time 10 "$(GO_MK_URL)" -o "$(GO_MK)"; then \
		mkdir -p "$(dir $(GO_MK_CACHE))" && cp "$(GO_MK)" "$(GO_MK_CACHE)"; \
		echo "go.mk updated"; \
	else \
		echo "error: go.mk fetch failed" >&2; \
		exit 1; \
	fi

.DEFAULT_GOAL := check

# Library module. No local build, deploy, or clean targets.
# The 'build' target comes from go.mk (runs 'go build ./...').
# Other targets (check, fmt, lint, test, vet, govulncheck) also come from go.mk.
