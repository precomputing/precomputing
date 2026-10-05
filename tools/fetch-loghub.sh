#!/bin/sh
# Downloads the Loghub 2,000-line samples that Drain's published accuracy was measured on
# (logpai/logparser, data/loghub_2k) into build/loghub, for the Drain accuracy test:
#   sh tools/fetch-loghub.sh && LOGHUB=build/loghub go test -run TestLoghub -v ./internal/drain
# The data belongs to the Loghub project (https://github.com/logpai/loghub) and is not part of this repository.
set -eu
cd "$(dirname "$0")/.."
BASE=https://raw.githubusercontent.com/logpai/logparser/main/data/loghub_2k
for d in HDFS Hadoop Spark Zookeeper BGL HPC Thunderbird Windows Linux Android HealthApp Apache Proxifier OpenSSH OpenStack Mac; do
  mkdir -p "build/loghub/$d"
  for f in "${d}_2k.log" "${d}_2k.log_structured.csv"; do
    [ -s "build/loghub/$d/$f" ] || curl -sSf -o "build/loghub/$d/$f" "$BASE/$d/$f"
  done
done
echo "Loghub samples in build/loghub"
