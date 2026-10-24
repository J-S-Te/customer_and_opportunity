# CRM and Portal task authorization

Production Worker commands bind a commercial runtime Check to their cancellation context using a compiled application code, not a caller-controlled override. Every polling task checks current authorization; a startup check is not enough. Unit/embed callers without this context retain existing constructor behavior, while production startup always binds either the explicit disabled compatibility consumer or configured fail-closed consumer.

New CRM approval/action/worklog delivery, contract transfer, engineer-pool synchronization, new SLA alerts and aggregate recomputation require `MUTATE_BUSINESS`. Portal project synchronization, report submission/ingestion, feedback SLA escalation and identity provisioning compensation also require mutation authorization. Claimed queue work denied before business execution stays leased for subsequent recovery; it is not marked sent or dead-lettered merely because a license expired.

Existing owner/assignment/progress/notification deliveries are `ESSENTIAL_SERVICE`; they consume previously committed events and do not authorize new alert scans. Portal project PDF export is `EXPORT_HISTORY` and uses the existing frozen job snapshot. The separate Portal access-disable worker remains a safety operation. The mixed invite compensation worker preserves `DISABLE_PLATFORM_CUSTOMER` safety work but denies role/mapping/binding creation and automatic compensation generation when mutation is unavailable.

Temporal Activity instances receive the same runtime checker before polling starts and recheck on each Activity execution. A typed license-only deferral uses durable workflow timers and the same EventID; it does not exhaust normal external-operation retries or complete the workflow falsely. Authorization is re-evaluated when the timer schedules the Activity again. Transactions/external business operations already started while allowed may finish their existing atomic/idempotent completion path; a license is not used to roll back completed external business.

No business database migration or historical data changes are introduced by these gates. Production Compose must supply a distinct component identity/credential and durable state, and Portal compensation components must be enrolled under `customer_portal` even when their physical tables are in the CRM database.

## Operational verification (2026-10-13)

Operators must identify the compiled application/component and the actual command before inspecting queue state. Start flows through workerlicense.Start/WithCheck; Allowed/Require gates the task before execution. A queue lease or an existing constructor alone is not evidence of authorized business completion.

CRM notification-delivery-worker uses ESSENTIAL_SERVICE and writes platform_delivered_at only after an ACCEPTED/DUPLICATE receipt. Missing notification credentials disables delivery; HTTP 422 becomes a permanent local delivery failure. Database failures can stop Run and require supervisor recovery. Mutation-denied queues must retain their original identity and recovery path; never mark them sent to clear backlog.

The current CRM UI request/approval/worklog flow is local and does not enqueue new PMS worklogs. Legacy presale-worker/Temporal activities remain a separate operational concern. Do not require those deliveries to prove a newly saved local worklog, and do not use local SUCCESS as an external PMS receipt. This review did not start workers, inspect licenses or alter commercial-license documentation.

Evidence: [CRM documentation review](../../../.ai/modules/operation-docs-crm-20261013.md).
