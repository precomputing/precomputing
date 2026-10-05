-- Distill for wikipedia.precompute: run every few minutes with :now bound to the time of the newest event.

-- Stream edits.
DELETE FROM edits_raw WHERE ts < :now - 600;
DELETE FROM edits_win WHERE res = 10 AND w <= :now - 3610;
DELETE FROM edits_win WHERE res = 60 AND w <= :now - 604860;
DELETE FROM edits_win WHERE res = 3600 AND w <= :now - 31539600;

-- Stream article_edits.
DELETE FROM article_edits_win WHERE res = 60 AND w <= :now - 3660;

-- Stream article_editors.
DELETE FROM article_editors_win WHERE res = 60 AND w <= :now - 3660;
