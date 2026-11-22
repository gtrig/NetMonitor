# Scan profiles Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Add a `services` source that records open ports, services and OS per device, shown in the UI, without affecting presence or alerts.
**Architecture:** A scheduler job either collects presence (`Source`) or details (`Details` scanner). Details are stored in their own table keyed like devices.
**Spec:** `docs/superpowers/specs/2026-11-22-scan-profiles-design.md`

## Global Constraints
- No new modules or JS libraries; migrations are appended only.
- Services jobs never call diff/offline code and never enqueue events.

## Review Focus
1. Details of a device whose key changes (IP-only → MAC) or network is renamed/merged/deleted follow or disappear.
2. A services job must not appear in offline thresholds (would make devices go offline after 18h).
3. Scan with zero online hosts must not run nmap with no targets (nmap would error).
4. On-demand scans and periodic scans of the same network do not corrupt status or run unbounded in parallel.
5. nmap `-O` output with no osmatch or multiple osmatches parses; hosts that are down produce no details row overwrite.

### Task 1: model, store, config
- `model.Port{Proto string; Port int; State, Service, Product, Version, Extra string}`, `model.HostDetails{IP netip.Addr; OS string; OSAccuracy int; Ports []Port}`, `model.DeviceDetails{Network, Key string; ScannedAt time.Time; OS string; OSAccuracy int; Ports []Port}`.
- Migration 4 `device_details(network, key, scanned_at, os, os_accuracy, ports JSON, PK(network,key))`.
- `Store.SaveDetails(ctx, []model.DeviceDetails) error`, `Store.Details(ctx, network string) (map[model.MemberRef]model.DeviceDetails, error)`; details follow PrevKey/MergedInto, RenameNetwork (newer scanned_at wins), DeleteNetworkData, DeleteDevice.
- Config: type `services` valid (no ref, no traceroute, one per network), default interval 6h.
- Tests: round trip; follows PrevKey; rename/delete; config validation and default.

### Task 2: nmap services
- `nmap.ServiceArgs(user []string, targets []netip.Addr) []string`; `nmap.ParseServicesXML([]byte) ([]model.HostDetails, error)` (up hosts only, open ports only, best osmatch); `nmap.NewServices(args []string, exec ExecFunc) *Services` with `Name() "services"` and `Scan(ctx, targets []netip.Addr) ([]model.HostDetails, error)`.
- Tests with fixture `testdata/services.xml`.

### Task 3: scheduler
- `scheduler.DetailScanner` interface `{Name() string; Scan(ctx, []netip.Addr) ([]model.HostDetails, error)}`; `Job.Details DetailScanner` (Source nil); jobs keyed by index for status.
- Details run: online devices of the network → IPs → Scan → map IP→(network,key) → SaveDetails; empty target list = success without running.
- `thresholds` skips details jobs.
- `Scheduler.ScanDevice(network, key string) error` runs an ad-hoc details scan (network's scanner or `DefaultDetails`) through the semaphore; while running it appears in `Status()` with `ID: -1`.
- Tests: details job saves and creates no events; zero targets skips scan; ScanDevice unknown device → error; thresholds ignore details jobs.

### Task 4: web + app
- app DefaultBuild: `services` → `Job{Details: nmap.NewServices(args, nil)}`; scheduler DefaultDetails = `nmap.NewServices(nil, nil)`.
- `GET /api/details?network=` → list; `POST /api/details/scan` `{network,key}` → 202/404.
- Devices page Services column; map panel details; settings source type `services`; jobs.js handles ID -1 (no Scan now button).
- README.
