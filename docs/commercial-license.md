# CRM / Customer Portal commercial runtime

CRM and Customer Portal are separate licensed applications even though they share
this repository. Each API and Worker requires an independent managed runtime
Service ID, OAuth Client/Secret (`license.runtime` only), and persisted state file.
Do not reuse audit, catalog publisher or interactive-login credentials.

`COMMERCIAL_LICENSE_ENABLED=false` is the compatibility default before controlled
enrollment. Set true only via the reviewed platform process. Invalid trust/binding
or incomplete credential configuration refuses startup; absent or invalid signed
state refuses business operations. A platform outage never extends license expiry.

Common `COMMERCIAL_LICENSE_` variables: INSTANCE_ID, ENVIRONMENT, SERVICE_ID,
STATE_PATH, PLATFORM_PUBLIC_KEY_PATH, PLATFORM_BASE_URL, CLIENT_ID, CLIENT_SECRET,
COVERAGE_DIGEST, IMAGE_DIGEST, ALLOW_HTTP. Digests are SHA-256 with `sha256:` prefix.
PLATFORM_PUBLIC_KEY_PATH is read-only Ed25519 public-key PEM. The real vendor key
is embedded in the reviewed consumer; deployment cannot replace it. ENVIRONMENT
uses the commercial installation binding, not OAuth's `prod`/`dev` alias. HTTP
requires controlled explicit ALLOW_HTTP=true. Credentials and state directories
must be per component; secrets must not enter source control or logs.

API policy uses a concrete reviewed route map, not the HTTP method as a license
policy. Unknown routes are business mutations. Existing historical queries and
customer/report/project/filing export remain allowed after expiry, with unchanged
identity, permission and data-scope checks. Portal GET /activate is a business
operation, whereas login/logout, account safety revocation, personal read markers
and CRM invite verification remain operational. Invite consumption and new account
provisioning do not inherit that exemption.

`internal/commercial.Start(ctx, application)` installs the real shared consumer.
API shutdown cancels synchronization. Worker integration must check each task at
its execution and persistence boundary, not just during startup; already generated
notification delivery and historical export must not be classified as new business.

Independent CI/Docker consume only `third_party/license-core` reviewed production
sources and check their file allowlist and SHA-256 manifest. Sync with
`sh scripts/license-core-sync.sh --sync ../license-core` only when intentionally
updating that shared source, then review both source and manifest. No sibling
workspace is required for production builds.

Code integration does not prove enrollment or deployment. Platform must deliver
each component's binding and verify ready/snapshot/ACK before ENFORCED. Current
tests use isolated in-memory fixtures; they do not write the CRM business database.
