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
