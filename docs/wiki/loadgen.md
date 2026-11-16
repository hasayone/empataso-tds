# Нагрузка

`cmd/loadgen` шлёт клики на тестовые кампании движка и печатает сводку. В образ `emtds:local` утилита не входит: образ собирает engine, admin и cron. Её запускают из каталога `tds` образом `emtds-src`. Стреляют только по тестовым кампаниям.

Образ собирается один раз:

```bash
docker build -f Dockerfile --target build -t emtds-src .
```

Запуск, сеть стенда `deploy_default`, движок по имени `engine`:

```bash
docker run --rm --network deploy_default -v <tds>:/src -w /src emtds-src go run ./cmd/loadgen -url http://engine:8080 -campaigns loadtest-1,loadtest-2 -n 5000 -duration 60s
```

`<tds>` — путь к каталогу `tds` на хосте. В `-campaigns` подставляют id тестовых кампаний.

## Флаги

| Флаг | По умолчанию | Смысл |
| --- | --- | --- |
| `-url` | `http://engine:8080` | база движка, завершающий `/` снимается |
| `-campaigns` | пусто, флаг обязателен | id кампаний через запятую |
| `-n` | `5000` | сколько запросов отправить |
| `-duration` | `60s` | окно, по которому запросы размазываются; `0` и меньше — без паузы |
| `-concurrency` | `32` | число работников |
| `-timeout` | `5s` | предел одного запроса |
| `-report` | `text` | `text` или `json` |

Кампании идут по кругу. Запрос — `GET {url}/{id}?q=…&sub_id_3=…`. Слово `q` и модель `sub_id_3` выбираются из короткого набора. User-Agent, язык и реферер тоже меняются; пустой User-Agent уходит пустым, язык иногда не ставится, реферер есть у половины кликов. Редирект утилита не следует. Ctrl+C останавливает набор и всё равно печатает то, что успело завершиться; отменённые запросы в сводку не входят. Код выхода `1` — неверные флаги или хотя бы одна ошибка запроса.

Текст сводки:

```text
sent N errors N
elapsed … rps …
p50 … p95 … p99 … max …
started … finished …
status <код> <число>
campaign <id> <число>
error <текст>
```

`-report json` печатает тот же отчёт объектом: `sent`, `status`, `campaigns`, `errors`, `error_texts` (не больше пяти), `p50`, `p95`, `p99`, `max`, `elapsed`, `rps`, `started`, `finished`. Времена в JSON — наносекунды.

## Что смотреть

До и после прогона — `GET /healthz` движка. Заголовки `X-Events-Lost` и `X-Events-Lost-Detail`. Во втором четыре счётчика: `full`, `queue`, `insert`, `late`.

Ответ клика не ждёт записи в ClickHouse. Число строк сверяют с `sent` по `group_id` кампании в таблице `events`. Ноль в обоих заголовках и столько же строк, сколько запросов, значит потерь нет.

Уборка тестовых данных: `DELETE /api/logs?group=<id>` снимает журнал кликов этой кампании. Маршрут требует администратора; пустой `group` отвечает `400`, успех — `200` и `{"status":"deleted"}`. `DELETE /api/groups/<id>` снимает саму кампанию (раздел `flows`). Нет такого id — `404`, успех — `200` и `{"status":"deleted"}`.

## Нагрузка на реальных кампаниях: `deploy/loadtest`

`cmd/loadgen` стреляет по тестовым кампаниям. Для замеров на реальных кампаниях после импорта Keitaro, со сбоями и с проверкой по ClickHouse есть `deploy/loadtest/run.ps1` (только локально, Windows PowerShell). Генератор — `grafana/k6` в своём контейнере с лимитами CPU и памяти в сети `deploy_default`.

| Действие | Что делает |
|---|---|
| `prepare` | копирует `groups.json` из engine, берёт реальные клики из ClickHouse и строит `targets.json` (`targets.mjs`) |
| `engine-load` | пересоздаёт engine с прод-лимитами (`-EngineMem`, `-EngineCpus`, `GOMEMLIMIT`) |
| `engine-normal` | возвращает engine к обычным локальным настройкам |
| `engine-recreate` | пересоздаёт только engine |
| `clicks` | прогон кликов: `-Rate` и `-Duration` или `-Stages "rate:duration,..."` |
| `chaos` | прогон кликов со сбоем `-ChaosTarget clickhouse\|redis\|engine` через `-ChaosAt` с на `-ChaosFor` с |
| `postbacks` | `-Count` постбеков по кликам прогона `-FromRun` с частотой `-Rate` (можно с `-ChaosTarget`) |
| `cleanup` | удаляет тестовые строки и ключи Redis тестовых адресов |
| `job` | состояние переноса Keitaro |

Пример: `powershell -NoProfile -ExecutionPolicy Bypass -File deploy\loadtest\run.ps1 -Action chaos -Run ch60 -Rate 167 -Duration 120s -ChaosTarget clickhouse -ChaosAt 20 -ChaosFor 60`.

- **Что исключено из целей.** Кампании с лендингами, ссылками на другие кампании и исходящими шаблонами постбеков. PHP-лендинги делают внешние запросы; тестовые клики не должны уходить наружу. После прогона отчёт проверяет `landings_served = 0` и `outbound_sent = 0`.
- **Сбои** — только `docker pause` и `docker unpause` контейнеров `deploy-clickhouse-1` и `deploy-redis-1`, без `stop`, `rm` и `down`. Контейнер снимается с паузы и при ошибке, и при Ctrl+C. Пересоздание engine требует простоя переноса Keitaro (`running` и `sync_running` — `false`). ClickHouse, Redis и cron не пересоздаются.
- **Метки.** Клики несут `sub_id_12 = lt-<прогон>-<n>`. `cleanup` удаляет `events` с `lt-` за два дня лёгким `DELETE`, конверсии и `postback_log` тестовых кликов, ключи `uniq*` и `st:` тестовых адресов.
- **Отчёт** — JSON и Markdown в `%LOCALAPPDATA%\emtds-load\<прогон>.report.*`: отправлено, ответы по классам, записано, «ответили, но не записали», задержки, CPU и память, пики очереди и спула из `GET /internal/metrics`, приросты потерь engine.
- **Цели устаревают.** Если перенос Keitaro удалил кампанию, engine отвечает на её адрес пустым `200` без записи, и отчёт покажет «ответили, но не записали». Перед замером повторяют `prepare`.

Бенчмарки горячего пути: `cmd/engine/hotpath_bench_test.go` (`BenchmarkCatalogs`, `BenchmarkRootOfferStream`, `BenchmarkRootBotPath`), запуск через `deploy/gotest.ps1`.

## Замер 17.10.2026

Локальный стенд. 5000 запросов за 60 с на 5 кампаний. 0 ошибок, 0 потерь. p50 ≈ 0,45–0,83 мс, p99 ≈ 0,9–1,6 мс.
