-- Demo schema for the ClickHouse Schema Flow Visualizer.
--
-- Layout:
--   raw         - airports + flights (MergeTree, ReplicatedMergeTree, Distributed)
--   aggregated  - daily route stats (ReplicatedAggregatingMergeTree) and
--                 hourly airport traffic (SummingMergeTree), each fed by a
--                 Materialized View from raw.flights_local, plus Distributed
--                 wrappers.
--
-- Order is significant: the Materialized Views must exist before the seed
-- INSERT into raw.flights_local so they capture the seed rows.

CREATE DATABASE IF NOT EXISTS raw;
CREATE DATABASE IF NOT EXISTS aggregated;

-- ---------------------------------------------------------------------------
-- raw: local tables
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS raw.airports_local
(
    code    String,
    name    String,
    city    String,
    country String,
    lat     Float64,
    lon     Float64
)
ENGINE = MergeTree
ORDER BY code;

CREATE TABLE IF NOT EXISTS raw.flights_local
(
    flight_id           UInt64,
    flight_number       String,
    airline_code        LowCardinality(String),
    origin              String,
    destination         String,
    scheduled_departure DateTime,
    actual_departure    DateTime,
    delay_minutes       Int32,
    status              LowCardinality(String)
)
ENGINE = ReplicatedMergeTree('/clickhouse/tables/{shard}/raw/flights_local', '{replica}')
PARTITION BY toYYYYMM(scheduled_departure)
ORDER BY (toDate(scheduled_departure), origin, destination, flight_id);

-- ---------------------------------------------------------------------------
-- aggregated: local tables
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS aggregated.flight_stats_daily_local
(
    day           Date,
    origin        String,
    destination   String,
    flights_count AggregateFunction(count, UInt64),
    avg_delay     AggregateFunction(avg, Int32),
    max_delay     AggregateFunction(max, Int32)
)
ENGINE = ReplicatedAggregatingMergeTree(
    '/clickhouse/tables/{shard}/aggregated/flight_stats_daily_local',
    '{replica}')
ORDER BY (day, origin, destination);

CREATE TABLE IF NOT EXISTS aggregated.airport_traffic_hourly_local
(
    hour        DateTime,
    airport     String,
    flights     UInt64,
    total_delay Int64
)
ENGINE = SummingMergeTree
ORDER BY (hour, airport);

-- ---------------------------------------------------------------------------
-- aggregated: Materialized Views (must exist before seed INSERTs below)
-- ---------------------------------------------------------------------------

CREATE MATERIALIZED VIEW IF NOT EXISTS aggregated.flight_stats_daily_mv
TO aggregated.flight_stats_daily_local AS
SELECT
    toDate(scheduled_departure) AS day,
    origin,
    destination,
    countState()                AS flights_count,
    avgState(delay_minutes)     AS avg_delay,
    maxState(delay_minutes)     AS max_delay
FROM raw.flights_local
GROUP BY day, origin, destination;

CREATE MATERIALIZED VIEW IF NOT EXISTS aggregated.airport_traffic_hourly_mv
TO aggregated.airport_traffic_hourly_local AS
SELECT
    toStartOfHour(scheduled_departure) AS hour,
    origin                              AS airport,
    toUInt64(1)                         AS flights,
    toInt64(delay_minutes)              AS total_delay
FROM raw.flights_local;

-- ---------------------------------------------------------------------------
-- Distributed wrappers (one per local table on the test_cluster cluster)
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS raw.airports AS raw.airports_local
ENGINE = Distributed(test_cluster, 'raw', 'airports_local', rand());

CREATE TABLE IF NOT EXISTS raw.flights AS raw.flights_local
ENGINE = Distributed(test_cluster, 'raw', 'flights_local', rand());

CREATE TABLE IF NOT EXISTS aggregated.flight_stats_daily
    AS aggregated.flight_stats_daily_local
ENGINE = Distributed(test_cluster, 'aggregated', 'flight_stats_daily_local');

CREATE TABLE IF NOT EXISTS aggregated.airport_traffic_hourly
    AS aggregated.airport_traffic_hourly_local
ENGINE = Distributed(test_cluster, 'aggregated', 'airport_traffic_hourly_local');

-- ---------------------------------------------------------------------------
-- Seed: 10 airports
-- ---------------------------------------------------------------------------

INSERT INTO raw.airports_local (code, name, city, country, lat, lon) VALUES
    ('JFK', 'John F. Kennedy International Airport', 'New York',     'USA',         40.6413,  -73.7781),
    ('LAX', 'Los Angeles International Airport',     'Los Angeles',  'USA',         33.9416, -118.4085),
    ('LHR', 'London Heathrow Airport',               'London',       'UK',          51.4700,   -0.4543),
    ('CDG', 'Charles de Gaulle Airport',             'Paris',        'France',      49.0097,    2.5479),
    ('NRT', 'Narita International Airport',          'Tokyo',        'Japan',       35.7720,  140.3929),
    ('FRA', 'Frankfurt Airport',                     'Frankfurt',    'Germany',     50.0379,    8.5622),
    ('AMS', 'Amsterdam Schiphol Airport',            'Amsterdam',    'Netherlands', 52.3105,    4.7683),
    ('DXB', 'Dubai International Airport',           'Dubai',        'UAE',         25.2532,   55.3657),
    ('SIN', 'Singapore Changi Airport',              'Singapore',    'Singapore',    1.3644,  103.9915),
    ('HKG', 'Hong Kong International Airport',       'Hong Kong',    'China',       22.3080,  113.9185);

-- ---------------------------------------------------------------------------
-- Seed: ~50 flights across 3 days. Last so the MVs above capture every row.
-- ---------------------------------------------------------------------------

INSERT INTO raw.flights_local
    (flight_id, flight_number, airline_code, origin, destination,
     scheduled_departure, actual_departure, delay_minutes, status) VALUES
    -- 2026-05-10
    (1001, 'AA101', 'AA', 'JFK', 'LHR', '2026-05-10 06:00:00', '2026-05-10 06:00:00',   0, 'on_time'),
    (1002, 'BA178', 'BA', 'LHR', 'JFK', '2026-05-10 08:30:00', '2026-05-10 08:55:00',  25, 'delayed'),
    (1003, 'AF11',  'AF', 'CDG', 'JFK', '2026-05-10 09:00:00', '2026-05-10 09:00:00',   0, 'on_time'),
    (1004, 'LH400', 'LH', 'FRA', 'JFK', '2026-05-10 10:15:00', '2026-05-10 11:00:00',  45, 'delayed'),
    (1005, 'JL5',   'JL', 'NRT', 'LAX', '2026-05-10 11:00:00', '2026-05-10 11:00:00',   0, 'on_time'),
    (1006, 'EK205', 'EK', 'DXB', 'JFK', '2026-05-10 12:30:00', '2026-05-10 14:00:00',  90, 'delayed'),
    (1007, 'SQ21',  'SQ', 'SIN', 'JFK', '2026-05-10 13:45:00', '2026-05-10 13:45:00',   0, 'on_time'),
    (1008, 'CX846', 'CX', 'HKG', 'LAX', '2026-05-10 14:00:00', '2026-05-10 15:00:00',  60, 'delayed'),
    (1009, 'KL643', 'KL', 'AMS', 'JFK', '2026-05-10 15:00:00', '2026-05-10 15:00:00',   0, 'on_time'),
    (1010, 'NH106', 'NH', 'NRT', 'LAX', '2026-05-10 16:30:00', '2026-05-10 19:30:00', 180, 'delayed'),
    (1011, 'AA106', 'AA', 'LAX', 'LHR', '2026-05-10 18:00:00', '2026-05-10 18:00:00',   0, 'on_time'),
    (1012, 'BA284', 'BA', 'LHR', 'LAX', '2026-05-10 19:30:00', '2026-05-10 19:30:00',   0, 'on_time'),
    (1013, 'AF65',  'AF', 'CDG', 'LAX', '2026-05-10 20:00:00', '2026-05-10 20:15:00',  15, 'delayed'),
    (1014, 'LH452', 'LH', 'FRA', 'LAX', '2026-05-10 21:00:00', '2026-05-10 21:00:00',   0, 'on_time'),
    (1015, 'EK241', 'EK', 'DXB', 'LAX', '2026-05-10 22:00:00', '2026-05-10 22:00:00',   0, 'cancelled'),
    (1016, 'SQ23',  'SQ', 'SIN', 'LAX', '2026-05-10 23:00:00', '2026-05-10 23:30:00',  30, 'delayed'),
    (1017, 'CX880', 'CX', 'HKG', 'JFK', '2026-05-10 23:30:00', '2026-05-10 23:30:00',   0, 'on_time'),
    -- 2026-05-11
    (1018, 'AA107', 'AA', 'LHR', 'JFK', '2026-05-11 06:30:00', '2026-05-11 06:40:00',  10, 'delayed'),
    (1019, 'BA179', 'BA', 'JFK', 'LHR', '2026-05-11 07:00:00', '2026-05-11 07:00:00',   0, 'on_time'),
    (1020, 'AF12',  'AF', 'JFK', 'CDG', '2026-05-11 08:00:00', '2026-05-11 08:50:00',  50, 'delayed'),
    (1021, 'LH401', 'LH', 'JFK', 'FRA', '2026-05-11 09:00:00', '2026-05-11 09:00:00',   0, 'on_time'),
    (1022, 'JL6',   'JL', 'LAX', 'NRT', '2026-05-11 10:00:00', '2026-05-11 12:00:00', 120, 'delayed'),
    (1023, 'EK206', 'EK', 'JFK', 'DXB', '2026-05-11 11:00:00', '2026-05-11 11:00:00',   0, 'on_time'),
    (1024, 'SQ22',  'SQ', 'JFK', 'SIN', '2026-05-11 12:00:00', '2026-05-11 12:00:00',   0, 'cancelled'),
    (1025, 'CX847', 'CX', 'LAX', 'HKG', '2026-05-11 13:00:00', '2026-05-11 13:20:00',  20, 'delayed'),
    (1026, 'KL644', 'KL', 'JFK', 'AMS', '2026-05-11 14:00:00', '2026-05-11 14:00:00',   0, 'on_time'),
    (1027, 'NH107', 'NH', 'LAX', 'NRT', '2026-05-11 15:00:00', '2026-05-11 15:05:00',   5, 'delayed'),
    (1028, 'AA200', 'AA', 'JFK', 'LAX', '2026-05-11 16:00:00', '2026-05-11 16:00:00',   0, 'on_time'),
    (1029, 'BA300', 'BA', 'LHR', 'FRA', '2026-05-11 17:00:00', '2026-05-11 17:35:00',  35, 'delayed'),
    (1030, 'AF80',  'AF', 'CDG', 'AMS', '2026-05-11 18:00:00', '2026-05-11 18:00:00',   0, 'on_time'),
    (1031, 'LH900', 'LH', 'FRA', 'AMS', '2026-05-11 19:00:00', '2026-05-11 19:00:00',   0, 'on_time'),
    (1032, 'EK500', 'EK', 'DXB', 'SIN', '2026-05-11 20:00:00', '2026-05-11 21:15:00',  75, 'delayed'),
    (1033, 'SQ800', 'SQ', 'SIN', 'HKG', '2026-05-11 21:00:00', '2026-05-11 21:00:00',   0, 'on_time'),
    (1034, 'CX900', 'CX', 'HKG', 'NRT', '2026-05-11 22:00:00', '2026-05-11 22:15:00',  15, 'delayed'),
    -- 2026-05-12
    (1035, 'AA300', 'AA', 'LAX', 'JFK', '2026-05-12 06:00:00', '2026-05-12 06:00:00',   0, 'on_time'),
    (1036, 'BA200', 'BA', 'LHR', 'CDG', '2026-05-12 07:30:00', '2026-05-12 07:40:00',  10, 'delayed'),
    (1037, 'AF13',  'AF', 'CDG', 'LHR', '2026-05-12 08:00:00', '2026-05-12 08:00:00',   0, 'on_time'),
    (1038, 'LH402', 'LH', 'FRA', 'LHR', '2026-05-12 09:00:00', '2026-05-12 09:25:00',  25, 'delayed'),
    (1039, 'JL7',   'JL', 'NRT', 'HKG', '2026-05-12 10:00:00', '2026-05-12 10:00:00',   0, 'on_time'),
    (1040, 'EK207', 'EK', 'DXB', 'LHR', '2026-05-12 11:00:00', '2026-05-12 12:00:00',  60, 'delayed'),
    (1041, 'SQ24',  'SQ', 'SIN', 'LHR', '2026-05-12 12:00:00', '2026-05-12 12:00:00',   0, 'on_time'),
    (1042, 'CX848', 'CX', 'HKG', 'LHR', '2026-05-12 13:00:00', '2026-05-12 13:00:00',   0, 'cancelled'),
    (1043, 'KL645', 'KL', 'AMS', 'LHR', '2026-05-12 14:00:00', '2026-05-12 14:05:00',   5, 'delayed'),
    (1044, 'NH108', 'NH', 'NRT', 'FRA', '2026-05-12 15:00:00', '2026-05-12 15:00:00',   0, 'on_time'),
    (1045, 'AA400', 'AA', 'JFK', 'FRA', '2026-05-12 16:00:00', '2026-05-12 17:30:00',  90, 'delayed'),
    (1046, 'BA400', 'BA', 'LHR', 'AMS', '2026-05-12 17:00:00', '2026-05-12 17:00:00',   0, 'on_time'),
    (1047, 'AF14',  'AF', 'CDG', 'FRA', '2026-05-12 18:00:00', '2026-05-12 18:20:00',  20, 'delayed'),
    (1048, 'LH600', 'LH', 'FRA', 'DXB', '2026-05-12 19:00:00', '2026-05-12 19:00:00',   0, 'on_time'),
    (1049, 'EK600', 'EK', 'DXB', 'NRT', '2026-05-12 20:00:00', '2026-05-12 20:45:00',  45, 'delayed'),
    (1050, 'SQ900', 'SQ', 'SIN', 'NRT', '2026-05-12 21:00:00', '2026-05-12 21:00:00',   0, 'on_time'),
    (1051, 'CX700', 'CX', 'HKG', 'SIN', '2026-05-12 22:00:00', '2026-05-12 22:30:00',  30, 'delayed');
