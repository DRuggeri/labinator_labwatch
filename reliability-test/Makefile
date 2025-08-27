.PHONY: build run clean test

# Build the reliability test binary
build:
	go build -o reliability-test main.go config.go

# Run the reliability test
run: build
	./reliability-test

# Clean build artifacts
clean:
	rm -f reliability-test

# Test the build
test: build
	@echo "Build successful!"

# Show help
help:
	@echo "Available targets:"
	@echo "  build  - Build the reliability test binary"
	@echo "  run    - Build and run the reliability test" 
	@echo "  clean  - Remove build artifacts"
	@echo "  test   - Test that the application builds"
	@echo "  help   - Show this help message"
