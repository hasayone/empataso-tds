# Партнёрские сети

`GET|POST /api/affiliate-networks` и `GET|PUT|DELETE /api/affiliate-networks/{id}`.

Поля: `id`, `name`, `status`, `postback_url`, `offer_param`, `offer_query`.

`postback_url` — заметка для кабинета сети. Движок её не вызывает и вторую очередь не поднимает.

`offer_query` — строка вроде `sub1={subid}`. Она дописывается к ссылке, которую видит посетитель, после того как оффер уже узнан по адресу без этой склейки. Уже стоящий query не затирается. Адрес длиннее 500 символов, с пробелом или со схемой `://` даёт `400`.

Шаблон по умолчанию: `{engine}/postback?subid={subid}&status={status}&payout={payout}`. На странице две кнопки копируют ту же ссылку со `status=lead` и `status=sale`.

У оффера поле `network_id`. Закрытый каталог Keitaro не скачивается и на сервер не пишется.
