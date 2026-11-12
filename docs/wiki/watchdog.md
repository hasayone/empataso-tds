# Сторож трекера

Раз в минуту systemd-таймер `kuztds-watchdog.timer` запускает `deploy/watchdog.sh`. Скрипт поднимает Docker, nginx и остановленные контейнеры трекера. Он не делает `docker compose down`, не трогает тома и не пересоздаёт ClickHouse, Redis, php и cron.

## Что проверяет

- `docker.service` и `nginx.service` включены и запущены.
- Сеть `deploy_default`, контейнеры `deploy-clickhouse-1` и `deploy-redis-1`: если стоят — `docker start`, если их нет — `compose up -d` проекта `deploy` без `--force-recreate`.
- `kuztds-engine`, `kuztds-admin`, `kuztds-php`, `kuztds-cron`: остановленные поднимает `docker start`. Пропавший engine или admin — `compose up -d --no-deps` без `--force-recreate`. php и cron заново не создаёт.
- Проба движка `http://127.0.0.1:18080/healthz` (порт из `KUZTDS_ENGINE_PUBLISH`). Если контейнер давно running, а проба молчит — `docker restart kuztds-engine`. Сразу после старта ждёт `start_period`.
- Админка: если контейнер running, restart нет — перенос Keitaro живёт в памяти процесса. Стоящую админку только стартует.
- `http://127.0.0.1:8123/ping` у ClickHouse и `https://127.0.0.1/healthz` через nginx (самоподписанный сертификат).

Два прохода не накладываются: `flock` на `/run/kuztds-watchdog.lock`.

## Установка

На уже установленном сервере из `/opt/kuztds`:

```bash
bash deploy/watchdog.sh --install
```

`install.sh` вызывает то же после первого подъёма стека. Юниты сторожа не мешают повторной установке: фильтр `kuztds*` в `install.sh` пропускает `kuztds-watchdog.*`.

Проверка скрипта без Docker: `bash deploy/watchdog.sh --self-test`.
