# Repository entry point for the Go Temporal worker module.
# The implementation and its build assets live under golang/ so this repository
# can host additional clients without coupling their build roots.
.PHONY: help
help:
	@echo "Go worker targets are available through: make -C golang <target>"

%:
	$(MAKE) -C golang $@
