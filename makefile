# Cross-platform Makefile for anchor, not tested...

# ------------------------------------------------------------

# Detect OS / binary suffix

# ------------------------------------------------------------

EXE :=
ifeq ($(OS),Windows_NT)
EXE := .exe
endif

BINARY := bin/anchor$(EXE)
SIMC   := bin/simc$(EXE)

SIMGO ?= simgo

BUILD_DIR   := build
STAGING_DIR := build/staging
DEPLOY_DIR  := build/deployed
SIMC_SRC    := .simc-src

GOFLAGS :=

# ------------------------------------------------------------

# Default

# ------------------------------------------------------------

.PHONY: all
all: build

# ------------------------------------------------------------

# Install simgo (Go → SimplicityHL transpiler)

# ------------------------------------------------------------

.PHONY: install-simgo
install-simgo:
go install github.com/0ceanslim/go-simplicity/cmd/simgo@latest

# ------------------------------------------------------------

# Install simc (patched SimplicityHL compiler)

# ------------------------------------------------------------

.PHONY: install-simc
install-simc:
@echo "Installing simc..."
@if ! command -v cargo >/dev/null 2>&1; then 
echo "ERROR: cargo not found. Install Rust: https://rustup.rs/"; 
exit 1; 
fi
@if [ -d "$(SIMC_SRC)/.git" ]; then 
echo "Updating SimplicityHL source..."; 
git -C $(SIMC_SRC) pull --ff-only; 
else 
echo "Cloning SimplicityHL..."; 
git clone https://github.com/0ceanslim/SimplicityHL $(SIMC_SRC); 
fi
@echo "Building simc..."
cargo build --release --manifest-path $(SIMC_SRC)/Cargo.toml
@mkdir -p bin
@if [ -f "$(SIMC_SRC)/target/release/simc$(EXE)" ]; then 
cp $(SIMC_SRC)/target/release/simc$(EXE) $(SIMC); 
elif [ -f "$(SIMC_SRC)/target/release/simplicity$(EXE)" ]; then 
cp $(SIMC_SRC)/target/release/simplicity$(EXE) $(SIMC); 
else 
echo "ERROR: simc binary not found in target/release"; 
exit 1; 
fi
@echo "Installed $(SIMC)"
@$(SIMC) --version || true

# ------------------------------------------------------------

# Update simc

# ------------------------------------------------------------

.PHONY: update-simc
update-simc: install-simc

# ------------------------------------------------------------

# Transpile Go contracts → SimplicityHL

# ------------------------------------------------------------

.PHONY: transpile
transpile:
mkdir -p $(BUILD_DIR)
$(SIMGO) -input contracts/pool_creation.go     -output build/pool_creation.shl
$(SIMGO) -input contracts/pool_a_swap.go       -output build/pool_a_swap.shl
$(SIMGO) -input contracts/pool_a_remove.go     -output build/pool_a_remove.shl
$(SIMGO) -input contracts/pool_b_swap.go       -output build/pool_b_swap.shl
$(SIMGO) -input contracts/pool_b_remove.go     -output build/pool_b_remove.shl
$(SIMGO) -input contracts/lp_reserve_add.go    -output build/lp_reserve_add.shl
$(SIMGO) -input contracts/lp_reserve_remove.go -output build/lp_reserve_remove.shl
@echo "Contracts transpiled to build/"

# ------------------------------------------------------------

# Run integration tests against staging contracts

# ------------------------------------------------------------

.PHONY: test-staging
test-staging:
mkdir -p $(STAGING_DIR)
ANCHOR_BUILD_DIR=$(CURDIR)/$(STAGING_DIR) 
ANCHOR_POOL_JSON=$(CURDIR)/$(STAGING_DIR)/pool.json 
go test -tags integration ./tests/... -v -timeout 1800s

# ------------------------------------------------------------

# Promote staging contracts → deployed

# ------------------------------------------------------------

.PHONY: promote
promote:
mkdir -p $(DEPLOY_DIR)
cp build/*.shl $(DEPLOY_DIR)/ || true
cp $(STAGING_DIR)/*.shl build/
@echo "Promoted staging contracts to build/"

# ------------------------------------------------------------

# Build CLI

# ------------------------------------------------------------

.PHONY: build
build:
mkdir -p bin
go build $(GOFLAGS) -o $(BINARY) ./cmd/anchor

# ------------------------------------------------------------

# Unit tests

# ------------------------------------------------------------

.PHONY: test
test:
go test ./pkg/...

# ------------------------------------------------------------

# Esplora integration

# ------------------------------------------------------------

.PHONY: test-esplora
test-esplora:
go test -tags esplora ./tests/... -v

# ------------------------------------------------------------

# Full integration (testnet)

# ------------------------------------------------------------

.PHONY: test-integration
test-integration:
go test -tags integration ./tests/... -v -timeout 60m

# ------------------------------------------------------------

# All tests

# ------------------------------------------------------------

.PHONY: test-all
test-all: test test-esplora test-integration

# ------------------------------------------------------------

# Clean

# ------------------------------------------------------------

.PHONY: clean
clean:
rm -f $(BINARY)

.PHONY: clean-all
clean-all: clean
rm -rf bin
rm -rf $(SIMC_SRC)
