package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/Ivanvnew75/libs/common"
)

type Config struct {
	Port    string
	Service string
	Version string

	// TelegramToken — секрет. Приходит из Kubernetes Secret,
	// в ConfigMap и в git его нет.
	TelegramToken string
	// TelegramAPIURL — Фактор 4: внешний API тоже подключаемый ресурс.
	// В тестах сюда подставляется заглушка.
	TelegramAPIURL string

	// UsersServiceURL — адрес соседнего микросервиса.
	//
	// Именно переменная окружения, а не «найдём по DNS сами».
	// Сервис не должен знать ни про namespace, ни про домен кластера:
	// в другом окружении users может стоять за ingress по другому имени.
	UsersServiceURL string

	// PollingEnabled — переключатель типа процесса (Фактор 8).
	// Один и тот же образ работает либо веб-процессом (принимает
	// POST /send), либо процессом-опросчиком. См. комментарий в bot/poller.go
	// о том, почему опросчик обязан быть в одном экземпляре.
	PollingEnabled bool
	PollTimeout    time.Duration

	Question string

	// AnswerSink — куда уходит ответ пользователя: "users" (синхронный
	// вызов сервиса users, прежнее поведение) или "kafka".
	//
	// Значение по умолчанию — "users", то есть СТАРОЕ поведение.
	// Это принципиально: выкатка нового образа не должна менять поведение
	// сама по себе. Новый путь включается отдельным действием — правкой
	// ConfigMap, которую легко откатить, не пересобирая образ.
	AnswerSink string

	// LinkSecret и WebAdminURL — выдача ссылки на веб-кабинет.
	// Секрет ОБЩИЙ с сервисом web-admin: бот подписывает, кабинет
	// проверяет. Общий симметричный ключ здесь уместен, потому что
	// обе стороны наши; для внешнего потребителя понадобилась бы
	// асимметричная подпись, чтобы не раздавать возможность подписывать.
	//
	// Пустой LinkSecret означает «команда /kabinet выключена»,
	// а не «подписываем пустым ключом». Второе было бы тихой дырой:
	// токен подписывался бы предсказуемым ключом и подделывался кем угодно.
	LinkSecret  string
	WebAdminURL string

	// KafkaBrokers обязателен только при AnswerSink=kafka.
	// Проверка условная (см. Load) — требовать адрес брокера от сервиса,
	// который в Kafka не пишет, значило бы заставить всех, кто ещё
	// не переехал, придумывать фиктивное значение.
	KafkaBrokers []string

	LogLevel        string
	LogFormat       string
	ShutdownTimeout time.Duration
	HTTPTimeout     time.Duration
	HTTPRetries     int
}

func Load(version string) (Config, error) {
	c := Config{
		Port:           common.Env("SERVER_PORT", "8080"),
		Service:        "telegram-api",
		Version:        version,
		TelegramAPIURL: common.Env("TELEGRAM_API_URL", "https://api.telegram.org"),
		PollingEnabled: common.Env("POLLING_ENABLED", "false") == "true",
		Question:       common.Env("MOOD_QUESTION", "Как вы себя чувствуете?"),
		AnswerSink:     common.Env("ANSWER_SINK", "users"),
		LinkSecret:     common.Env("LINK_SECRET", ""),
		WebAdminURL:    common.Env("WEB_ADMIN_URL", ""),
		LogLevel:       common.Env("LOG_LEVEL", "info"),
		LogFormat:      common.Env("LOG_FORMAT", "json"),
	}

	var err error
	if c.TelegramToken, err = common.MustEnv("TELEGRAM_TOKEN"); err != nil {
		return c, err
	}
	if c.UsersServiceURL, err = common.MustEnv("USERS_SERVICE_URL"); err != nil {
		return c, err
	}
	// 25 секунд, а не 60: Telegram допускает до 50, но длинный опрос
	// удлиняет и время завершения процесса. При выкатке под должен
	// успеть закрыть текущий запрос в пределах grace period.
	if c.PollTimeout, err = common.EnvDuration("POLL_TIMEOUT", 25*time.Second); err != nil {
		return c, err
	}
	if c.ShutdownTimeout, err = common.EnvDuration("SHUTDOWN_TIMEOUT", 15*time.Second); err != nil {
		return c, err
	}
	if c.HTTPTimeout, err = common.EnvDuration("HTTP_TIMEOUT", 5*time.Second); err != nil {
		return c, err
	}
	if c.HTTPRetries, err = common.EnvInt("HTTP_RETRIES", 2); err != nil {
		return c, err
	}

	// Проверка выдачи ссылок нужна ТОЛЬКО опросчику.
	//
	// Один образ работает двумя типами процессов (Фактор 8), и требования
	// к конфигурации у них разные: команду /kabinet обрабатывает опросчик,
	// web-процесс о ней не знает. Требовать LINK_SECRET у web-процесса
	// значило бы отдавать ему секрет, которым он не пользуется, — прямое
	// нарушение least privilege ради единообразия проверки.
	//
	// Наступил на это на стенде: ConfigMap общий у обоих Deployment,
	// WEB_ADMIN_URL приехал обоим, а LINK_SECRET смонтирован только
	// опросчику — и web-процесс ушёл в CrashLoopBackOff. Ошибка была
	// в проверке, а не в развёртывании.
	if c.PollingEnabled {
		// Секрет без адреса и адрес без секрета — всегда ошибка
		// конфигурации, и узнать о ней надо на старте, а не от
		// пользователя, которому бот прислал ссылку на пустой хост.
		if (c.LinkSecret == "") != (c.WebAdminURL == "") {
			return c, fmt.Errorf("LINK_SECRET и WEB_ADMIN_URL задаются только вместе")
		}
		// Короткий ключ для HMAC формально работает — именно поэтому
		// ошибку легко не заметить: подписи считаются, всё «функционирует».
		if c.LinkSecret != "" && len(c.LinkSecret) < 32 {
			return c, fmt.Errorf("LINK_SECRET должен быть не короче 32 символов")
		}
	} else {
		// web-процесс ссылок не выдаёт: обнуляем, чтобы случайно
		// пробравшийся в окружение секрет не оказался в памяти процесса,
		// которому он не нужен.
		c.LinkSecret, c.WebAdminURL = "", ""
	}

	switch c.AnswerSink {
	case "users":
		// адрес брокера не нужен
	case "kafka":
		brokers, err := common.MustEnv("KAFKA_BROKERS")
		if err != nil {
			return c, err
		}
		c.KafkaBrokers = strings.Split(brokers, ",")
	default:
		// Явный список допустимых значений вместо «что не kafka, то users».
		// Опечатка ANSWER_SINK=kafla должна ронять под на старте,
		// а не тихо оставлять старое поведение: молчаливый откат
		// к прежнему пути — самый неприятный вид отказа, потому что
		// его никто не замечает.
		return c, fmt.Errorf("ANSWER_SINK: недопустимое значение %q, ожидается users или kafka", c.AnswerSink)
	}

	return c, nil
}
