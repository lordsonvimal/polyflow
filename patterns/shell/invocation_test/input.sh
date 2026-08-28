#!/usr/bin/env bash
# Positive fixture for patterns/shell/invocation.yaml: all four recognized
# invocation shapes (verb-prefixed x4, bare executable-bit x2).

bash migrate.sh
sh setup.sh
. lib.sh
source lib2.sh
./scripts/seed.sh
../shared/init.sh
