BINARY     := vee
MODULE     := github.com/Benehiko/vee
INSTALL_DIR := $(HOME)/.vee/bin

GO        := go
GOFLAGS   := -mod=vendor
LDFLAGS   := -s -w

.PHONY: all build install clean

all: build

build:
	$(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BINARY) .

install: build
	mkdir -p $(INSTALL_DIR)
	cp $(BINARY) $(INSTALL_DIR)/$(BINARY)
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

clean:
	rm -f $(BINARY)
