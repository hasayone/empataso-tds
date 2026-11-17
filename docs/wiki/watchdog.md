# Сторож трекера

Раз в минуту systemd-таймер `emtds-watchdog.timer` запускает `deploy/watchdog.sh`. Скрипт поднимает Docker, nginx и остановленные контейнеры трекера. Он не делает `docker compose down`, не трогает тома и не пересоздаёт ClickHouse, Redis, php и cron.

## Что проверяет

- `docker.service` и `nginx.service` включены и запущены.
- Сеть `emtds-infra_default`, контейнеры `emtds-infra-clickhouse-1` и `emtds-infra-redis-1`: если стоят — `docker start`, если их нет — `compose -p emtds-infra up -d` без `--force-recreate`.
- `emtds-engine`, `emtds-admin`, `emtds-php`, `emtds-cron`: остановленные поднимает `docker start`. Пропавший engine или admin — `compose up -d --no-deps` без `--force-recreate`. php и cron заново не создаёт.
- Проба движка `http://127.0.0.1:18080/healthz` (порт из `EMTDS_ENGINE_PUBLISH`). Если контейнер давно running, а проба молчит — `docker restart emtds-engine`. Сразу после старта ждёт `start_period`.
- Админка: если контейнер running, restart нет — перенос Keitaro живёт в памяти процесса. Стоящую админку только стартует.
- `http://127.0.0.1:8123/ping` у ClickHouse и `https://127.0.0.1/healthz` через nginx (самоподписанный сертификат).

Два прохода не накладываются: `flock` на `/run/emtds-watchdog.lock`.

Пока агент обновлений держит `/run/emtds-update.lock`, сторож пропускает проход целиком: `flock -n /run/emtds-update.lock true || exit 0` выполняется в начале `main()` до любых проверок.

## Установка

На уже установленном сервере из `/opt/emtds`:

```bash
bash deploy/watchdog.sh --install
```

`install.sh` вызывает то же после первого подъёма стека. Юниты сторожа не мешают повторной установке: фильтр `emtds*` в `install.sh` пропускает `emtds-watchdog.*`.

Проверка скрипта без Docker: `bash deploy/watchdog.sh --self-test`.
