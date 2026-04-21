# Changelog

## Unreleased

* Add opt-in dual-stack VPC support for environments via `network.vpc.ipv6.enabled` on the environment manifest (foundation; task/load-balancer support to follow).
* Enable IPv6 task networking on workloads (Backend Service, Load Balanced Web Service, Worker Service, Scheduled Job) deployed into dual-stack environments. Fargate tasks now receive a global IPv6 address and can reach the IPv6 internet via the Egress-Only Internet Gateway. Windows workloads remain IPv4 with a notice at deploy time.
