#!/bin/bash
# Скрипт инициализации ClickHouse

set -e

CLICKHOUSE_CLIENT="clickhouse-client --host=localhost --port=9000 --user=default"

# Ждём запуска ClickHouse
until $CLICKHOUSE_CLIENT --query "SELECT 1" &>/dev/null; do
  echo "Ожидание запуска ClickHouse..."
  sleep 2
done

echo "Выполняется инициализация схемы..."
$CLICKHOUSE_CLIENT < /scripts/clickhouse_init.sql

echo "Инициализация завершена."
