# Производительность: индекс cid, тайм-ауты, keepalive, ETag

## Индекс `idx_cid` на `events`

Постбек ищет клик запросом `SELECT … FROM events WHERE cid = ? ORDER BY ts DESC LIMIT 1`. Ключ сортировки `events` — `(group_id, stream_name, ts)`, по `cid` он не помогает, а партиций по дню до 365. Без индекса запрос читает колонку `cid` всей таблицы. Тот же `cid` ищут журнал постбеков (`cid IN (…)`), импорт Keitaro и подзапросы условий.

Индекс: `ALTER TABLE events ADD INDEX IF NOT EXISTS idx_cid cid TYPE bloom_filter(0.01) GRANULARITY 1`.

- `bloom_filter(0.01)` — 1% ложных попаданий. `cid` уникален у клика, поэтому `set` не подходит, а `tokenbf` разбивает строку на токены, что для точного равенства не нужно.
- `GRANULARITY 1` — один фильтр на гранулу в 8192 строки. `cid` разбросан по всей таблице, и при более крупной грануле каждое попадание тянуло бы больше строк. Цена — около 1,2 байта на строку (порядка 120 МБ на 100 млн кликов).
- Где ставится: миграция `migrations/clickhouse/013_events_cid_index.sql` и тот же оператор в `ensureSchema` при старте engine, admin и cron. Это только метаданные: `ALTER` выполняется сразу и куски не переписывает. Без прав на `ALTER` в лог пишется ошибка, процесс работает дальше.
- Индекс получают новые куски: при вставке и при слиянии. Старые куски остаются без индекса, пока не пройдёт ручной `MATERIALIZE INDEX` (ниже). Миграции и движок `MATERIALIZE` не запускают; проверка миграций его не пропускает.
- `conversions` и `postbacks` уже отсортированы по `cid`, индекс им не нужен.

### Поиск клика сначала за 7 дней

`readClick` (`internal/store/conversions.go`) сначала ищет клик с `ts >= now() - INTERVAL 7 DAY`, и только если не нашёл — без границы по времени. Почти все постбеки приходят в первые дни, поэтому запрос читает только свежие партиции. Клик старше недели (в пределах срока хранения 365 дней) по-прежнему находится вторым запросом. Замер на локальной базе после `MATERIALIZE INDEX` свежих партиций: 523 прочитанные строки на постбек вместо 1,58 млн по всей таблице.

### Ручной шаг на проде: индекс для старых кусков

Выполнять в тихий час, а не во время переноса из Keitaro (`GET /api/keitaro/job` → `running: false`).

1. Проверить, что индекс есть: `SELECT name, type_full, granularity FROM system.data_skipping_indices WHERE database = 'emtds' AND table = 'events'`.
2. Оценить объём: `SELECT count() AS parts, sum(rows) AS rows, formatReadableSize(sum(data_compressed_bytes)) AS size FROM system.parts WHERE active AND database = 'emtds' AND table = 'events'`. Мутация читает колонку `cid` каждого куска и пишет рядом файл индекса, примерно 1,2 байта на строку. Свободного места на томе ClickHouse нужно не меньше этого с запасом. Память — небольшая: кусок обрабатывается по гранулам.
3. Запустить по партициям, начиная с самых свежих: по ним идут почти все постбеки. Например:
   `ALTER TABLE emtds.events MATERIALIZE INDEX idx_cid IN PARTITION 20261115`
   Для всей таблицы сразу: `ALTER TABLE emtds.events MATERIALIZE INDEX idx_cid`. Это фоновая мутация, вставки она не блокирует.
4. Следить: `SELECT command, parts_to_do, is_done, latest_fail_reason FROM system.mutations WHERE database = 'emtds' AND table = 'events' AND NOT is_done`. Остановить при необходимости: `KILL MUTATION WHERE database = 'emtds' AND table = 'events' AND command LIKE '%MATERIALIZE INDEX%'`. Уже обработанные куски сохранят индекс.
5. Проверка эффекта без записи: `EXPLAIN indexes = 1 SELECT ts FROM emtds.events WHERE cid = 'nope' ORDER BY ts DESC LIMIT 1`. В блоке `Skip` для `idx_cid` число кусков и гранул должно быть заметно меньше общего.

## Redis у движка

Клик ждёт Redis для уникальности, firewall, лимитов и ротации. Все эти счётчики при ошибке пропускают клик (fail-open). Поэтому у клиента Redis движка короткие тайм-ауты вместо стандартных 3 с go-redis:

| Переменная | По умолчанию | Что ограничивает |
|---|---|---|
| `EMTDS_ENGINE_REDIS_DIAL_TIMEOUT` | `1s` | одно подключение |
| `EMTDS_ENGINE_REDIS_READ_TIMEOUT` | `200ms` | чтение ответа команды |
| `EMTDS_ENGINE_REDIS_WRITE_TIMEOUT` | `200ms` | запись команды |
| `EMTDS_ENGINE_REDIS_POOL_TIMEOUT` | `300ms` | ожидание свободного соединения |

Значение — длительность Go (`250ms`, `2s`). Пустое, неверное или неположительное значение оставляет значение по умолчанию. Подключение к Redis движок делает одной попыткой (у go-redis по умолчанию пять), чтобы недоступный Redis не держал клик пять тайм-аутов подряд. При старте в логе `redis connected` видны действующие значения.

Admin эти переменные не читает: у него свой процесс и стандартные тайм-ауты go-redis.

## HTTP-сервер движка

- `ReadHeaderTimeout` — 5 с.
- `ReadTimeout` — 30 с (`EMTDS_HTTP_READ_TIMEOUT`): заголовки и тело одного запроса. nginx буферизует тело до передачи, поэтому лимит касается только зависшего прямого клиента. По этому сроку net/http отменяет контекст запроса, поэтому он выше самого долгого ожидания внутри обработчика (PHP-лендинг, 10 с).
- `IdleTimeout` — 120 с (`EMTDS_HTTP_IDLE_TIMEOUT`): простой keep-alive соединения. Больше 60 с keepalive у upstream nginx, поэтому соединение из пула первым закрывает nginx.
- `WriteTimeout` не задан: лендинг и `[REMOTE]` могут отдавать ответ дольше.

## nginx: keepalive до движка и админки

`apply-edge.sh` рендерит три файла в `conf.d`, все они в одном http-контексте. Каждый upstream объявлен один раз:

- `emtds_engine` (`server __ENGINE_UPSTREAM__; keepalive 32;`) — в `emtds-engine.conf`. Его используют `location /` движка и `location = /postback` в `emtds-admin-host.conf` и `emtds.conf`.
- `emtds_admin` (`server 127.0.0.1:8090; keepalive 16;`) — в `emtds.conf`. Его используют вход по IP и `admin.*`.

В каждом `location` с `proxy_pass` на upstream стоят `proxy_http_version 1.1` и `proxy_set_header Connection ""`, иначе nginx закрывает соединение после каждого ответа. WebSocket в продукте нет, `Upgrade` не пробрасывается. Заголовки `Host`, `X-Forwarded-For $remote_addr`, `X-Forwarded-Proto`, `CF-Connecting-IP`, `CF-IPCountry` и `real_ip_header` не менялись. Если в upstream один сервер, nginx не исключает его по `max_fails`.

Выкладка — обычный `bash deploy/apply-edge.sh` (бэкап, `nginx -t`, откат). Перед этим `bash deploy/apply-edge.sh --check` покажет отрендеренные файлы без записи. Руками nginx на сервере не правят.

### Лимиты соединений nginx: `--tune-events`

В Ubuntu по умолчанию `worker_connections 768` на воркер. Флаг `bash deploy/apply-edge.sh --tune-events` (только по явной просьбе) поднимает в `/etc/nginx/nginx.conf` `worker_connections` в блоке `events` до 4096 и `worker_rlimit_nofile` в главном контексте до 16384. Большие значения не уменьшаются, отсутствующие директивы добавляются, повторный запуск ничего не меняет.

- `nginx.conf` попадает в тот же бэкап, что и файлы `conf.d`, и восстанавливается при ошибке `nginx -t` или сбое любой записи (`set -E` и ловушка `ERR`). Права файла сохраняются, символьная ссылка разыменовывается.
- Действующие значения определяются по `nginx -T`. Если директива задана в подключаемом файле или блок `events` не распознан однозначно, скрипт останавливается с понятной ошибкой и ничего не дописывает.
- `--check` показывает текущие `worker_connections` и `worker_rlimit_nofile` (с учётом контекста) и что изменит `--tune-events`.
- Без флага `nginx.conf` не трогается. `keepalive_timeout` клиентов (300 с) не менялся.

## ETag у страницы админки

`GET /` отдаёт встроенный `index.html` (около 1,2 МБ). Файл не шаблонизируется, байты одинаковы во всех ответах, поэтому хеш SHA-256 считается один раз при старте.

- Без сжатия: строгий `ETag: "<хеш>"`.
- Со сжатием gzip: `ETag: W/"<хеш>-gzip"`. Байты gzip зависят от компрессора, поэтому тег слабый и отдельный.
- `Vary: Accept-Encoding` — один раз в ответе. `Cache-Control: no-cache` остаётся, браузер каждый раз переспрашивает.
- `If-None-Match` с тем же тегом (сравнение слабое, `*` тоже подходит) → `304` без тела. Тег от другого кодирования не подходит.

Новый образ с изменённым `index.html` даёт новый хеш, и браузер получает новую страницу при первом же запросе.
