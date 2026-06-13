# Changelog

All notable changes to the `proc-lens` project will be documented in this file.

## [1.0.0] - 2026-06-13
### Added
- Dedicated `LICENSE` file under Apache-2.0.
- GitHub Actions CI workflow for automatic builds and linting.
- Indian Professional English translation for code comments and user-facing CLI output.
- Bounded read protection helpers for procfs/sysfs telemetry collection to prevent DoS.
- Global timeout configurations for concurrent collection cycles.
- NetworkPolicy Helm template to secure Prometheus endpoint access.

### Changed
- Refactored `os.Exit(1)` usage inside nested commands to clean error returns handled via Cobra framework in `main.go`.
- Changed default metrics HTTP binding behavior to protect internal state.

