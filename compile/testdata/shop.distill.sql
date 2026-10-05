-- Distill for shop.precompute: run every few minutes with :now bound to the time of the newest event.

-- Stream lines.
DELETE FROM lines_raw WHERE ts < :now - 172800;
DELETE FROM lines_win WHERE res = 60 AND w <= :now - 2592060;
DELETE FROM lines_win WHERE res = 600 AND w <= :now - 2592600;
DELETE FROM lines_sample WHERE res = 600 AND w <= :now - 2592600;

-- Stream errors.
DELETE FROM errors_win WHERE res = 60 AND w <= :now - 2592060;
DELETE FROM errors_sample WHERE res = 60 AND w <= :now - 2592060;

-- Stream web_by_route.
DELETE FROM web_by_route_win WHERE res = 60 AND w <= :now - 2592060;

-- Stream web_ms_by_route.
DELETE FROM web_ms_by_route_win WHERE res = 60 AND w <= :now - 2592060;
DELETE FROM web_ms_by_route_ms_sk WHERE res = 60 AND w <= :now - 2592060;

-- Stream error_by_service.
DELETE FROM error_by_service_win WHERE res = 60 AND w <= :now - 2592060;

-- Stream payments_by_provider_result.
DELETE FROM payments_by_provider_result_win WHERE res = 60 AND w <= :now - 2592060;

-- Stream checkout_total.
DELETE FROM checkout_total_win WHERE res = 60 AND w <= :now - 2592060;

-- Stream search_by_q.
DELETE FROM search_by_q_win WHERE res = 60 AND w <= :now - 2592060;
