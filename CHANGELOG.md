# Changelog

## Unreleased

* Add opt-in dual-stack VPC support for environments via `network.vpc.ipv6.enabled` on the environment manifest (foundation; task/load-balancer support to follow).
* Enable IPv6 task networking on workloads (Backend Service, Load Balanced Web Service, Worker Service, Scheduled Job) deployed into dual-stack environments. Fargate tasks now receive a global IPv6 address and can reach the IPv6 internet via the Egress-Only Internet Gateway. Windows workloads remain IPv4 with a notice at deploy time.
* Route internet IPv6 traffic to services in dual-stack environments. Shared Application Load Balancers run in `dualstack` mode, security groups accept IPv6 ingress, and Route 53 AAAA records are emitted alongside A records for every LoadBalancedWebService and BackendService alias.
* Enable IPv6 dual-stack networking on environments that import an existing VPC. Copilot validates that the imported VPC and every imported subnet has an IPv6 CIDR block before deploying. The user is responsible for pre-provisioning the EgressOnlyInternetGateway and IPv6 routes on their VPC.
