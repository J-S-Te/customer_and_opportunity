-- Controlled rollback only. Stop Portal internal callers before removing replay state.
DROP TABLE IF EXISTS portal_machine_request_replays;
