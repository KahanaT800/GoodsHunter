# Documentation

GoodsHunter technical documentation.

---

## Directory Structure

```
docs/
├── README.md                           # This file
├── architecture/
│   └── design.md                       # System architecture & design
├── ops/
│   ├── configuration.md                # Environment variables & capacity planning
│   ├── deployment_modes.md             # Deployment modes & service management
│   ├── server_commands.txt             # Common server commands reference
│   └── grafana/
│       ├── grafana_system_overview.json
│       ├── grafana_business_metrics.json
│       └── grafana_distributed_cluster.json
├── dev/
│   └── local_test_checklist.md         # Local testing guide
└── archive/                            # Historical materials (archived)
```

---

## Quick Links

### Architecture

| Document | Description |
|----------|-------------|
| [design.md](architecture/design.md) | Complete system architecture, anti-detection features, data model |

### Operations

| Document | Description |
|----------|-------------|
| [configuration.md](ops/configuration.md) | All environment variables, Redis keys, capacity planning |
| [deployment_modes.md](ops/deployment_modes.md) | Master/Worker deployment, Docker profiles, troubleshooting |
| [server_commands.txt](ops/server_commands.txt) | Copy-paste commands for server management |

### Development

| Document | Description |
|----------|-------------|
| [local_test_checklist.md](dev/local_test_checklist.md) | Step-by-step local testing guide |

### Grafana Dashboards

Import these JSON files into Grafana:

| Dashboard | Description |
|-----------|-------------|
| [grafana_system_overview.json](ops/grafana/grafana_system_overview.json) | System metrics (CPU, memory, containers) |
| [grafana_business_metrics.json](ops/grafana/grafana_business_metrics.json) | Crawler metrics (success rate, latency) |
| [grafana_distributed_cluster.json](ops/grafana/grafana_distributed_cluster.json) | Multi-node cluster overview |

---

## Document Summary

| Topic | Key Information |
|-------|-----------------|
| **Architecture** | Master-Worker topology, Redis queues, anti-detection pipeline |
| **Deployment** | 4 modes: Minimal, SSL, Cloud Monitoring, Distributed |
| **Configuration** | 50+ environment variables, Redis keys, capacity limits |
| **Anti-Detection** | Stealth scripts, cookie persistence, adaptive throttling |
| **Testing** | Health check, task creation, log verification |
