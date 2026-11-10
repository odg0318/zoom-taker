# zoom-taker build. The cgo engine needs whisper.cpp built first:
#   make build     # whisper.cpp static libs + Go binary with Metal (macOS) / CPU (Linux)
#   make test      # full test suite (needs whisper-lib)
#   make test-pure # tests without cgo (no whisper.cpp build needed)
WHISPER_DIR := third_party/whisper.cpp
BUILD_DIR   := build
TAG         := whisper

build: whisper-lib
	CGO_ENABLED=1 \
	C_INCLUDE_PATH="$(CURDIR)/$(BUILD_DIR)/stage/include" \
	LIBRARY_PATH="$(CURDIR)/$(BUILD_DIR)/stage/lib" \
	go build -tags $(TAG) -o bin/zoom-taker ./cmd/zoom-taker

whisper-src:
	@test -f $(WHISPER_DIR)/CMakeLists.txt || git submodule update --init --depth 1 $(WHISPER_DIR)

# Apple cmake defaults (Metal + Accelerate BLAS, shaders embedded in the
# static lib) are required by the binding's darwin link flags — do not
# override GGML_METAL/GGML_BLAS/GGML_METAL_EMBED_LIBRARY here.
whisper-lib: whisper-src
	cmake -S $(WHISPER_DIR) -B $(BUILD_DIR)/whisper -DCMAKE_BUILD_TYPE=Release \
		-DBUILD_SHARED_LIBS=OFF -DGGML_NATIVE=OFF
	cmake --build $(BUILD_DIR)/whisper --target whisper -j$$(sysctl -n hw.ncpu 2>/dev/null || nproc)
	@mkdir -p $(BUILD_DIR)/stage/include $(BUILD_DIR)/stage/lib
	cp -R $(WHISPER_DIR)/include/. $(WHISPER_DIR)/ggml/include/. $(BUILD_DIR)/stage/include/
	find $(BUILD_DIR)/whisper -name '*.a' -exec cp {} $(BUILD_DIR)/stage/lib/ \;

test: whisper-lib
	C_INCLUDE_PATH="$(CURDIR)/$(BUILD_DIR)/stage/include" \
	LIBRARY_PATH="$(CURDIR)/$(BUILD_DIR)/stage/lib" \
	go test -tags $(TAG) ./...

test-pure:
	go test ./...

vet:
	go vet ./...
	CGO_ENABLED=1 go vet -tags $(TAG) ./...

docker: whisper-src
	docker buildx build --platform linux/$$(uname -m) -t guri/zoom-taker:dev --load .

clean:
	rm -rf build bin

.PHONY: build whisper-src whisper-lib test test-pure vet docker clean
