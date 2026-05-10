# Lint is centralized in go-makefile. Do NOT define project-local lint,
# deadcode, audit, fmt, vet, or staticcheck targets here. They duplicate
# the central pipeline and let agents bypass strict rules. Run `make help`
# for the canonical entry points (build/check/lint/fmt) and per-linter
# sub-targets (lint-golangci, lint-format, lint-gocyclo, lint-deadcode,
# staticcheck-extra). Refresh baselines via the matching *-baseline target.
#
# gklog Makefile.
# Library-mode: build/install are no-ops. Lint, vet, and test still run.
# Pipeline lives in go-makefile and is fetched at runtime.

# Library mode: no binary. Consumers stamp gklog version metadata through
# their normal build pipeline.
LIBRARY := 1
GKLOG_VPKG := goodkind.io/gklog/version

# Repo-local validation must compile the guarded version package and stamp
# values for tests because library mode does not produce a binary.
GOFLAGS := $(strip $(GOFLAGS) -tags=gklog_stamped)
export GOFLAGS

GKLOG_TEST_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo test)
GKLOG_TEST_DIRTY := $(shell git diff --quiet 2>/dev/null && echo false || echo true)
GKLOG_TEST_BUILD_TIME := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
GKLOG_TEST_LDFLAGS := -X $(GKLOG_VPKG).Commit=$(GKLOG_TEST_COMMIT) -X $(GKLOG_VPKG).Dirty=$(GKLOG_TEST_DIRTY) -X $(GKLOG_VPKG).BuildTime=$(GKLOG_TEST_BUILD_TIME) -X $(GKLOG_VPKG).BinHash=
GO_TEST_TARGETS := -ldflags '$(GKLOG_TEST_LDFLAGS)' ./...
DEADCODE_TARGETS := -tags gklog_stamped ./...

# Pipeline modules
GO_MK_MODULES := go-build.mk

include bootstrap.mk

.DEFAULT_GOAL := check
