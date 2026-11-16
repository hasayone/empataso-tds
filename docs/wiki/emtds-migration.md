# Переход KuzTDS → EmTDS на сервере

Продукт переименован в EmTDS. Код, образ `emtds:local`, контейнеры `emtds-*`, проект compose `emtds-app`, каталоги `/opt/emtds` и `/etc/emtds`, файлы nginx `emtds*.conf`, юниты `emtds-watchdog.*` и переменные `EMTDS_*` — новые имена. Переход на сервере делает один сценарий `deploy/migrate-emtds.sh`. Данные не удаляются: старые тома и остановленные контейнеры `kuztds-*` остаются до ручной уборки.

## Что остаётся со старым именем

- База и пользователь ClickHouse `kuztds`. Это физические имена в томе `deploy_ch_data`, их переименование — перенос данных. В коде это значения по умолчанию `config.DefaultClickHouseDB` и `config.DefaultClickHouseUser`, миграции SQL пишут в `kuztds.*`.
- Проект инфраструктуры `deploy` (сеть `deploy_default`, тома ClickHouse и Redis). Сценарий их не пересоздаёт и не останавливает.
- Переменные `KUZTDS_*`. Каждое чтение сначала берёт непустое `EMTDS_X`, иначе `KUZTDS_X` (Go — `config.Env`, shell и compose — `${EMTDS_X:-${KUZTDS_X:-…}}`). Старый `.env` работает без правки.
- Старая сессия `kuztds_admin` принимается; при входе и выходе она гасится. Заголовок раздела `X-Kuztds-Board` читается. В PHP-лендинг cid уходит в `X-Emtds-Cid` и `X-Kuztds-Cid`. Проверка домена принимает `X-Kuztds-Probe/Host/Service` от движка старой сборки.
- Ключи `kuztds*` в localStorage и sessionStorage копируются в `emtds*` при открытии панели, существующие `emtds*` не перезаписываются.
- В образе `/var/lib/kuztds` — симлинк на `/var/lib/emtds`.
- `/opt/kuztds` и `/etc/kuztds` после перехода — симлинки на новые каталоги. `apply-edge.sh`, `install.sh` и `watchdog.sh` берут `/etc/kuztds`, только если `/etc/emtds` нет.

## Что делает `migrate-emtds.sh`

1. Проверяет, что сеть `deploy_default` принадлежит проекту `deploy`.
2. Бэкап в `/var/backups/emtds-migrate/<UTC-время>`: `state.tgz` каталога `/etc/kuztds`, `app.env` (права 0600), `nginx/kuztds*.conf`, `nginx/legacy-dir` (копия `/etc/nginx/kuztds`).
3. Выключает старый сторож `kuztds-watchdog.timer`, юниты кладёт в `systemd/` бэкапа и удаляет.
4. Переносит `/opt/kuztds` → `/opt/emtds` и `/etc/kuztds` → `/etc/emtds`, на старых местах оставляет симлинки.
5. В `.env` и `server.env` меняет ключи `KUZTDS_X` на `EMTDS_X`, значения не трогает. Если `EMTDS_X` уже есть, старая строка комментируется. В `compose.app.yml`, `compose.infra.yml` и `up.sh` меняет ключи и пути `/opt`, `/etc`, `/var/lib`. Права и владелец файлов сохраняются.
6. Собирает образ `emtds:local` (`build engine`), пока `kuztds-*` ещё принимают клики. Если сборка упала, старое приложение не останавливалось и продолжает работать.
7. Останавливает `kuztds-engine`, `kuztds-admin`, `kuztds-cron`, `kuztds-php` (`docker stop -t 30`, без удаления).
8. Копирует тома из явного списка `STATE_VOLUMES` (сейчас только `kuztds-app_kuztds_state` → `emtds-app_emtds_state`; другие тома с префиксом `kuztds`, например тестовые кэши, не трогаются). Копия: `docker run --rm`, `cp -a`, затем сверка отпечатка (пути, размеры, права, владельцы). Нет исходного тома — ошибка. Готовность отмечается в `/etc/emtds/migrate/volumes/`. Старый том не удаляется. Целевой том без метки `emtds.migrated-from` не перезаписывается.
9. Поднимает `engine admin php cron` в проекте `emtds-app` (`up -d --no-deps`) из готового образа. Ждёт `clickhouse connected` и `redis connected` в логах engine и admin, проверяет `/healthz` движка и `http://127.0.0.1:8090/api/health`. Адрес движка берётся из `EMTDS_ENGINE_PUBLISH` (или `KUZTDS_ENGINE_PUBLISH`) в `server.env` так же, как в `watchdog.sh` и `apply-edge.sh`; без ключа — `127.0.0.1:18080`.
10. Запускает `apply-edge.sh`: файлы `emtds*.conf` с бэкапом, `nginx -t` и откатом; старые `kuztds*.conf` и `/etc/nginx/kuztds` уходят в бэкап apply-edge.
11. Ставит новый сторож `watchdog.sh --install` (`emtds-watchdog.timer`).

Повторный запуск пропускает сделанное: перенесённые каталоги, переписанные ключи, проверенные тома.

## Порядок перехода

1. `GET /api/keitaro/job` на проде: ждать `running: false`. Перенос живёт в памяти admin и оборвётся, а скрипт не может узнать его состояние сам (эндпоинт закрыт авторизацией). Поэтому боевой запуск требует флага `--keitaro-idle`: им оператор подтверждает `running: false`. Без флага скрипт отказывает до любых изменений; `--dry-run` и `--self-test` флага не требуют.
2. Свободное место: копия тома состояния занимает столько же, сколько оригинал (`docker system df -v`).
3. Залить код в `/opt/kuztds` без `.env` и файлов состояния, как при обычной выкладке.
4. Посмотреть план, ничего не меняя:

   ```bash
   cd /opt/kuztds && bash deploy/migrate-emtds.sh --dry-run
   ```

   В выводе — команды, имена ключей без значений и список томов к копированию.
5. Переход:

   ```bash
   cd /opt/kuztds && bash deploy/migrate-emtds.sh --keitaro-idle
   ```

6. Проверка:
   - `docker ps` — работают `emtds-engine`, `emtds-admin`, `emtds-php`, `emtds-cron`; `kuztds-*` в `Exited`; `deploy-clickhouse` и `deploy-redis` не перезапускались (тот же `Up`).
   - `/api/health` — 200; в логах engine и admin есть `clickhouse connected` и `redis connected`.
   - `https://212.2.33.246/` открывает админку, старая сессия работает без нового входа.
   - Тестовый клик по своему домену доходит до журнала, лендинг PHP открывается.
   - `systemctl status emtds-watchdog.timer` — активен; `kuztds-watchdog.timer` нет.
7. Дальнейшие выкладки — из `/opt/emtds` с `--env-file /etc/emtds/server.env -f /etc/emtds/compose.app.yml`.

## Откат

```bash
bash /opt/emtds/deploy/migrate-emtds.sh --rollback
```

Сценарий выключает `emtds-watchdog.timer` и `emtds-update.timer`, останавливает `emtds-*`, запускает остановленные `kuztds-*` (у них прежние env и том), возвращает `kuztds*.conf` и `/etc/nginx/kuztds` из последнего бэкапа, где они есть, убирает `emtds*.conf` в `nginx-emtds/` бэкапа, проверяет `nginx -t` и перезагружает nginx. Отметки о скопированных томах уходят в бэкап, поэтому повторный переход скопирует тома заново.

После отката:

- Изменения, сделанные в EmTDS после перехода (кампании, файлы), остались только в томах `emtds*`.
- Старый сторож не включается: в `/opt/kuztds` уже код EmTDS. Его юниты лежат в `systemd/` бэкапа.
- Каталоги остаются в `/opt/emtds` и `/etc/emtds` с симлинками; ключи `EMTDS_*` в `.env` новый код читает, старые контейнеры свои env уже несут.

Другой путь без копии тома: новый проект на старом томе. Задать в `/etc/emtds/server.env` `EMTDS_STATE_VOLUME=kuztds-app_kuztds_state` и поднять `engine admin php cron` с флагами выкладки, предварительно остановив `kuztds-*`.

## Уборка

Не раньше, чем EmTDS проработает без замечаний. Вручную и по одному: удалить остановленные `kuztds-*`, затем тома `kuztds*`, затем симлинки `/opt/kuztds` и `/etc/kuztds`. Сценарий этого не делает.
