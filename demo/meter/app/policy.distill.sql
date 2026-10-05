-- Distill for usage.precompute: run every few minutes with :now bound to the time of the newest event.

-- Stream usage.
DELETE FROM usage_raw WHERE ts < CAST(strftime('%s', :now - 7862400, 'unixepoch', 'start of month') AS INTEGER);
DELETE FROM usage_ids WHERE ts < :now - 604800;
DELETE FROM usage_win WHERE res = 3600 AND w <= :now - 34563600;
