.DEFAULT_GOAL := all

TARGET     := tunnel-client
OS         := $(if $(GOOS),$(GOOS),$(shell go env GOOS))
ARCH       := $(if $(GOARCH),$(GOARCH),$(shell go env GOARCH))
GOARM      := $(if $(GOARM),$(GOARM),)
GO_PACKAGE := ./cmd/client
BIN         = bin/$(OS)_$(ARCH)$(if $(GOARM),v$(GOARM),)/$(TARGET)
ADMIN_UI_DIR := adminui
ADMIN_UI_ASSETS_DIR := pkg/adminui/assets
ADMIN_UI_BUILD_SCRIPT := scripts/build_admin_ui.sh
PNPM       ?= pnpm
ADMIN_UI_PNPM_FLAGS := --config.shared-workspace-lockfile=false --config.confirmModulesPurge=false
ADMIN_UI_PNPM_STORE_DIR ?= $(if $(TMPDIR),$(TMPDIR),/tmp)/tunnel-client-adminui-pnpm-store
ifeq ($(OS),windows)
  BIN = bin/$(OS)_$(ARCH)$(if $(GOARM),v$(GOARM),)/$(TARGET).exe
endif
STABLE_BIN := bin/$(TARGET)
ifeq ($(OS),windows)
  STABLE_BIN = bin/$(TARGET).exe
endif
ABS_BIN := $(abspath $(BIN))

GIT_SHA    := $(if $(GIT_SHA),$(GIT_SHA),$(shell git rev-parse --short HEAD 2>/dev/null))
LDFLAGS    := -X go.openai.org/api/tunnel-client/pkg/version.GitSHA=$(GIT_SHA)

.PHONY: all help fmt test clean build-image mod-tidy admin-ui admin-ui-test release-source-version release-tag end-user-guide-screenshots end-user-guide-html end-user-guide-slides

all: clean mod-tidy fmt test $(TARGET)

help:
	@echo "Available targets:"
	@echo "  all           - Build the tunnel-client binary (default)"
	@echo "  mod-tidy      - Run go mod tidy and fail if go.mod/go.sum change"
	@echo "  fmt           - Run go fmt and fail if files are modified"
	@echo "  $(TARGET)     - Build the tunnel-client binary"
	@echo "  test          - Run Go and admin UI tests"
	@echo "  admin-ui      - Build the admin UI assets (manual; not part of make all)"
	@echo "  admin-ui-test - Run admin UI tests"
	@echo "  end-user-guide-screenshots - Capture the local /ui screenshots used by the shareable guide"
	@echo "  end-user-guide-html - Render docs/end-user-guide.md to a standalone HTML archive"
	@echo "  end-user-guide-slides - Render docs/end-user-guide.md to a local .pptx deck for on-demand slide import/distribution"
	@echo "  release-source-version - Write VERSION into pkg/version/VERSION before creating a release tag"
	@echo "  release-tag   - Generate a release tag like v1.2.3--ember-orchid"
	@echo "  clean         - Remove built binaries"
	@echo "  build-image   - Build Docker image with tunnel-client binary"
	@echo ""
	@echo "Docker image build options:"
	@echo "  make build-image                   # Build with git short SHA tag (default)"
	@echo "  GIT_SHA=v1.0.0 make build-image    # Build with specific tag"
	@echo ""
	@echo "Environment variables:"
	@echo "  GOOS         - Target OS (default: $(OS))"
	@echo "  GOARCH       - Target architecture (default: $(ARCH))"
	@echo "  GIT_SHA      - Git SHA/tag for version info and Docker tagging"
	@echo "  VERSION      - Version for make release-tag (required)"
	@echo "  WORD         - Required release word for make release-tag"
	@echo ""
	@echo "Artifacts:"
	@echo "  $(STABLE_BIN) -> $(BIN)"

test: admin-ui-test
	go test -race ./...

mod-tidy:
	go mod tidy
	@if git rev-parse --is-inside-work-tree >/dev/null 2>&1; then \
		git diff --exit-code -- go.mod go.sum || { \
			echo "go mod tidy updated go.mod/go.sum; please commit changes."; \
			exit 1; \
		}; \
	fi

fmt:
	@before=$$(mktemp); \
	after=$$(mktemp); \
	git diff -- . > $$before; \
	go fmt ./...; \
	git diff -- . > $$after; \
	if ! cmp -s $$before $$after; then \
		echo "go fmt updated files; please commit formatting changes."; \
		rm -f $$before $$after; \
		exit 1; \
	fi; \
	rm -f $$before $$after

admin-ui:
	./$(ADMIN_UI_BUILD_SCRIPT) $(ADMIN_UI_DIR) $(ADMIN_UI_ASSETS_DIR)
	@echo "Admin UI assets copied to $(abspath $(ADMIN_UI_ASSETS_DIR))"

admin-ui-test:
	$(PNPM) --dir $(ADMIN_UI_DIR) install --frozen-lockfile $(ADMIN_UI_PNPM_FLAGS) --store-dir $(ADMIN_UI_PNPM_STORE_DIR)
	$(PNPM) --dir $(ADMIN_UI_DIR) test

end-user-guide-screenshots:
	./scripts/capture_end_user_guide_screenshots.sh

end-user-guide-html:
	./scripts/render_end_user_guide_html.sh

end-user-guide-slides:
	./scripts/render_end_user_guide_slides.sh

release-source-version:
	@if [ -z "$(VERSION)" ]; then \
		echo "usage: make release-source-version VERSION=1.2.3"; \
		exit 1; \
	fi
	@./scripts/release_tag.sh set-source-version "$(VERSION)"

release-tag:
	@if [ -z "$(VERSION)" ] || [ -z "$(WORD)" ]; then \
		echo "usage: make release-tag VERSION=1.2.3 WORD=ember-orchid"; \
		exit 1; \
	fi
	@./scripts/release_tag.sh check-source-version "$(VERSION)"
	@./scripts/release_tag.sh make "$(VERSION)" "$(WORD)"

$(TARGET): clean | $(dir $(BIN))
	CGO_ENABLED=$(if $(CGO_ENABLED),$(CGO_ENABLED),0) go build -o $(BIN) -ldflags "$(LDFLAGS)" $(GO_PACKAGE)
	ln -sf $(ABS_BIN) $(STABLE_BIN)

$(dir $(BIN)):
	mkdir -p $(dir $(BIN))

clean:
	-rm -f $(BIN) $(STABLE_BIN)
	-go clean -cache -testcache

IMAGE_NAME    := openai/tunnel-client
IMAGE_TAG     := $(if $(GIT_SHA),$(GIT_SHA),latest)

build-image: $(TARGET)
	docker build --build-arg GIT_SHA=$(IMAGE_TAG) -t $(IMAGE_NAME):$(IMAGE_TAG) .
	@if [ "$(GIT_SHA)" != "" ]; then \
		docker tag $(IMAGE_NAME):$(IMAGE_TAG) $(IMAGE_NAME):latest; \
	fi
