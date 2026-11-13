-- Sample data for the development stack and the end-to-end tests. Hardcharger runs this at startup.
-- Both tables are views over the current time, so the last week always has data: no loader, no cron.

CREATE SCHEMA IF NOT EXISTS demo;

-- One row per host and minute for the last 7 days.
CREATE OR REPLACE VIEW demo.metrics AS
SELECT
    ts,
    host,
    region,
    round(greatest(0, least(100,
        45 + 25 * sin(epoch(ts) / 3600.0 + n) + 10 * sin(epoch(ts) / 420.0 + 2 * n)
        + (hash(epoch(ts)::BIGINT, n) % 800) / 100.0)), 2) AS cpu_percent,
    CAST(2000 + 600 * n + 400 * sin(epoch(ts) / 7200.0 + n) + (hash(n, epoch(ts)::BIGINT) % 20000) / 100.0 AS BIGINT)
        AS memory_mb,
    CAST(120 + 80 * sin(epoch(ts) / 1800.0 + n / 2.0) + hash(epoch(ts)::BIGINT + n) % 40 AS INTEGER) AS requests
FROM (
    SELECT unnest(generate_series(
        date_trunc('minute', now()) - INTERVAL 7 DAY, date_trunc('minute', now()), INTERVAL 1 MINUTE)) AS ts
) AS minutes
CROSS JOIN (
    VALUES (0, 'web-1', 'us-east'), (1, 'web-2', 'us-east'), (2, 'db-1', 'eu-west'), (3, 'db-2', 'eu-west')
) AS hosts(n, host, region);

-- A deployment every 3 hours on alternating hosts, for annotations.
CREATE OR REPLACE VIEW demo.events AS
SELECT
    ts,
    'deploy' AS kind,
    'Deployed build ' || CAST(epoch(ts)::BIGINT / 10800 % 1000 AS VARCHAR) || ' to '
        || CASE WHEN epoch(ts)::BIGINT / 10800 % 2 = 0 THEN 'web-1' ELSE 'web-2' END AS message,
    'deploy,' || CASE WHEN epoch(ts)::BIGINT / 10800 % 2 = 0 THEN 'web-1' ELSE 'web-2' END AS tags
FROM (
    SELECT unnest(generate_series(
        date_trunc('day', now()) - INTERVAL 7 DAY, now(), INTERVAL 3 HOUR)) AS ts
) AS deployments;
