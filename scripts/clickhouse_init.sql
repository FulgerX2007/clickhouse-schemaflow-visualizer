-- Создание базы данных
CREATE DATABASE IF NOT EXISTS aviation;

-- Справочник cities (Dictionary)
CREATE TABLE IF NOT EXISTS aviation.cities_source (
    city_id UInt32,
    city_name String,
    country String
) ENGINE = TinyLog;

CREATE DICTIONARY IF NOT EXISTS aviation.cities_dict
(
    city_id UInt32,
    city_name String,
    country String
)
PRIMARY KEY city_id
SOURCE(CLICKHOUSE(HOST 'localhost' PORT 9000 USER 'default' TABLE 'cities_source' PASSWORD '' DB 'aviation'))
LAYOUT(HASHED())
LIFETIME(MIN 300 MAX 600);

-- Таблица flights на MergeTree
CREATE TABLE IF NOT EXISTS aviation.flights (
    flight_id UInt32,
    departure_city String,
    arrival_city String,
    departure_time DateTime,
    arrival_time DateTime,
    price Float64,
    seats_available UInt16,
    is_cancelled UInt8,
    passengers_count AggregateFunction(sum, UInt32),
    ticket_prices AggregateFunction(quantiles(0.5, 0.9), Float64)
) ENGINE = MergeTree()
ORDER BY (flight_id);

-- Таблица replicated_flights на ReplicatedMergeTree
CREATE TABLE IF NOT EXISTS aviation.replicated_flights (
    flight_id UInt32,
    departure_city String,
    arrival_city String,
    departure_time DateTime,
    arrival_time DateTime,
    price Float64,
    seats_available UInt16,
    is_cancelled UInt8,
    passengers_count AggregateFunction(sum, UInt32),
    ticket_prices AggregateFunction(quantiles(0.5, 0.9), Float64)
) ENGINE = ReplicatedMergeTree('/clickhouse/tables/{shard}/replicated_flights', '{replica}')
ORDER BY (flight_id);

-- Таблица distributed_flights на Distributed
CREATE TABLE IF NOT EXISTS aviation.distributed_flights AS aviation.flights
ENGINE = Distributed('test_cluster', 'aviation', 'flights', rand());

-- Таблица tickets на StripeLog (лог для билетов)
CREATE TABLE IF NOT EXISTS aviation.tickets (
    ticket_id UInt64,
    flight_id UInt32,
    passenger_name String,
    purchase_time DateTime,
    price Float64,
    seat_number String
) ENGINE = StripeLog;

-- Таблица bookings на SummingMergeTree (агрегирующая бронирования)
CREATE TABLE IF NOT EXISTS aviation.bookings (
    booking_id UInt64,
    flight_id UInt32,
    booking_time DateTime,
    seats_booked UInt16,
    total_price Float64
) ENGINE = SummingMergeTree()
ORDER BY (flight_id, booking_id);

-- Таблица cancelled_flights на Null (для теста пустых данных)
CREATE TABLE IF NOT EXISTS aviation.cancelled_flights (
    flight_id UInt32,
    reason String
) ENGINE = Null;

-- Таблица airport_events на Kafka (пример интеграции с внешним источником)
CREATE TABLE IF NOT EXISTS aviation.airport_events (
    event_id UInt64,
    airport_code String,
    event_type String,
    event_time DateTime
) ENGINE = Kafka
SETTINGS kafka_broker_list = 'localhost:9092',
         kafka_topic_list = 'airport_events',
         kafka_group_name = 'aviation_group',
         kafka_format = 'JSONEachRow';

-- Таблица airport_events_buffer на Buffer (буфер для событий)
CREATE TABLE IF NOT EXISTS aviation.airport_events_buffer AS aviation.airport_events
ENGINE = Buffer('aviation', 'airport_events', 16, 10, 60, 10000, 100000, 1000000, 10000000);

-- Примеры вставки данных
INSERT INTO aviation.cities_source VALUES (1, 'Moscow', 'Russia'), (2, 'London', 'UK'), (3, 'New York', 'USA');

INSERT INTO aviation.flights VALUES (
    1001, 'Moscow', 'London', '2025-06-15 10:00:00', '2025-06-15 12:30:00', 250.0, 120, 0, sumState(100), quantilesState(200.0, 250.0, 300.0)
), (
    1002, 'London', 'New York', '2025-06-16 14:00:00', '2025-06-16 18:00:00', 500.0, 200, 0, sumState(180), quantilesState(450.0, 500.0, 550.0)
);

INSERT INTO aviation.replicated_flights VALUES (
    2001, 'New York', 'Moscow', '2025-06-17 09:00:00', '2025-06-17 19:00:00', 700.0, 150, 0, sumState(130), quantilesState(650.0, 700.0, 750.0)
);

INSERT INTO aviation.tickets VALUES (1, 1001, 'Ivan Petrov', '2025-06-10 09:00:00', 250.0, '12A'), (2, 1002, 'John Smith', '2025-06-12 15:00:00', 500.0, '7B');
INSERT INTO aviation.bookings VALUES (101, 1001, '2025-06-09 08:00:00', 2, 500.0), (102, 1002, '2025-06-13 10:00:00', 3, 1500.0);
INSERT INTO aviation.cancelled_flights VALUES (3001, 'Weather conditions');
