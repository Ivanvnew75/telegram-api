// Package bot — обработка входящих сообщений через long polling.
package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Ivanvnew75/libs/common"

	"github.com/Ivanvnew75/telegram-api/telegram"
	"github.com/Ivanvnew75/telegram-api/usersclient"
)

// Poller опрашивает Telegram и обрабатывает сообщения.
//
// ─────────────────────────────────────────────────────────────────────
// ПОЧЕМУ LONG POLLING, А НЕ WEBHOOK
//
// Webhook требует публичный HTTPS-эндпоинт с валидным сертификатом:
// Telegram сам приходит к вам. У локального kind-кластера такого адреса
// нет. Long polling работает из-за NAT и без белого IP — сервис сам
// ходит наружу. Цена: небольшая задержка и постоянное открытое
// соединение к api.telegram.org.
//
// На собеседовании это хороший ответ на «как выбирали»: webhook
// эффективнее и масштабируется горизонтально, polling — работает
// где угодно и не требует инфраструктуры входящего трафика.
//
// # ПОЧЕМУ ОПРОСЧИК ДОЛЖЕН БЫТЬ В ОДНОМ ЭКЗЕМПЛЯРЕ
//
// getUpdates — это очередь с курсором на стороне Telegram. Два процесса,
// опрашивающих одного бота, получают ошибку 409 Conflict: «terminated by
// other getUpdates request». Даже без ошибки они бы делили сообщения
// случайным образом.
//
// Отсюда прямое следствие для Фактора 8 (Concurrency): у приложения
// РАЗНЫЕ типы процессов с разными свойствами масштабирования.
// web-процесс (POST /send) масштабируется репликами свободно;
// poller — принципиально одиночный. Поэтому они выкатываются разными
// Deployment'ами из одного и того же образа, а не replicas: 3 на всё.
// ─────────────────────────────────────────────────────────────────────
type Poller struct {
	tg       *telegram.Client
	users    *usersclient.Client
	log      *slog.Logger
	question string

	// offset хранится в памяти процесса.
	//
	// Это осознанный компромисс, а не нарушение Фактора 6 (Processes).
	// Настоящее состояние — курсор очереди — живёт на стороне Telegram:
	// он двигается, когда мы передаём offset. При рестарте пода мы
	// стартуем с offset=0 и получаем непрочитанные апдейты заново —
	// то есть теряем не данные, а максимум идемпотентную переобработку.
	offset int64
}

func New(tg *telegram.Client, users *usersclient.Client, log *slog.Logger, question string) *Poller {
	return &Poller{tg: tg, users: users, log: log, question: question}
}

// Run крутит цикл опроса, пока не отменят контекст.
func (p *Poller) Run(ctx context.Context, pollTimeout time.Duration) {
	p.log.Info("poller started", slog.Duration("poll_timeout", pollTimeout))

	// backoff растёт при ошибках и сбрасывается при успехе.
	// Без него сервис при недоступном Telegram будет молотить запросами
	// в цикле без пауз, съедая CPU и упираясь в rate limit.
	backoff := time.Second
	const maxBackoff = 30 * time.Second

	for {
		if ctx.Err() != nil {
			p.log.Info("poller stopped")
			return
		}

		updates, err := p.tg.GetUpdates(ctx, p.offset, pollTimeout)
		if err != nil {
			// Отмена контекста — это штатное завершение, а не сбой.
			if errors.Is(err, context.Canceled) || ctx.Err() != nil {
				p.log.Info("poller stopped")
				return
			}

			var apiErr *telegram.APIError
			if errors.As(err, &apiErr) && apiErr.Code == 409 {
				// Именно тот случай, ради которого poller одиночный.
				// Логируем внятно, чтобы при разборе не гадать.
				p.log.Error("другой процесс уже опрашивает этого бота — "+
					"проверьте, что Deployment опросчика запущен в одной реплике",
					slog.String("error", apiErr.Description))
			} else {
				p.log.Error("getUpdates failed", slog.String("error", err.Error()))
			}

			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < maxBackoff {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second

		for _, u := range updates {
			// Сдвигаем offset ДО обработки: апдейт, на котором обработчик
			// падает, не должен застревать навсегда и блокировать очередь.
			// «Хотя бы один раз» здесь предпочтительнее «ровно один раз»,
			// потому что обработчики идемпотентны.
			if u.UpdateID >= p.offset {
				p.offset = u.UpdateID + 1
			}
			p.handle(ctx, u)
		}
	}
}

func (p *Poller) handle(ctx context.Context, u telegram.Update) {
	if u.Message == nil || u.Message.From == nil {
		return
	}
	msg := u.Message
	text := strings.TrimSpace(msg.Text)

	// Сквозной идентификатор для входящего сообщения (Фактор 11).
	//
	// У сообщения из Telegram нет ни HTTP-запроса, ни заголовка, поэтому
	// идентификатор мы создаём сами — из update_id, который у Telegram
	// уникален и монотонен. Дальше он уходит в context, оттуда — в заголовок
	// исходящих вызовов к users, и весь путь «сообщение → регистрация →
	// сохранение ответа» ищется в логах ОДНИМ фильтром по request_id,
	// в двух разных сервисах.
	//
	// Детерминированность (не случайный UUID) даёт побочную пользу:
	// повторная обработка того же апдейта получит тот же идентификатор,
	// и в логах будет видно, что это ретрай, а не новое событие.
	requestID := fmt.Sprintf("tg-%d", u.UpdateID)
	ctx = common.WithRequestID(ctx, requestID)

	log := p.log.With(
		slog.String("request_id", requestID),
		slog.Int64("telegram_id", msg.From.ID),
		slog.Int64("update_id", u.UpdateID),
	)

	// Таймаут на обработку одного сообщения: без него зависший сосед
	// остановит весь цикл опроса.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	switch {
	case strings.HasPrefix(text, "/start"):
		name := msg.From.FirstName
		if name == "" {
			name = msg.From.Username
		}
		if name == "" {
			name = "аноним"
		}

		user, err := p.users.Register(ctx, msg.From.ID, name)
		if err != nil {
			log.Error("register failed", slog.String("error", err.Error()))
			p.reply(ctx, msg.Chat.ID, "Не получилось зарегистрировать, попробуйте позже.")
			return
		}
		log.Info("user registered", slog.Int64("user_id", user.ID))
		p.reply(ctx, msg.Chat.ID,
			"Привет, "+user.Name+"! Я буду спрашивать, как ты себя чувствуешь, дважды в день. "+
				"Просто отвечай текстом — я сохраню.")

	case text == "/help":
		p.reply(ctx, msg.Chat.ID,
			"/start — зарегистрироваться\n/help — эта справка\n"+
				"Любой другой текст я сохраню как ответ на вопрос «"+p.question+"»")

	case text == "":
		// Сообщения без текста (стикеры, фото) молча игнорируем.
		return

	default:
		user, err := p.users.GetByTelegramID(ctx, msg.From.ID)
		if errors.Is(err, usersclient.ErrNotFound) {
			p.reply(ctx, msg.Chat.ID, "Сначала отправьте /start")
			return
		}
		if err != nil {
			log.Error("lookup failed", slog.String("error", err.Error()))
			p.reply(ctx, msg.Chat.ID, "Сервис пользователей недоступен, попробуйте позже.")
			return
		}

		if err := p.users.SaveAnswer(ctx, user.ID, p.question, text); err != nil {
			log.Error("save answer failed", slog.String("error", err.Error()))
			p.reply(ctx, msg.Chat.ID, "Не получилось сохранить ответ.")
			return
		}
		log.Info("answer saved", slog.Int64("user_id", user.ID))
		p.reply(ctx, msg.Chat.ID, "Записал, спасибо!")
	}
}

func (p *Poller) reply(ctx context.Context, chatID int64, text string) {
	if err := p.tg.SendMessage(ctx, chatID, text); err != nil {
		p.log.Error("sendMessage failed", slog.String("error", err.Error()))
	}
}
