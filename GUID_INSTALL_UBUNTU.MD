# Установка EmTDS на чистый Ubuntu

Гайд для сервера с Ubuntu 22.04 или 24.04 (Debian 12 тоже подходит). Все команды выполняются от root (`sudo -i`). Паролей и токенов в гайде нет: их создаёт установщик на сервере, а владельца вы задаёте сами при первом входе.

## 1. Требования

- 4 ядра, 16 ГБ RAM, 80 ГБ диска. На меньшем установщик предупредит, на совсем малом откажется.
- Публичный IPv4. Порты 80 и 443 открыты наружу.
- Домены за Cloudflare с проксированием (оранжевое облако):
  - `example.com` и любые домены кампаний ведут на движок;
  - `admin.example.com` ведёт на админку.

  A-записи указывают на IP сервера. В Cloudflare режим SSL — **Full**, не Full (strict): сервер отдаёт самоподписанный сертификат, посетитель видит Universal SSL Cloudflare. Сертификаты Let's Encrypt не нужны.

## 2. Пакеты: Docker, git, nginx

Установщик ставит недостающее сам. Нужен nginx **1.25.1 или новее**: конфиги EmTDS используют директиву `http2 on`. В Ubuntu 22.04 и 24.04 и в Debian 12 nginx из репозитория дистрибутива старее (в 24.04 это 1.24), поэтому установщик ставит nginx из официального репозитория nginx.org:

- nginx нет — ставится пакет nginx.org;
- nginx 1.25.1 или новее уже стоит — его не трогают;
- стоит старый nginx без своих сайтов (только стандартные конфиги пакета) — копия `/etc/nginx` уходит в `/etc/emtds/nginx-backup/`, пакет дистрибутива удаляется, ставится nginx.org;
- стоит старый nginx со своими сайтами — установщик останавливается: обновите nginx вручную или запустите с `--no-edge`.

Если хотите поставить заранее или проверить:

```bash
apt-get update
apt-get install -y ca-certificates curl git gnupg
curl -fsSL https://nginx.org/keys/nginx_signing.key -o /tmp/nginx_signing.key
gpg --show-keys /tmp/nginx_signing.key
# в выводе должны быть ровно эти отпечатки:
# 573BFD6B3D8FBC641079A6ABABF5BD827BD9BF62
# 8540A6F18833A80E9C1653A42FD21310B49F6B46
# 9E9BE90EACBCDE69FE9B204CBCDCD8A38D88A2B3
gpg --dearmor -o /usr/share/keyrings/nginx-archive-keyring.gpg /tmp/nginx_signing.key
echo "deb [signed-by=/usr/share/keyrings/nginx-archive-keyring.gpg] https://nginx.org/packages/$(. /etc/os-release && echo "$ID $VERSION_CODENAME") nginx" > /etc/apt/sources.list.d/nginx.list
printf 'Package: *\nPin: origin nginx.org\nPin: release o=nginx\nPin-Priority: 900\n' > /etc/apt/preferences.d/99nginx
apt-get update
apt-get install -y nginx
systemctl enable --now nginx
install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo "$VERSION_CODENAME") stable" > /etc/apt/sources.list.d/docker.list
apt-get update
apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
docker compose version
git --version
nginx -v
```

`nginx -v` должен показать 1.25.1 или новее.

Конфиги nginx руками не правьте: файлы `emtds*` кладёт и проверяет `deploy/apply-edge.sh` (бэкап, `nginx -t`, откат при ошибке). Его вызывают установщик и каждое обновление. Пакет nginx.org подключает только `/etc/nginx/conf.d/*.conf`, туда и ложатся файлы `emtds*`.

## 3. Код в /opt/emtds

Каталог `/opt/emtds` должен быть пустым или отсутствовать. Есть два равных способа.

**Однострочник.** Установщик сам клонирует репозиторий в `/opt/emtds`:

```bash
curl -fsSLo /root/emtds-install.sh https://raw.githubusercontent.com/hasayone/empataso-tds/main/deploy/install.sh
bash /root/emtds-install.sh --repo https://github.com/hasayone/empataso-tds.git --channel main
```

Скрипт запускайте файлом через `bash`, не через `curl | bash`.

**Клон вручную.** Те же шаги, но клон делаете вы:

```bash
git clone https://github.com/hasayone/empataso-tds.git /opt/emtds
cd /opt/emtds
bash deploy/install.sh --repo https://github.com/hasayone/empataso-tds.git --channel main
```

`--channel main` — сервер берёт каждый новый коммит ветки `main`. Без флага канал `release`: только теги `vX.Y.Z`. Адрес репозитория в скрипт не зашит, его всегда передаёт `--repo`.

## 4. Что делает установщик

1. Проверяет ресурсы, ставит Docker и git, если их нет, и nginx 1.25.1+ из nginx.org (см. раздел 2).
2. Создаёт `/etc/emtds`:
   - `server.env` — порты только на `127.0.0.1` и `EMTDS_ENV=production`, по нему «О системе» показывает Production;
   - лимиты контейнеров;
   - `up.sh` для повторного старта.
3. Пишет `/opt/emtds/.env` с паролем ClickHouse и токеном первого входа. Файл только для root, в git он не попадает.
4. Поднимает ClickHouse и Redis (`emtds-infra`), собирает образ с вшитым коммитом и запускает engine, admin и cron (`emtds-app`).
5. Применяет nginx через `apply-edge.sh`:
   - `https://<IP>/` и `admin.<домен>` ведут на админку;
   - все остальные домены ведут на движок;
   - `admin.<домен>` принимает запросы только от Cloudflare;
   - порт 80 перенаправляет на https того же адреса (`http://<IP>/` → `https://<IP>/`). Стандартная страница «Welcome to nginx» (`conf.d/default.conf` пакета nginx.org или `sites-enabled/default` Ubuntu) отключается, копия лежит в `/etc/emtds/nginx-backup/<время>/stock/`. Если порт 80 уже занят другим сервером (например, панелью хостинга), редирект не ставится и чужой сервер не трогается.
6. Ставит агент обновлений `/usr/local/lib/emtds/update.sh` с таймером systemd и сторож `emtds-watchdog.timer`.

В конце установщик печатает адрес входа и **токен первого входа**. Сохраните токен: он нужен один раз при создании владельца. Повторно его можно прочитать на сервере (только root):

```bash
grep '^EMTDS_SETUP_TOKEN=' /opt/emtds/.env | cut -d= -f2
```

Не пересылайте токен в чаты и не кладите в git.

## 5. Проверка

```bash
curl -fsS http://127.0.0.1:8090/api/health && echo admin ok
curl -fsS http://127.0.0.1:18080/healthz && echo engine ok
IP=$(grep '^EMTDS_PUBLIC_IP=' /etc/emtds/server.env | cut -d= -f2)
curl -sI "http://${IP}/" | head -n 3    # 301 и Location: https://<IP>/
curl -kfsS "https://${IP}/api/setup" && echo    # {"needed":true} до создания владельца
docker logs emtds-engine 2>&1 | grep -E 'clickhouse connected|redis connected'
docker logs emtds-admin 2>&1 | grep -E 'clickhouse connected|redis connected'
systemctl list-timers | grep emtds
```

## 6. Первый вход

Откройте `https://admin.<ваш домен>/` или `https://<IP>/` (`http://<IP>/` перенаправит туда же). По IP сервер отдаёт самоподписанный сертификат, поэтому браузер покажет предупреждение «Подключение не защищено»: нажмите «Дополнительно» → «Перейти на сайт». Это ожидаемо, через домен за Cloudflare предупреждения нет.

Пока владельца нет, админка показывает форму: логин, почта, пароль и токен первого входа (раздел 4). После отправки формы владелец создан, токен больше не действует.

Если вместо админки открывается «Welcome to nginx», значит порт 80 обслуживает другой сервер: откройте `https://<IP>/` напрямую или посмотрите строку «порт 80 занят» в выводе `apply-edge.sh`.

## 7. Обновления

- Кнопка «Обновить» в «Настройки → О системе» только кладёт запрос в inbox. Его исполняет хостовый агент: бэкап, сборка образа, `apply-edge.sh`, пересоздание engine, admin и cron. У админки нет доступа к Docker.
- Жёлтая точка у аватара и значок у пункта «О системе» означают, что есть коммит новее.
- Красная точка и красный треугольник означают, что движок, ClickHouse или Redis не отвечают. Откроется окно со списком служб. Перезапустите сервер или службу, состояние видно в «О системе».
- По SSH: `sudo /usr/local/lib/emtds/update.sh --check`, затем `sudo /usr/local/lib/emtds/update.sh --channel`.

Не делайте на сервере вручную:

- `docker compose build`;
- копирование кода в `/opt/emtds`;
- `docker compose down`;
- правку томов.

## 8. Повторный старт и удаление

- Поднять стек после перезагрузки: `/etc/emtds/up.sh` (обычно не нужно, контейнеры с `restart: unless-stopped`).
- Повторный `install.sh` блокирует маркер `/etc/emtds/installed`. Чтобы поставить с нуля, сначала удалите прежнюю установку:
  - контейнеры и тома проектов `emtds-app` и `emtds-infra`;
  - `/opt/emtds`, `/etc/emtds`, `/var/lib/emtds-update`;
  - файлы `emtds*` в `/etc/nginx/conf.d` и `/etc/nginx/emtds` (стандартную страницу nginx при желании верните из `/etc/emtds/nginx-backup/*/stock/`);
  - таймеры `emtds-*`.

  Это удалит все данные трекера.
