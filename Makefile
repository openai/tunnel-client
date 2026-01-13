.DEFAULT_GOAL := all

TARGET     := tunnel-client
OS         := $(if $(GOOS),$(GOOS),$(shell go env GOOS))
ARCH       := $(if $(GOARCH),$(GOARCH),$(shell go env GOARCH))
GOARM      := $(if $(GOARM),$(GOARM),)
GO_PACKAGE := ./cmd/client
BIN         = bin/$(OS)_$(ARCH)$(if $(GOARM),v$(GOARM),)/$(TARGET)
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

.PHONY: all help fmt test clean build-image

all: clean fmt test $(TARGET)

help:
	@echo "Available targets:"
	@echo "  all           - Build the tunnel-client binary (default)"
	@echo "  fmt           - Run go fmt and fail if files are modified"
	@echo "  $(TARGET)     - Build the tunnel-client binary"
	@echo "  test          - Run tests"
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
	@echo ""
	@echo "Artifacts:"
	@echo "  $(STABLE_BIN) -> $(BIN)"

test:
	go test -race ./...

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

$(TARGET): clean | $(dir $(BIN))
	CGO_ENABLED=$(if $(CGO_ENABLED),$(CGO_ENABLED),0) go build -o $(BIN) -ldflags "$(LDFLAGS)" $(GO_PACKAGE)
	ln -sf $(ABS_BIN) $(STABLE_BIN)

$(dir $(BIN)):
	mkdir -p $(dir $(BIN))

clean:
	-rm -f $(BIN) $(STABLE_BIN)

IMAGE_NAME    := openai/tunnel-client
IMAGE_TAG     := $(if $(GIT_SHA),$(GIT_SHA),latest)

build-image: $(TARGET)
	docker build --build-arg GIT_SHA=$(IMAGE_TAG) -t $(IMAGE_NAME):$(IMAGE_TAG) .
	@if [ "$(GIT_SHA)" != "" ]; then \
		docker tag $(IMAGE_NAME):$(IMAGE_TAG) $(IMAGE_NAME):latest; \
	fi
