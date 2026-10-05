-- Distill for dejavu.precompute: run every few minutes with :now bound to the time of the newest event.

-- Stream lines.
DELETE FROM lines_raw WHERE ts < :now - 600;
DELETE FROM lines_win WHERE res = 60 AND w <= :now - 604860;
DELETE FROM lines_win WHERE res = 3600 AND w <= :now - 7779600;
