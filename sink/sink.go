// Package sink — куда уезжает ответ пользователя.
//
// ЗАЧЕМ ЗДЕСЬ ИНТЕРФЕЙС, А НЕ ПРОСТО ЗАМЕНА ВЫЗОВА.
//
// До курса Next Level telegram-api вызывал users.SaveAnswer синхронно.
// Теперь ответ должен уходить в Kafka, откуда его разбирают два разных
// потребителя. Соблазн — просто поменять вызов. Так делать нельзя:
// выкатка нового кода и переключение поведения слились бы в одно событие,
// и при первой же проблеме единственный способ отката — откатить образ.
//
// Интерфейс + переменная окружения ANSWER_SINK разделяют эти два события:
//
//  1. выкатили новый образ с ANSWER_SINK=users — поведение прежнее,
//     риск нулевой, но код с Kafka уже в проде;
//  2. подняли Kafka и answers, проверили на dev;
//  3. переключили переменную — новое поведение;
//  4. что-то пошло не так — вернули переменную, БЕЗ пересборки и выкатки.
//
// Это то, что на собеседовании называют «отделить деплой от релиза»,
// и здесь оно стоит двадцати строк кода.
package sink

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/Ivanvnew75/libs/events"
	"github.com/Ivanvnew75/libs/kafkax"
	"github.com/segmentio/kafka-go"
)

// Sink принимает событие «пользователь ответил».
type Sink interface {
	Save(ctx context.Context, e events.AnswerReceived) error
	Close() error
}

// ---------- Kafka ----------

type kafkaSink struct{ w *kafka.Writer }

// NewKafka — продюсер в топик answers.v1.
func NewKafka(brokers []string, log *slog.Logger) Sink {
	return &kafkaSink{w: kafkax.NewWriter(brokers, events.TopicAnswers, log)}
}

func (s *kafkaSink) Save(ctx context.Context, e events.AnswerReceived) error {
	body, err := e.Marshal()
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	// Ключ = user_id: все ответы одного человека в одной партиции,
	// значит в гарантированном порядке. См. events.AnswerReceived.Key.
	return kafkax.WriteJSON(ctx, s.w, e.Key(), body)
}

func (s *kafkaSink) Close() error { return s.w.Close() }

// ---------- users (прежнее поведение) ----------

// UsersAPI — то, что умеет старый клиент сервиса users.
// Объявлено здесь, а не импортировано, чтобы sink не зависел от
// usersclient: интерфейс определяется НА СТОРОНЕ ПОТРЕБИТЕЛЯ —
// это идиома Go и то, что делает пакет тестируемым без сети.
type UsersAPI interface {
	SaveAnswer(ctx context.Context, userID int64, question, answer string) error
}

type usersSink struct{ users UsersAPI }

func NewUsers(u UsersAPI) Sink { return &usersSink{users: u} }

func (s *usersSink) Save(ctx context.Context, e events.AnswerReceived) error {
	return s.users.SaveAnswer(ctx, e.UserID, e.Question, e.Answer)
}

func (s *usersSink) Close() error { return nil }

// NewEvent собирает событие из данных сообщения.
//
// occurred_at берётся из времени сообщения Telegram, а не time.Now():
// при переигрывании топика или при отставании потребителя now() дал бы
// время ОБРАБОТКИ, и вся аналитика «динамики настроения» поехала бы.
func NewEvent(eventID, requestID string, userID, telegramID int64,
	question, answer string, occurredAt time.Time) events.AnswerReceived {
	if occurredAt.IsZero() {
		occurredAt = time.Now().UTC()
	}
	return events.AnswerReceived{
		EventID:    eventID,
		Schema:     events.SchemaVersion,
		OccurredAt: occurredAt.UTC(),
		UserID:     userID,
		TelegramID: telegramID,
		Question:   question,
		Answer:     answer,
		RequestID:  requestID,
	}
}
