.PHONY: install test clean cover

PROGRAM := tim
SOURCES := $(wildcard *.go)

GO ?= go
MODULE_FILES := go.mod $(wildcard go.sum)
GOFLAGS ?= -mod=vendor -v

# macOS, FreeBSD and Linux detection
UNAME_S := $(shell uname -s)
ifeq ($(UNAME_S),Darwin)
  PREFIX ?= /usr/local
  MAKE ?= make
else ifeq ($(UNAME_S),FreeBSD)
  PREFIX ?= /usr/local
  MAKE ?= gmake
else
  PREFIX ?= /usr
  MAKE ?= make
endif

DESTDIR ?=
BINDIR ?= $(PREFIX)/bin
MANDIR ?= $(PREFIX)/share/man/man1

$(PROGRAM): $(SOURCES) $(MODULE_FILES)
	$(GO) build $(GOFLAGS) -o $(PROGRAM)

install: $(PROGRAM)
	install -d "$(DESTDIR)$(BINDIR)"
	install -m 755 $(PROGRAM) "$(DESTDIR)$(BINDIR)/$(PROGRAM)"
	install -d "$(DESTDIR)$(MANDIR)"
	install -m 644 $(PROGRAM).1 "$(DESTDIR)$(MANDIR)/$(PROGRAM).1"

test:
	@echo "Running tests..."
	$(GO) test -failfast -timeout 1m ./...

cover:
	$(GO) test -mod=vendor -coverprofile=coverage.out -coverpkg=./... ./...
	$(GO) tool cover -func=coverage.out

clean:
	rm -f $(PROGRAM) coverage.out
