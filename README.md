# telegram-api

Микросервис приложения mood-bot: приём и отправка сообщений через Telegram-бота.

Два **типа процессов** из одного образа (Фактор 8):

| Процесс | `POLLING_ENABLED` | Реплик | Что делает |
|---|---|---|---|
| web | `false` | сколько угодно | принимает `POST /send` от scheduler |
| poller | `true` | **ровно 1** | long polling `getUpdates`, обрабатывает сообщения |

Опросчик обязан быть один: Telegram отдаёт апдейты через курсор на своей
стороне и на второго опросчика отвечает `409 Conflict`.

## Переменные окружения

| Переменная | Обязательна | По умолчанию |
|---|---|---|
| `TELEGRAM_TOKEN` | да | — |
| `USERS_SERVICE_URL` | да | — |
| `TELEGRAM_API_URL` | нет | `https://api.telegram.org` |
| `POLLING_ENABLED` | нет | `false` |
| `POLL_TIMEOUT` | нет | `25s` |
| `MOOD_QUESTION` | нет | `Как вы себя чувствуете?` |
| `SERVER_PORT` | нет | `8080` |
| `LOG_LEVEL` / `LOG_FORMAT` | нет | `info` / `json` |
