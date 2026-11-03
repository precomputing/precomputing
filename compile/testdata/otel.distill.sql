-- Distill for otel.precompute: run every few minutes with :now bound to the time of the newest event.

-- Stream errors.
DELETE FROM errors_raw WHERE ts < :now - 172800;
DELETE FROM errors_win WHERE res = 60 AND w <= :now - 2592060;

-- Stream requests.
DELETE FROM requests_win WHERE res = 60 AND w <= :now - 2592060;
DELETE FROM requests_win WHERE res = 3600 AND w <= :now - 31539600;

-- Stream queue.
DELETE FROM queue_win WHERE res = 60 AND w <= :now - 604860;
