.PHONY: build app dmg install run test clean

BINARY := freegpu
APP_BUNDLE := FreeGPU.app
VERSION := 1.0.0
ARCH := $(shell uname -m)
DMG_NAME := dist/FreeGPU-v$(VERSION)-$(ARCH).dmg

LDFLAGS := -s -w
ifdef OAUTH_CLIENT_ID
LDFLAGS += -X github.com/anilgoudgunda/freegpu/internal/auth.DefaultClientID=$(OAUTH_CLIENT_ID)
endif
ifdef OAUTH_CLIENT_SECRET
LDFLAGS += -X github.com/anilgoudgunda/freegpu/internal/auth.DefaultClientSecret=$(OAUTH_CLIENT_SECRET)
endif

build:
	go build -ldflags="$(LDFLAGS)" -o $(BINARY) .

app: build
	mkdir -p $(APP_BUNDLE)/Contents/MacOS
	mkdir -p $(APP_BUNDLE)/Contents/Resources
	cp $(BINARY) $(APP_BUNDLE)/Contents/MacOS/FreeGPU
	@if [ -f assets/AppIcon.icns ]; then cp assets/AppIcon.icns $(APP_BUNDLE)/Contents/Resources/AppIcon.icns; fi
	@printf '%s\n' \
		'<?xml version="1.0" encoding="UTF-8"?>' \
		'<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">' \
		'<plist version="1.0"><dict>' \
		'  <key>CFBundleExecutable</key><string>FreeGPU</string>' \
		'  <key>CFBundleIdentifier</key><string>com.anilgoudgunda.freegpu</string>' \
		'  <key>CFBundleName</key><string>FreeGPU</string>' \
		'  <key>CFBundleDisplayName</key><string>FreeGPU</string>' \
		'  <key>CFBundleIconFile</key><string>AppIcon</string>' \
		'  <key>CFBundleShortVersionString</key><string>$(VERSION)</string>' \
		'  <key>CFBundleVersion</key><string>$(VERSION)</string>' \
		'  <key>CFBundlePackageType</key><string>APPL</string>' \
		'  <key>NSHighResolutionCapable</key><true/>' \
		'</dict></plist>' > $(APP_BUNDLE)/Contents/Info.plist
	@touch $(APP_BUNDLE)

dmg: app
	rm -rf dist/dmg-stage $(DMG_NAME)
	mkdir -p dist/dmg-stage
	cp -R $(APP_BUNDLE) dist/dmg-stage/
	ln -s /Applications dist/dmg-stage/Applications
	hdiutil create -volname "FreeGPU" -srcfolder dist/dmg-stage -ov -format UDZO $(DMG_NAME)
	rm -rf dist/dmg-stage
	@shasum -a 256 $(DMG_NAME)
	@echo "✅ Packaged $(DMG_NAME)"

install: app
	mkdir -p $(HOME)/.local/bin
	cp $(BINARY) $(HOME)/.local/bin/$(BINARY)
	@echo "✅ Built $(APP_BUNDLE) and installed $(BINARY) to $(HOME)/.local/bin/$(BINARY)"

run: app
	./$(BINARY)

test:
	go vet ./...
	python3 -m py_compile internal/worker/agent.py

clean:
	rm -rf $(BINARY) $(APP_BUNDLE) dist
