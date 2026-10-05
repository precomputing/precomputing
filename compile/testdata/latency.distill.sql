-- Distill for latency.precompute: run every few minutes with :now bound to the time of the newest event.

-- Stream latency.
DELETE FROM latency_raw WHERE ts < :now - 300;
DELETE FROM latency_win WHERE res = 10 AND w <= :now - 86410;
DELETE FROM latency_win WHERE res = 60 AND w <= :now - 2592060;
DELETE FROM latency_ms_sk WHERE res = 60 AND w <= :now - 2592060;
DELETE FROM latency_sample WHERE res = 60 AND w <= :now - 2592060;
DELETE FROM latency_win WHERE res = 3600 AND w <= :now - 31539600;
DELETE FROM latency_ms_sk WHERE res = 3600 AND w <= :now - 31539600;
