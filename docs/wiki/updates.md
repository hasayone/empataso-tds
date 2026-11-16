# Обновление EmTDS

Агент обновлений — хостовый скрипт `deploy/update.sh` (работает как root). Обновления применяются только вручную: командой из CLI или кнопкой в интерфейсе. Таймер `emtds-update.timer` самостоятельно применение не инициирует — он делает периодическую проверку и обрабатывает запросы кнопки.

## Версии — где видно

- **`GET /api/version`** (только администратор) — возвращает `admin`, `engine`, `cron` (commit, version, channel), образы Docker трёх контейнеров и флаг рассинхрона.
- **Вкладка Профиль → Обновления** в браузере — те же данные плюс доступное обновление, список коммитов, заметки к релизу и команды CLI.
- **Логи** — строки `engine listening`, `admin api listening`, `cron started` содержат `version` и `commit`.

`/internal/version` движка закрыт nginx и внешним запросом недоступен.

При dev-сборке (нет `-X` флагов) version = `dev`, commit = `""`. Кнопка неактивна на dev-сборке.

## Каналы обновлений

| Канал | Описание | Цель |
|-------|----------|------|
| `release` | по умолчанию при `install.sh` | максимальный тег `vX.Y.Z`, который является предком `origin/main` |
| `main` | применяется для своего прода | верхушка `origin/main` |

Канал задаётся в `/etc/emtds/update.env` (ключ `EMTDS_UPDATE_CHANNEL`) и не меняется агентом самостоятельно.

## Первая установка агента (`update.sh --install`)

`install.sh` вызывает `update.sh --install` автоматически, если на сервере есть git-дерево и systemd. Вручную — из каталога кода:

```bash
sudo bash deploy/update.sh --install \
  --repo git@github.com:<owner>/emtds.git \
  --channel release \
  --tds-dir /opt/emtds
```

**Что создаётся:**

| Путь | Права | Содержимое |
|------|-------|------------|
| `/etc/emtds/update.env` | 0600 root | настройки агента |
| `/etc/emtds/deploy_key` | 0600 root | приватный ключ SSH (создаётся, если нет) |
| `/etc/emtds/deploy_key.pub` | — | публичная часть (печатается в терминал) |
| `/etc/emtds/known_hosts` | 0644 root | отпечаток хоста git-сервера |
| `/var/lib/emtds-update/` | 0700 root | история, логи прогонов, сборки |
| `/var/lib/emtds-update/inbox/` | 0700 10001:10001 | входящие запросы кнопки |
| `/var/backups/emtds-update/` | 0700 root | архивы бэкапов |
| `/usr/local/lib/emtds/update.sh` | 0755 root | рабочая копия скрипта |
| `/etc/systemd/system/emtds-update.service` | 0644 | oneshot-юнит |
| `/etc/systemd/system/emtds-update.timer` | 0644 | таймер 60 с |

После установки таймер включается: `systemctl enable --now emtds-update.timer`.

## Deploy key

Скрипт создаёт ключ `ssh-keygen -t ed25519` и **печатает только публичную часть** с инструкцией:

```
=== Deploy key (добавьте в GitHub → Settings → Deploy keys) ===
ssh-ed25519 AAAA... emtds-deploy@hostname
=== Только read-only доступ ===
```

Добавьте публичный ключ в **GitHub → Settings → Deploy keys** без галочки Write access. Приватный ключ никогда не печатается, не логируется и не попадает в том.

Если репозиторий на GitHub, скрипт сам запишет `/etc/emtds/known_hosts`, сверив отпечаток с опубликованным. Для других git-серверов — вручную:

```bash
ssh-keyscan -t ed25519 git.example.com | sudo tee /etc/emtds/known_hosts
# Сверьте отпечаток с официальной документацией git-сервера.
```

## update.env

Файл `/etc/emtds/update.env` (права 0600, владелец root). Принимаются только ключи из белого списка; любой неизвестный ключ или команда вызывают отказ.

```ini
EMTDS_UPDATE_REPO=git@github.com:<owner>/emtds.git
EMTDS_UPDATE_CHANNEL=release
EMTDS_UPDATE_CHECK_HOURS=6
EMTDS_UPDATE_KEEP_IMAGES=3
EMTDS_UPDATE_KEEP_BACKUPS=5
TDS_DIR=/opt/emtds
```

| Ключ | Значение |
|------|----------|
| `EMTDS_UPDATE_REPO` | SSH-URL репозитория (`git@…`, `ssh://git@…` или `https://…`); учётные данные в URL запрещены |
| `EMTDS_UPDATE_CHANNEL` | `release` или `main` |
| `EMTDS_UPDATE_CHECK_HOURS` | интервал проверки в часах (по умолчанию 6) |
| `EMTDS_UPDATE_KEEP_IMAGES` | сколько образов `emtds:<sha12>` хранить (по умолчанию 3) |
| `EMTDS_UPDATE_KEEP_BACKUPS` | сколько бэкапов хранить (по умолчанию 5) |
| `TDS_DIR` | абсолютный путь к каталогу кода (git-репо) |

## Подпись (SSH-подпись)

### Настройка разработчика

```bash
git config --global gpg.format ssh
git config --global user.signingkey ~/.ssh/id_ed25519.pub
git config --global tag.gpgSign true
git config --global commit.gpgSign true
```

- **Канал `release`** — проверяется подпись тега (`git verify-tag`).
- **Канал `main`** — проверяется подпись коммита (`git verify-commit`).

### allowed_signers на сервере

Файл `/etc/emtds/allowed_signers` создаётся вручную (не берётся из репозитория). Формат строки — стандартный git SSH allowed_signers:

```
developer@example.com namespaces="git" ssh-ed25519 AAAA...
```

Каждая строка — один ключ-разработчик. Скрипт при установке напоминает о создании файла, если его нет.

Пока файл отсутствует или пуст, флаг `signing=false` в `agent.json` → кнопка неактивна, таймер не применяет обновления. CLI без подписи требует явный флаг `--unsigned`.

## Обновление кнопкой

**Профиль → Обновления** (только администратор). Кнопка «Обновить» неактивна, если:

| Код блокировки | Причина |
|----------------|---------|
| `updater_absent` | `agent.json` старше 10 минут или агент не видит inbox |
| `unsigned` | подпись не настроена (`signing=false`) или цель не подписана |
| `no_update` | нет доступного обновления или `check.json` старше 24 часов |
| `manual_steps` | изменения требуют ручных шагов (добавьте `--schema-ok` в CLI) |
| `keitaro_running` | идёт импорт из Keitaro |
| `request_pending` | запрос уже ждёт обработки |
| `running` | прогон уже выполняется |
| `dev_build` | текущая сборка dev |

Нажатие кнопки требует ввести первые 7 символов SHA целевого коммита. Запрос пишется агентом из admin в inbox (`/var/lib/emtds-update/inbox/request`) атомарно. Таймер `emtds-update.timer` забирает файл при следующем запуске (раз в 60 секунд) и запускает прогон.

Кнопка «Проверить сейчас» — не чаще одного раза в 5 минут.

API-ключ не может вызвать `/api/update/apply`, `/api/update/rollback`, `/api/update/check` — только cookie-сессия администратора.

## Обновление командой (CLI)

Все команды запускаются от root; рабочая копия скрипта — `/usr/local/lib/emtds/update.sh`.

```bash
# Проверить наличие обновления, записать check.json (без применения):
sudo /usr/local/lib/emtds/update.sh --check

# Обновить по каналу из update.env:
sudo /usr/local/lib/emtds/update.sh --channel

# Обновить до конкретного SHA или тега:
sudo /usr/local/lib/emtds/update.sh --to abc123...

# Показать план без изменений (fetch разрешён):
sudo /usr/local/lib/emtds/update.sh --dry-run

# Показать состояние:
sudo /usr/local/lib/emtds/update.sh --status

# Откат на предыдущую версию:
sudo /usr/local/lib/emtds/update.sh --rollback
```

**Модификаторы:**

| Флаг | Когда использовать |
|------|--------------------|
| `--unsigned` | применить без проверки подписи (только CLI) |
| `--schema-ok` | подтвердить выполнение ручных шагов (миграции, схема) |
| `--keitaro-idle` | пропустить гейт Keitaro — подтвердить, что перенос не запущен |
| `--force-version` | подтвердить понижение версии интерактивно |

## Что происходит при обновлении

1. **Захват lock** `/run/emtds-update.lock` — два прогона не запускаются одновременно.
2. **Проверка окружения** — нет контейнеров `kuztds-*`, есть `server.env` и `compose.app.yml`, TDS_DIR — git-репо.
3. **git fetch** через deploy key со `StrictHostKeyChecking=yes`.
4. **Выбор и проверка цели** — по каналу или по `--to`. Понижение версии без `--force-version` запрещено.
5. **Проверка подписи** — `git verify-tag` (release) или `git verify-commit` (main) по `/etc/emtds/allowed_signers`.
6. **Ручные шаги** — если diff `HEAD..цель` затрагивает `migrations/clickhouse/`, `internal/store/schema*.go` или `deploy/docker-compose.yml`, прогон останавливается и перечисляет файлы. Флаг `--schema-ok` подтверждает, что шаги выполнены.
7. **Проверка ресурсов** — MemAvailable ≥ 2 ГБ, свободно на `/var/lib/docker` ≥ 5 ГБ, ClickHouse `/ping` и Redis `PING` отвечают.
8. **Бэкап** (каталог 0700, umask 077): архив `STATE_DIR` и `.env`; снимок тома без `geo/` через `docker run --user 10001 --network none`. Хранятся 5 последних.
9. **Сборка** — `docker build` из `git archive <sha>` в тег `emtds:<sha12>`. Если сборка упала — прогон завершается, диск не изменён.
10. **Гейт Keitaro** — агент пишет `update/pending` с id прогона; admin под своей блокировкой, убедившись, что `keitaro idle` и нет активных writers, отвечает `update/ready.json` с тем же id. Новые записи в Keitaro блокирует код `update_pending`. Если admin не отвечает 10 минут — прогон останавливается (кроме `--keitaro-idle`).
11. **Вторая фаза** — `git checkout --detach <sha>`, проверка `compose config` (порты только `127.0.0.1:`, нет `docker.sock`), `apply-edge.sh` при наличии nginx, retag `emtds:<sha12> → emtds:local`.
12. **Пересоздание сервисов** по одному: engine → admin → cron. Каждый — `up -d --no-build --force-recreate --no-deps`. ClickHouse, Redis, php не трогаются.
    - **Cron**: ожидает, пока не завершатся задачи `geo_db` и `virustotal` (до 10 минут). Если ждать дольше — cron остаётся старым с флагом `cron_lagging: true` в `state.json`.
13. **Проверка здоровья** — healthcheck Docker healthy, `clickhouse connected` и `redis connected` в логах, `/healthz` движка и `/api/health` admin отвечают.
14. **Простой engine** — несколько секунд пока контейнер поднимается. При автооткате — дважды.
15. **Финал** — `state.json`, строка в `/var/lib/emtds-update/history`, удаление `pending`, очистка старых образов и `docker builder prune`.

**Провал второй фазы** — агент откатывает: retag `emtds:<prev12> → emtds:local`, checkout предыдущего SHA, apply-edge, пересоздание engine/admin/cron. Если при этом ClickHouse или Redis недоступны — откат не делается, итог `failed` с причиной «инфраструктура».

## Откат

```bash
sudo /usr/local/lib/emtds/update.sh --rollback
```

Откат на предыдущую версию из `/var/lib/emtds-update/history`. Образ `emtds:<prev12>` должен быть на хосте. Это тот же прогон через вторую фазу, только без сборки.

**Автооткат** при провале второй фазы выполняется агентом без участия оператора.

**Схема БД при откате** — автоматического отката SQL-миграций нет. Если при обновлении применялась миграция `migrations/clickhouse/`, откатить схему нужно вручную. Флаг `--schema-ok` при следующем применении подтверждает, что шаги выполнены.

## Процесс релиза

1. Коммиты идут в ветку `main`, CI-тесты зелёные.
2. Разработчик ставит аннотированный тег с подписью:
   ```bash
   git tag -a -s v1.2.3 -m "Release v1.2.3\n\nDescription"
   git push origin v1.2.3
   ```
3. Workflow `.github/workflows/release.yml` при push тега `v*.*.*`:
   - прогоняет `go test -count=1 ./...`;
   - проверяет, что тег аннотированный и его коммит — предок `origin/main`;
   - собирает release notes из аннотации тега и `git log <prev>..<tag>`;
   - создаёт GitHub release через `gh release create`.

Actions закреплены по SHA. `permissions: contents: write` — только у job релиза.

## Файловый контракт (`/var/lib/emtds/update/`)

Каталог `update/` живёт рядом с `groups.json` внутри тома. Каждый файл имеет ровно одного писателя.

| Файл | Писатель | Содержимое |
|------|----------|------------|
| `agent.json` | updater | пульс каждые 5 минут: версия скрипта, repo, channel, signing, images |
| `check.json` | updater | результат последней проверки: текущий SHA, цель, список коммитов, notes тега |
| `state.json` | updater | ход и итог прогона: id, action, phases, result, reason |
| `log.txt` | updater | хвост лога прогона ≤ 256 КиБ (маскированный) |
| `pending` | updater | id прогона, ждущего гейт Keitaro |
| `ready.json` | admin | подтверждение idle Keitaro с тем же id |
| `announced.json` | cron | дедуп Telegram-уведомлений |
| `request` | admin | запрос кнопки (читает updater через `--timer`) |

## Коды отказов и неполадки

| Ситуация | Что делать |
|----------|------------|
| «агент не установлен» (`updater_absent`) | запустите `update.sh --install`, проверьте таймер `systemctl status emtds-update.timer` |
| Кнопка не активна — `unsigned` | создайте `/etc/emtds/allowed_signers` и убедитесь, что тег/коммит подписан |
| `request_pending` держится, прогон не начинается | зависший `request` в inbox чистится таймером при следующем запуске; проверьте логи `journalctl -u emtds-update` |
| Lock `/run/emtds-update.lock` занят | другой прогон активен; дождитесь завершения или перезагрузки. `--status` покажет состояние без захвата lock |
| Перенос из Keitaro блокирует обновление | дождитесь `GET /api/keitaro/job` → `running: false`, или передайте `--keitaro-idle` |
| Прогон завис, `state.json` показывает `running` | таймер при следующем захвате lock найдёт зависший `pending` и запишет `result: failed` с причиной `interrupted` |
| Ручные шаги после `--schema-ok` | выполните миграции вручную, затем повторно запустите с `--schema-ok` |

**Где смотреть логи:**

```bash
# Таймер и прогоны:
journalctl -u emtds-update -n 100 --no-pager

# Полный лог конкретного прогона:
cat /var/lib/emtds-update/runs/<id>.log

# Лог в томе (хвост ≤ 256 КиБ):
# раздел «Обновления» → лог в интерфейсе; или GET /api/update/log
```
