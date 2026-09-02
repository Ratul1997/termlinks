.PHONY: build test install clean

build:
	npm run build

test:
	npm test

install: build
	install -d "$(HOME)/.local/bin"
	install -m 755 dist/termlinks "$(HOME)/.local/bin/termlinks"
	@echo "Installed $(HOME)/.local/bin/termlinks"

clean:
	go clean -C apps/backend
