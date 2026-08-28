#!/usr/bin/env bash
# Positive fixture for patterns/shell/functions.yaml: covers both function
# definition forms and same-file bare-word command invocations.

function greet {
  echo "hello"
}

build() {
  greet
  echo "built"
}

build
