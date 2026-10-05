-- Distill for trades.precompute: run every few minutes with :now bound to the time of the newest event.

-- Stream trades.
DELETE FROM trades_raw WHERE ts < :now - 300;
DELETE FROM trades_win WHERE res = 1 AND w <= :now - 3601;
DELETE FROM trades_win WHERE res = 60 AND w <= :now - 7776060;
