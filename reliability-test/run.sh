#!/bin/bash

# Reliability Test Runner
# This script builds and runs the reliability test application

set -e

cd "$(dirname "$0")"

echo "Building reliability test..."
go build -o reliability-test main.go config.go

echo "Starting reliability test..."
echo "Press Ctrl+C to stop and view results"
echo ""

# Set default URL if not provided
export LABWATCH_URL="${LABWATCH_URL:-http://boss.local:8080}"

echo "Using LABWATCH_URL: $LABWATCH_URL"
echo ""

./reliability-test
