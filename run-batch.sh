#!/bin/bash

set -e

# Default values
runner=""
amount=100
dedicated_db=false
runs=10
base_out_dir=""

# Usage help
usage() {
  echo "Usage: $0 --runner [operator|scripted] --baseOutDir DIR [--amount N] [--dedicated-db true|false] [--runs N]"
  exit 1
}

# Parse args
while [[ "$#" -gt 0 ]]; do
  case $1 in
    --runner) runner="$2"; shift ;;
    --amount) amount="$2"; shift ;;
    --dedicated-db) dedicated_db="$2"; shift ;;
    --runs) runs="$2"; shift ;;
    --baseOutDir) base_out_dir="$2"; shift ;;
    *) usage ;;
  esac
  shift
done

# Validate required args
if [[ "$runner" != "operator" && "$runner" != "scripted" ]]; then
  echo "❌ Invalid runner: must be 'operator' or 'scripted'"
  usage
fi

if [[ -z "$base_out_dir" ]]; then
  echo "❌ Missing required flag: --baseOutDir"
  usage
fi

# Compute output folder name
dedicated_label="shared"
if [[ "$dedicated_db" == "true" ]]; then
  dedicated_label="dedicated"
fi

out_dir="${base_out_dir}/${runner}-results-${amount}-${dedicated_label}"

# Determine Make target
target="test-${runner}-runner"

# Run N times
for ((i=0; i<runs; i++)); do
  echo "▶️ Running $runner test: run #$i"
  make "$target" ARGS="--amount=$amount --dedicated-db=$dedicated_db --run=$i --outDir=$out_dir"
done

echo "✅ All $runs runs completed for $runner."
