.DEFAULT_GOAL := all

TARGET     := tunnel-client
RUNTIME_TARGET := tunnel-client-runtime
RUNTIME_CLOUDFLARED_TARGET := tunnel-client-runtime-cloudflared
OS         := $(if $(GOOS),$(GOOS),$(shell go env GOOS))
ARCH       := $(if $(GOARCH),$(GOARCH),$(shell go env GOARCH))
GOARM      := $(if $(GOARM),$(GOARM),)
GO_PACKAGE := ./cmd/client
RUNTIME_GO_PACKAGE := ./cmd/client-runtime
RUNTIME_CLOUDFLARED_GO_PACKAGE := ./cmd/client-runtime-cloudflared
BIN         = bin/$(OS)_$(ARCH)$(if $(GOARM),v$(GOARM),)/$(TARGET)
RUNTIME_BIN = bin/$(OS)_$(ARCH)$(if $(GOARM),v$(GOARM),)/$(RUNTIME_TARGET)
RUNTIME_CLOUDFLARED_BIN = bin/$(OS)_$(ARCH)$(if $(GOARM),v$(GOARM),)/$(RUNTIME_CLOUDFLARED_TARGET)
ADMIN_UI_DIR := adminui
ADMIN_UI_ASSETS_DIR := pkg/adminui/assets
ADMIN_UI_BUILD_SCRIPT := scripts/build_admin_ui.sh
PNPM       ?= pnpm
PNPM_PACKAGE_MANAGER_MANIFEST ?= $(or $(wildcard package.json),$(shell git rev-parse --show-toplevel 2>/dev/null)/package.json)
PNPM_PACKAGE_MANAGER ?= $(shell sed -n 's/^[[:space:]]*"packageManager"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$(PNPM_PACKAGE_MANAGER_MANIFEST)")
ADMIN_UI_PNPM_FLAGS := --config.shared-workspace-lockfile=false --config.confirmModulesPurge=false
ADMIN_UI_PNPM_STORE_DIR ?= $(if $(TMPDIR),$(TMPDIR),/tmp)/tunnel-client-adminui-pnpm-store
MAKE_ALL_JOBS ?= 3
GOPROXY ?= https://proxy.golang.org
ifeq ($(OS),windows)
  BIN = bin/$(OS)_$(ARCH)$(if $(GOARM),v$(GOARM),)/$(TARGET).exe
  RUNTIME_BIN = bin/$(OS)_$(ARCH)$(if $(GOARM),v$(GOARM),)/$(RUNTIME_TARGET).exe
  RUNTIME_CLOUDFLARED_BIN = bin/$(OS)_$(ARCH)$(if $(GOARM),v$(GOARM),)/$(RUNTIME_CLOUDFLARED_TARGET).exe
endif
STABLE_BIN := bin/$(TARGET)
RUNTIME_STABLE_BIN := bin/$(RUNTIME_TARGET)
RUNTIME_CLOUDFLARED_STABLE_BIN := bin/$(RUNTIME_CLOUDFLARED_TARGET)
ifeq ($(OS),windows)
  STABLE_BIN = bin/$(TARGET).exe
  RUNTIME_STABLE_BIN = bin/$(RUNTIME_TARGET).exe
  RUNTIME_CLOUDFLARED_STABLE_BIN = bin/$(RUNTIME_CLOUDFLARED_TARGET).exe
endif
ABS_BIN := $(abspath $(BIN))
RUNTIME_ABS_BIN := $(abspath $(RUNTIME_BIN))
RUNTIME_CLOUDFLARED_ABS_BIN := $(abspath $(RUNTIME_CLOUDFLARED_BIN))

GIT_SHA    := $(if $(GIT_SHA),$(GIT_SHA),$(shell git rev-parse --short HEAD 2>/dev/null))
LDFLAGS    := -X github.com/openai/tunnel-client/pkg/version.GitSHA=$(GIT_SHA)
BUILD_GO_VERSION := $(shell go env GOVERSION 2>/dev/null)
RUNTIME_GOFLAGS := -trimpath -buildvcs=false
RUNTIME_BUILD_METADATA := $(LDFLAGS) -X github.com/openai/tunnel-client/pkg/version.GoVersion=$(BUILD_GO_VERSION) -X 'github.com/openai/tunnel-client/pkg/version.BuildFlags=$(RUNTIME_GOFLAGS)'
RUNTIME_LDFLAGS := $(RUNTIME_BUILD_METADATA) -X github.com/openai/tunnel-client/pkg/version.Flavor=runtime
RUNTIME_CLOUDFLARED_LDFLAGS := $(RUNTIME_BUILD_METADATA) -X github.com/openai/tunnel-client/pkg/version.Flavor=runtime-cloudflared

DIST_DIR ?= dist
STAGE_ROOT ?= $(DIST_DIR)/stage
SBOM_ROOT ?= $(DIST_DIR)/sbom
CLIENT_STAGE_DIR := $(STAGE_ROOT)/client/$(OS)_$(ARCH)
RUNTIME_STAGE_DIR := $(STAGE_ROOT)/runtime/$(OS)_$(ARCH)
RUNTIME_CLOUDFLARED_STAGE_DIR := $(STAGE_ROOT)/runtime-cloudflared/$(OS)_$(ARCH)
CLIENT_LICENSE_REPORT := compliance/oss-license-report-client.txt
RUNTIME_LICENSE_REPORT := compliance/oss-license-report-runtime.txt
RUNTIME_CLOUDFLARED_LICENSE_REPORT := compliance/oss-license-report-runtime-cloudflared.txt
ARTIFACT_LICENSE_REPORT_SCRIPT := ./scripts/build_artifact_license_report.sh
CLIENT_STAGE_LICENSE_NAME := $(TARGET)-$(OS)-$(ARCH)-licenses.txt
RUNTIME_STAGE_LICENSE_NAME := $(RUNTIME_TARGET)-$(OS)-$(ARCH)-licenses.txt
RUNTIME_CLOUDFLARED_STAGE_LICENSE_NAME := $(RUNTIME_CLOUDFLARED_TARGET)-$(OS)-$(ARCH)-licenses.txt

.PHONY: all help fmt test test-go-race test-runtime test-runtime-release-archive runtime-container-compatibility runtime-k8s-compatibility clean clean-client clean-go-cache clean-runtime build-image build-image-runtime build-image-runtime-cloudflared mod-tidy admin-ui admin-ui-test release-source-version release-tag end-user-guide-screenshots end-user-guide-html end-user-guide-slides tunnel-client-runtime tunnel-client-runtime-cloudflared runtime runtime-cloudflared sbom sbom-runtime sbom-runtime-cloudflared sbom-baselines verify-sbom-baselines verify-license-reports

# Keep an explicit mixed-goal invocation such as make -j all tunnel-client from
# racing all's clean/tidy/format phase against a sibling goal. The default
# no-goal make invocation and all's recursive final phase still use the bounded
# parallelism below.
ifneq (,$(filter all,$(MAKECMDGOALS)))
.NOTPARALLEL:
endif

# Keep the mutation-prone checks ordered, then let the independent read/build
# work overlap. The recursive make provides the bounded parallelism even when a
# caller invokes plain `make all` without its own -j flag.
all:
	$(MAKE) clean
	$(MAKE) mod-tidy
	$(MAKE) fmt
	$(MAKE) -j$(MAKE_ALL_JOBS) admin-ui-test test-go-race $(TARGET)

help:
	@echo "Available targets:"
	@echo "  all           - Run the full clean/tidy/format/test/build gate (default)"
	@echo "  mod-tidy      - Run go mod tidy and fail if go.mod/go.sum change"
	@echo "  fmt           - Run go fmt and fail if files are modified"
	@echo "  $(TARGET)     - Build the tunnel-client binary"
	@echo "  $(RUNTIME_TARGET) - Build the narrow customer runtime binary"
	@echo "  $(RUNTIME_CLOUDFLARED_TARGET) - Build the runtime binary with the pinned cloudflared companion"
	@echo "  runtime       - Short alias for $(RUNTIME_TARGET)"
	@echo "  runtime-cloudflared - Short alias for $(RUNTIME_CLOUDFLARED_TARGET)"
	@echo "  test          - Run Go and admin UI tests"
	@echo "  test-go-race  - Run race-enabled Go tests"
	@echo "  test-runtime  - Run runtime package and runtime artifact tests"
	@echo "  test-runtime-release-archive - Package, verify, extract, and smoke-test native runtime ZIP fixtures"
	@echo "  runtime-container-compatibility - Smoke-test already-built runtime images with hardened mounts"
	@echo "  runtime-k8s-compatibility - Opt-in kind/k3d deployment smoke for already-built runtime images"
	@echo "  admin-ui      - Build the admin UI assets (manual; not part of make all)"
	@echo "  admin-ui-test - Run admin UI tests"
	@echo "  end-user-guide-screenshots - Capture the local /ui screenshots used by the shareable guide"
	@echo "  end-user-guide-html - Render docs/end-user-guide.md to a standalone HTML archive"
	@echo "  end-user-guide-slides - Render docs/end-user-guide.md to a local .pptx deck for on-demand slide import/distribution"
	@echo "  release-source-version - Write VERSION into pkg/version/VERSION before creating a release tag"
	@echo "  release-tag   - Generate a release tag like v1.2.3"
	@echo "  clean         - Remove built binaries"
	@echo "  clean-go-cache - Remove Go build and test caches"
	@echo "  build-image   - Build Docker image with tunnel-client binary"
	@echo "  build-image-runtime - Build the narrow runtime Docker image"
	@echo "  build-image-runtime-cloudflared - Build the runtime-cloudflared Docker image"
	@echo "  sbom          - Stage the full client payload and emit its SPDX 2.3 SBOM"
	@echo "  sbom-runtime  - Stage the runtime payload and emit its SPDX 2.3 SBOM"
	@echo "  sbom-runtime-cloudflared - Stage the runtime-cloudflared payload and emit its SPDX 2.3 SBOM"
	@echo "  sbom-baselines - Regenerate deterministic SBOM baseline payloads"
	@echo "  verify-sbom-baselines - Verify mirrored SBOM baseline manifest and SPDX files"
	@echo "  verify-license-reports - Verify checked-in public license report checksums"
	@echo ""
	@echo "Docker image build options:"
	@echo "  make build-image                   # Build with git short SHA tag (default)"
	@echo "  GIT_SHA=v1.0.0 make build-image    # Build with specific tag"
	@echo "  make build-image-runtime           # Build the narrow runtime image"
	@echo "  make build-image-runtime-cloudflared # Build the runtime-cloudflared image"
	@echo ""
	@echo "Environment variables:"
	@echo "  GOOS         - Target OS (default: $(OS))"
	@echo "  GOARCH       - Target architecture (default: $(ARCH))"
	@echo "  GIT_SHA      - Git SHA/tag for version info and Docker tagging"
	@echo "  GOPROXY      - Proxy-only Go module source for bundled cloudflared builds"
	@echo "  MAKE_ALL_JOBS - Maximum concurrent test/build jobs used by make all (default: $(MAKE_ALL_JOBS))"
	@echo "  VERSION      - Version for make release-tag (required)"
	@echo ""
	@echo "Artifacts:"
	@echo "  $(STABLE_BIN) -> $(BIN)"
	@echo "  $(RUNTIME_STABLE_BIN) -> $(RUNTIME_BIN)"
	@echo "  $(RUNTIME_CLOUDFLARED_STABLE_BIN) -> $(RUNTIME_CLOUDFLARED_BIN)"

test: admin-ui-test
	$(MAKE) test-go-race

test-go-race:
	go test -race ./...

test-runtime: runtime runtime-cloudflared test-runtime-release-archive
	go test ./cmd/client-runtime ./cmd/client-runtime-cloudflared ./pkg/runtimeapp/... ./pkg/runtimeconfig ./pkg/runtimehealth ./pkg/runtimeharpoon/...
	go test ./e2e -run '^TestRuntime' -count=1

test-runtime-release-archive: runtime runtime-cloudflared
	./scripts/runtime_release_archive_smoke_test.sh --flavor runtime --binary $(RUNTIME_BIN) --target-os $(OS) --target-arch $(ARCH)
	./scripts/runtime_release_archive_smoke_test.sh --flavor runtime-cloudflared --binary $(RUNTIME_CLOUDFLARED_BIN) --target-os $(OS) --target-arch $(ARCH)

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
	@if [ -z "$(VERSION)" ]; then \
		echo "usage: make release-tag VERSION=1.2.3"; \
		exit 1; \
	fi
	@./scripts/release_tag.sh check-source-version "$(VERSION)"
	@./scripts/release_tag.sh make "$(VERSION)"

$(TARGET): clean-client | $(dir $(BIN))
	CGO_ENABLED=$(if $(CGO_ENABLED),$(CGO_ENABLED),0) go build -o $(BIN) -ldflags "$(LDFLAGS)" $(GO_PACKAGE)
	ln -sf $(ABS_BIN) $(STABLE_BIN)

tunnel-client-runtime:
	-rm -f $(RUNTIME_BIN) $(RUNTIME_STABLE_BIN)
	mkdir -p $(dir $(RUNTIME_BIN))
	CGO_ENABLED=$(if $(CGO_ENABLED),$(CGO_ENABLED),0) GOOS=$(OS) GOARCH=$(ARCH) go build $(RUNTIME_GOFLAGS) -o $(RUNTIME_BIN) -ldflags "$(RUNTIME_LDFLAGS)" $(RUNTIME_GO_PACKAGE)
	ln -sf $(RUNTIME_ABS_BIN) $(RUNTIME_STABLE_BIN)

tunnel-client-runtime-cloudflared:
	-rm -f $(RUNTIME_CLOUDFLARED_BIN) $(RUNTIME_CLOUDFLARED_STABLE_BIN)
	mkdir -p $(dir $(RUNTIME_CLOUDFLARED_BIN))
	GOPROXY=$(GOPROXY) CGO_ENABLED=$(if $(CGO_ENABLED),$(CGO_ENABLED),0) GOOS=$(OS) GOARCH=$(ARCH) go build $(RUNTIME_GOFLAGS) -o $(RUNTIME_CLOUDFLARED_BIN) -ldflags "$(RUNTIME_CLOUDFLARED_LDFLAGS)" $(RUNTIME_CLOUDFLARED_GO_PACKAGE)
	ln -sf $(RUNTIME_CLOUDFLARED_ABS_BIN) $(RUNTIME_CLOUDFLARED_STABLE_BIN)

runtime: tunnel-client-runtime

runtime-cloudflared: tunnel-client-runtime-cloudflared

$(dir $(BIN)):
	mkdir -p $(dir $(BIN))

clean: clean-client clean-runtime

clean-client:
	-rm -f $(BIN) $(STABLE_BIN)

clean-go-cache:
	-go clean -cache -testcache

clean-runtime:
	-rm -f $(RUNTIME_BIN) $(RUNTIME_STABLE_BIN) $(RUNTIME_CLOUDFLARED_BIN) $(RUNTIME_CLOUDFLARED_STABLE_BIN)

IMAGE_NAME    := openai/tunnel-client
RUNTIME_IMAGE_NAME := openai/tunnel-client-runtime
RUNTIME_CLOUDFLARED_IMAGE_NAME := openai/tunnel-client-runtime-cloudflared
IMAGE_TAG     := $(if $(GIT_SHA),$(GIT_SHA),latest)

build-image: $(TARGET)
	docker build --build-arg GIT_SHA=$(IMAGE_TAG) --build-arg GOPROXY=$(GOPROXY) --build-arg PNPM_PACKAGE_MANAGER="$(PNPM_PACKAGE_MANAGER)" -t $(IMAGE_NAME):$(IMAGE_TAG) .
	@if [ "$(GIT_SHA)" != "" ]; then \
		docker tag $(IMAGE_NAME):$(IMAGE_TAG) $(IMAGE_NAME):latest; \
	fi

build-image-runtime:
	docker build --file Dockerfile.runtime --target runtime --build-arg GIT_SHA=$(GIT_SHA) --build-arg GO_VERSION=$(BUILD_GO_VERSION) --build-arg GOPROXY=$(GOPROXY) -t $(RUNTIME_IMAGE_NAME):$(IMAGE_TAG) .
	@if [ "$(GIT_SHA)" != "" ]; then \
		docker tag $(RUNTIME_IMAGE_NAME):$(IMAGE_TAG) $(RUNTIME_IMAGE_NAME):latest; \
	fi

build-image-runtime-cloudflared:
	docker build --file Dockerfile.runtime --target runtime-cloudflared --build-arg GIT_SHA=$(GIT_SHA) --build-arg GO_VERSION=$(BUILD_GO_VERSION) --build-arg GOPROXY=$(GOPROXY) -t $(RUNTIME_CLOUDFLARED_IMAGE_NAME):$(IMAGE_TAG) .
	@if [ "$(GIT_SHA)" != "" ]; then \
		docker tag $(RUNTIME_CLOUDFLARED_IMAGE_NAME):$(IMAGE_TAG) $(RUNTIME_CLOUDFLARED_IMAGE_NAME):latest; \
	fi

runtime-container-compatibility:
	./scripts/runtime_container_compatibility_test.sh --skip-if-unavailable --flavor runtime --image $(RUNTIME_IMAGE_NAME):$(IMAGE_TAG)
	./scripts/runtime_container_compatibility_test.sh --skip-if-unavailable --flavor runtime-cloudflared --image $(RUNTIME_CLOUDFLARED_IMAGE_NAME):$(IMAGE_TAG)

runtime-k8s-compatibility:
	./scripts/runtime_k8s_compatibility_test.sh --skip-if-unavailable --flavor runtime --image $(RUNTIME_IMAGE_NAME):$(IMAGE_TAG)
	./scripts/runtime_k8s_compatibility_test.sh --skip-if-unavailable --flavor runtime-cloudflared --image $(RUNTIME_CLOUDFLARED_IMAGE_NAME):$(IMAGE_TAG)

sbom: $(TARGET)
	rm -rf $(CLIENT_STAGE_DIR)
	mkdir -p $(CLIENT_STAGE_DIR) $(SBOM_ROOT)
	cp $(BIN) $(CLIENT_STAGE_DIR)/$(TARGET)$(if $(filter windows,$(OS)),.exe,)
	cp LICENSE NOTICE $(CLIENT_STAGE_DIR)/
	$(ARTIFACT_LICENSE_REPORT_SCRIPT) --flavor client --goos $(OS) --goarch $(ARCH) --output $(CLIENT_STAGE_DIR)/$(CLIENT_STAGE_LICENSE_NAME)
	GOPROXY=$(GOPROXY) ./scripts/build_cloudflared.sh --goos $(OS) --goarch $(ARCH) --output $(CLIENT_STAGE_DIR)/cloudflared$(if $(filter windows,$(OS)),.exe,)
	cp pkg/cloudflared/manifest.json $(CLIENT_STAGE_DIR)/cloudflared-manifest.json
	./scripts/generate_sbom.sh --flavor client --staged-dir $(CLIENT_STAGE_DIR) --output $(SBOM_ROOT)/$(TARGET)-$(OS)-$(ARCH).spdx.json

sbom-runtime: runtime
	rm -rf $(RUNTIME_STAGE_DIR)
	mkdir -p $(RUNTIME_STAGE_DIR) $(SBOM_ROOT)
	cp $(RUNTIME_BIN) $(RUNTIME_STAGE_DIR)/$(RUNTIME_TARGET)$(if $(filter windows,$(OS)),.exe,)
	cp LICENSE NOTICE $(RUNTIME_STAGE_DIR)/
	$(ARTIFACT_LICENSE_REPORT_SCRIPT) --flavor runtime --goos $(OS) --goarch $(ARCH) --output $(RUNTIME_STAGE_DIR)/$(RUNTIME_STAGE_LICENSE_NAME)
	./scripts/generate_sbom.sh --flavor runtime --staged-dir $(RUNTIME_STAGE_DIR) --output $(SBOM_ROOT)/$(RUNTIME_TARGET)-$(OS)-$(ARCH).spdx.json

sbom-runtime-cloudflared: runtime-cloudflared
	rm -rf $(RUNTIME_CLOUDFLARED_STAGE_DIR)
	mkdir -p $(RUNTIME_CLOUDFLARED_STAGE_DIR) $(SBOM_ROOT)
	cp $(RUNTIME_CLOUDFLARED_BIN) $(RUNTIME_CLOUDFLARED_STAGE_DIR)/$(RUNTIME_CLOUDFLARED_TARGET)$(if $(filter windows,$(OS)),.exe,)
	cp LICENSE NOTICE $(RUNTIME_CLOUDFLARED_STAGE_DIR)/
	$(ARTIFACT_LICENSE_REPORT_SCRIPT) --flavor runtime-cloudflared --goos $(OS) --goarch $(ARCH) --output $(RUNTIME_CLOUDFLARED_STAGE_DIR)/$(RUNTIME_CLOUDFLARED_STAGE_LICENSE_NAME)
	GOPROXY=$(GOPROXY) ./scripts/build_cloudflared.sh --goos $(OS) --goarch $(ARCH) --output $(RUNTIME_CLOUDFLARED_STAGE_DIR)/cloudflared$(if $(filter windows,$(OS)),.exe,)
	cp pkg/cloudflared/runtime/manifest.json $(RUNTIME_CLOUDFLARED_STAGE_DIR)/cloudflared-manifest.json
	./scripts/generate_sbom.sh --flavor runtime-cloudflared --staged-dir $(RUNTIME_CLOUDFLARED_STAGE_DIR) --output $(SBOM_ROOT)/$(RUNTIME_CLOUDFLARED_TARGET)-$(OS)-$(ARCH).spdx.json

sbom-baselines:
	./scripts/generate_sbom_baselines.sh --write

verify-sbom-baselines:
	./scripts/verify_sbom_baselines.sh

verify-license-reports:
	@set -eu; \
	for report in \
		$(CLIENT_LICENSE_REPORT) \
		$(RUNTIME_LICENSE_REPORT) \
		$(RUNTIME_CLOUDFLARED_LICENSE_REPORT) \
		compliance/oss-license-report-cloudflared-binary.txt; do \
		checksum_file="$$report.sha256"; \
		test -f "$$report" || { echo "missing license report: $$report" >&2; exit 1; }; \
		test -f "$$checksum_file" || { echo "missing license report checksum: $$checksum_file" >&2; exit 1; }; \
		expected="$$(tr -d '[:space:]' < "$$checksum_file")"; \
		if command -v sha256sum >/dev/null 2>&1; then \
			actual="$$(sha256sum "$$report" | awk '{print $$1}')"; \
		else \
			actual="$$(shasum -a 256 "$$report" | awk '{print $$1}')"; \
		fi; \
		test "$$actual" = "$$expected" || { echo "license report checksum mismatch: $$report" >&2; exit 1; }; \
	done
