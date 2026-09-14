#!/usr/bin/env bash
# loadtest-metrics.sh — control-plane resource sampler for the scale load
# test (Phase 15.5). Samples, every ~4s, the same set of metrics the
# Phase 13.3 addendum used, so the 100/500/1000 numbers are directly
# comparable with the earlier lt-s3/lt-d3 runs:
#
#   - container CPU% + memory for postgres / temporal / keycloak (docker
#     stats, one snapshot per sample)
#   - worker + API CPU% + RSS from /proc stat deltas (1s window, % of one
#     core; ticks == percent at the standard 100 Hz clock)
#   - postgres active connection count (pg_stat_activity on the stack DB)
#
# Usage: loadtest-metrics.sh <outfile.csv>
# Runs until killed. One header row, then one data row per sample.
set -u

out="$1"
echo "ts,pg_cpu,pg_mem_mb,temporal_cpu,temporal_mem_mb,kc_cpu,kc_mem_mb,worker_cpu,worker_rss_mb,api_cpu,api_rss_mb,pg_conns" > "$out"

# Parse one docker stats MemUsage token ("774.3 MiB", "1.2 GiB") to MiB.
mib() {
  awk '{ v=$1; u=$2;
         if (u == "GiB") v = v * 1024;
         printf "%d", v }'
}

wp=$(pgrep -f '/tmp/tf-worker-bin' | head -1)
ap=$(pgrep -f '/tmp/tf-api-bin' | head -1)

while true; do
  ts=$(date +%s)

  # Single docker stats call for the three stack containers.
  stats=$(docker stats --no-stream --format '{{.Name}} {{.CPUPerc}} {{.MemUsage}}' \
      tenantflow-postgres tenantflow-temporal tenantflow-keycloak 2>/dev/null)
  pg_cpu=$(echo "$stats" | awk '/tenantflow-postgres/{c=$2; sub(/%/,"",c); print c}')
  pg_mem=$(echo "$stats" | awk '/tenantflow-postgres/{print $3, $4}' | mib)
  tc_cpu=$(echo "$stats" | awk '/tenantflow-temporal /{c=$2; sub(/%/,"",c); print c}')
  tc_mem=$(echo "$stats" | awk '/tenantflow-temporal /{print $3, $4}' | mib)
  kc_cpu=$(echo "$stats" | awk '/tenantflow-keycloak/{c=$2; sub(/%/,"",c); print c}')
  kc_mem=$(echo "$stats" | awk '/tenantflow-keycloak/{print $3, $4}' | mib)

  # 1s /proc delta for the host processes.
  wa=$(awk '{printf "%d %d", $14+$15, $24}' "/proc/$wp/stat" 2>/dev/null || echo "0 0")
  aa=$(awk '{printf "%d %d", $14+$15, $24}' "/proc/$ap/stat" 2>/dev/null || echo "0 0")
  sleep 1
  wb=$(awk '{printf "%d %d", $14+$15, $24}' "/proc/$wp/stat" 2>/dev/null || echo "0 0")
  ab=$(awk '{printf "%d %d", $14+$15, $24}' "/proc/$ap/stat" 2>/dev/null || echo "0 0")
  wcpu=$(( $(echo "$wb" | cut -d' ' -f1) - $(echo "$wa" | cut -d' ' -f1) ))
  acpu=$(( $(echo "$ab" | cut -d' ' -f1) - $(echo "$aa" | cut -d' ' -f1) ))
  wrss=$(( $(echo "$wb" | cut -d' ' -f2) / 256 ))   # pages (4 KiB) -> MiB
  arss=$(( $(echo "$ab" | cut -d' ' -f2) / 256 ))

  conns=$(docker exec tenantflow-postgres psql -tA -q \
      "postgresql://temporal:temporal@localhost:5432/postgres" \
      -c "select count(*) from pg_stat_activity" 2>/dev/null)

  echo "$ts,$pg_cpu,$pg_mem,$tc_cpu,$tc_mem,$kc_cpu,$kc_mem,$wcpu,$wrss,$acpu,$arss,$conns" >> "$out"
done