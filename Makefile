BINARY     := vee
MODULE     := github.com/Benehiko/vee
INSTALL_DIR := $(HOME)/.vee/bin

GO        := go
GOFLAGS   := -mod=vendor

# Version metadata injected into the binary at build time. Override on the
# command line for release builds (e.g. `make build VERSION=v0.4.0`).
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT    ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE      ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

VERSION_PKG := $(MODULE)/cmd
LDFLAGS     := -s -w \
  -X $(VERSION_PKG).version=$(VERSION) \
  -X $(VERSION_PKG).commit=$(COMMIT) \
  -X $(VERSION_PKG).date=$(DATE)

CONTAINER_RUNTIME := $(shell command -v nerdctl 2>/dev/null || command -v docker 2>/dev/null)
HUGO_IMAGE        := hugomods/hugo:go-git-0.147.0

.PHONY: all build build-windows install clean e2e site lint fmt test hooks licenses

all: build

build:
	$(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BINARY) .

# Cross-compile the Windows (WHPX/x86_64) binary. This is a build-only target —
# the `install` target below is POSIX-shell and unix-only, so Windows users run
# the produced vee.exe directly. Runs from any host with the Go toolchain.
build-windows:
	GOOS=windows GOARCH=amd64 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BINARY).exe .

# Mirror the CI lint job locally: format check (gofumpt + goimports) then lint.
lint:
	golangci-lint fmt --diff
	golangci-lint run --timeout=5m ./...

# Apply formatters in place (gofumpt + goimports).
fmt:
	golangci-lint fmt

test:
	$(GO) test $(GOFLAGS) -race ./...

# Enable the tracked git hooks (pre-commit lint + build) for this clone.
hooks:
	git config core.hooksPath .githooks
	@echo "git hooks enabled (core.hooksPath=.githooks)"

install: build
	mkdir -p $(INSTALL_DIR)
	install -m 755 $(BINARY) $(INSTALL_DIR)/$(BINARY)
	@echo "Installed to $(INSTALL_DIR)/$(BINARY)"
	@$(MAKE) --no-print-directory _add_to_path

_add_to_path:
	@# Check if already on PATH
	@case ":$$PATH:" in \
	  *:$(INSTALL_DIR):*) \
	    echo "$(INSTALL_DIR) already in PATH — nothing to do."; \
	    exit 0 ;; \
	esac; \
	\
	# Collect all known shell rc files that exist (all shells, not just login shell) \
	CANDIDATES=" \
	  $(HOME)/.bashrc \
	  $(HOME)/.bash_profile \
	  $(HOME)/.zshrc \
	  $(HOME)/.zprofile \
	  $(HOME)/.config/fish/config.fish \
	  $(HOME)/.profile \
	"; \
	EXISTING=""; \
	for f in $$CANDIDATES; do \
	  if [ -f "$$f" ]; then EXISTING="$$EXISTING $$f"; fi; \
	done; \
	\
	# Present selection menu \
	echo ""; \
	echo "$(INSTALL_DIR) is not in your PATH."; \
	echo "Select a shell config file to add it to (or 0 to skip):"; \
	echo ""; \
	I=1; FILE_LIST=""; \
	for f in $$EXISTING; do \
	  echo "  $$I) $$f"; \
	  FILE_LIST="$$FILE_LIST $$f"; \
	  I=$$((I+1)); \
	done; \
	echo "  $$I) Enter path manually"; \
	echo "  0) Skip"; \
	echo ""; \
	printf "Choice: "; \
	read CHOICE; \
	\
	if [ "$$CHOICE" = "0" ]; then \
	  echo "Skipped. Add the following line to your shell config manually:"; \
	  echo "  For bash/zsh:  export PATH=\"$(INSTALL_DIR):\$$PATH\""; \
	  echo "  For fish:      fish_add_path $(INSTALL_DIR)"; \
	  exit 0; \
	fi; \
	\
	TOTAL=$$((I)); \
	if [ "$$CHOICE" = "$$TOTAL" ]; then \
	  printf "Path to shell config file: "; \
	  read RCFILE; \
	else \
	  J=1; RCFILE=""; \
	  for f in $$FILE_LIST; do \
	    if [ "$$J" = "$$CHOICE" ]; then RCFILE="$$f"; fi; \
	    J=$$((J+1)); \
	  done; \
	fi; \
	\
	if [ -z "$$RCFILE" ]; then \
	  echo "Invalid choice. Skipped."; \
	  exit 0; \
	fi; \
	\
	# Append PATH line — use fish_add_path syntax for config.fish \
	if grep -qF "$(INSTALL_DIR)" "$$RCFILE" 2>/dev/null; then \
	  echo "$(INSTALL_DIR) already referenced in $$RCFILE — skipped."; \
	else \
	  case "$$RCFILE" in \
	    */fish/config.fish) \
	      LINE="fish_add_path $(INSTALL_DIR)"; \
	      RELOAD="fish_add_path takes effect immediately in new sessions" ;; \
	    *) \
	      LINE='export PATH="$(INSTALL_DIR):$$PATH"'; \
	      RELOAD="source $$RCFILE" ;; \
	  esac; \
	  echo "" >> "$$RCFILE"; \
	  echo "# Added by vee install" >> "$$RCFILE"; \
	  printf '%s\n' "$$LINE" >> "$$RCFILE"; \
	  echo "Added to $$RCFILE"; \
	  echo "Reload with:  $$RELOAD"; \
	fi

# Regenerate THIRD_PARTY_LICENSES from the vendored module LICENSE files.
# Run after `go mod vendor` whenever dependencies change.
licenses:
	@{ \
	  count=$$(find vendor \( -name LICENSE -o -name LICENSE.md -o -name COPYING -o -name LICENSE.txt \) | wc -l); \
	  printf '%s\n' "THIRD-PARTY LICENSES"; \
	  printf '%s\n' "===================="; \
	  printf '\n'; \
	  printf '%s\n' "vee is distributed under the MIT License (see the LICENSE file)."; \
	  printf '\n'; \
	  printf '%s\n' "vee vendors third-party Go modules under vendor/. This file reproduces the"; \
	  printf '%s\n' "license and copyright notices of those modules, as required by their"; \
	  printf '%s\n' "respective licenses (MIT, BSD-3-Clause, and Apache-2.0). It is assembled"; \
	  printf '%s\n' "directly from the LICENSE files shipped in each vendored module."; \
	  printf '\n'; \
	  printf '%s\n' "It covers only the Go build dependencies. External programs that vee invokes"; \
	  printf '%s\n' "at runtime (QEMU, OVMF/edk2, virtiofsd, swtpm, and any guest OS images the"; \
	  printf '%s\n' "user chooses to download) are not distributed with vee and are licensed"; \
	  printf '%s\n' "separately by their own vendors."; \
	  printf '\n'; \
	  printf '%s\n' "To regenerate after changing dependencies: run 'go mod vendor && make licenses'."; \
	  printf '\n'; \
	  printf '%s\n' "$$count vendored modules are listed below."; \
	  printf '\n\n'; \
	  find vendor \( -name LICENSE -o -name LICENSE.md -o -name COPYING -o -name LICENSE.txt \) | sort | while read -r f; do \
	    mod=$$(dirname "$$f" | sed 's|^vendor/||'); \
	    printf '%s\n' "================================================================================"; \
	    printf '%s\n' "$$mod"; \
	    printf '%s\n' "================================================================================"; \
	    printf '\n'; \
	    cat "$$f"; \
	    printf '\n\n'; \
	  done; \
	} > THIRD_PARTY_LICENSES
	@echo "Wrote THIRD_PARTY_LICENSES"

e2e: build
	VEE_E2E=1 VEE_BIN=$(CURDIR)/$(BINARY) $(GO) test $(GOFLAGS) -v -timeout 20m -tags e2e ./e2e/...

site:
	$(CONTAINER_RUNTIME) run --rm \
		-v $(CURDIR)/site:/src \
		-p 1313:1313 \
		$(HUGO_IMAGE) \
		hugo server --bind 0.0.0.0 --baseURL http://localhost:1313/vee/

clean:
	rm -f $(BINARY)
