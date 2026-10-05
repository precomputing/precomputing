-- Distill for traces.precompute: run every few minutes with :now bound to the time of the newest event.

-- Stream calls.
DELETE FROM calls_raw WHERE ts < (CAST(:now - 2678400 AS INTEGER) / 86400 * 86400);
DELETE FROM calls_ids WHERE ts < :now - 604800;
DELETE FROM calls_win WHERE res = 3600 AND w <= :now - 34563600;

-- Stream context.
DELETE FROM context_raw WHERE ts < (CAST(:now - 2678400 AS INTEGER) / 86400 * 86400);
DELETE FROM context_ids WHERE ts < :now - 604800;
DELETE FROM context_win WHERE res = 3600 AND w <= :now - 34563600;
